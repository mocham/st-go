package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"image"
	"io"
	"os"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
	"st-go/platform_neutral/api"
)

type pendingResource struct {
	id     uint64
	cancel context.CancelFunc
}
type dialResult struct {
	generation uint64
	c          *connection
	err        error
}
type previewState struct {
	job          resourceJob
	rect         image.Rectangle
	media        api.MediaView
	html         api.HTMLView
	path         string
	appliedMedia api.MediaView
	appliedHTML  api.HTMLView
	ready        bool
	nextInfo     time.Time
	lastStatus   api.MediaStatus
}

type previewInfo struct {
	preview *previewState
	view    api.MediaView
	until   time.Time
}

type app struct {
	tabs                                             *serviceTabs
	info                                             *previewInfo
	dialWG                                           sync.WaitGroup
	framePending                                     bool
	remote                                           *remote
	out                                              io.Writer
	fd                                               int
	reconnect, clipboardRead                         bool
	ctx                                              context.Context
	c                                                *connection
	screen                                           screen
	cache                                            *resourceCache
	resources                                        map[string]string
	pending                                          map[string]pendingResource
	failed                                           map[string]bool
	resourceID                                       uint64
	preview                                          *previewState
	queries                                          []documentRequest
	query                                            *localQuery
	queryToken                                       int
	status, clipboard                                string
	clipboardID                                      uint64
	clipboardUntil                                   time.Time
	dirty, statusDirty, quit, prefix, hello, dialing bool
	generation                                       uint64
	dialCancel                                       context.CancelFunc
	events                                           chan wireEvent
	dials                                            chan dialResult
	retryAt, heartbeatAt, lastRefresh                time.Time
	heartbeat, backoff                               time.Duration
}

func (a *app) note(s string) { a.status = s; a.statusDirty = true }
func (a *app) send(m inputMessage) error {
	if a.c == nil {
		return nil
	}
	if err := a.c.send(m); err != nil {
		return a.disconnect(err)
	}
	return nil
}
func (a *app) resize() error {
	ws, err := unix.IoctlGetWinsize(a.fd, unix.TIOCGWINSZ)
	if err != nil {
		return err
	}
	chrome := 1
	if a.tabs != nil {
		chrome = 2
	}
	if int(ws.Row) < chrome+1 || ws.Col < 8 {
		return fmt.Errorf("at least 8 columns and %d rows are required", chrome+1)
	}
	if int(ws.Col) == a.screen.cols && int(ws.Row)-chrome == a.screen.rows {
		return nil
	}
	if err := a.screen.size(int(ws.Col), int(ws.Row)-chrome); err != nil {
		return err
	}
	a.dirty, a.statusDirty = true, true
	if a.tabs != nil {
		a.tabs.lastBar = ""
	}
	if a.hello {
		w, h := a.screen.dimensions()
		if w > 65535 || h > 65535 || int64(w)*int64(h) > 16<<20 {
			return fmt.Errorf("remote viewport exceeds pixel limit")
		}
		if a.preview != nil {
			if err := a.setPreview(serverMessage{}); err != nil {
				return err
			}
		}
		return a.send(inputMessage{Type: "resize", Width: w, Height: h})
	}
	return nil
}

func (a *app) dial() {
	a.retryAt = time.Time{}
	if a.tabs != nil && (a.tabs.selected == "" || a.tabs.closing == a.tabs.selected) {
		return
	}
	if a.dialing {
		return
	}
	a.dialing = true
	a.retryAt = time.Time{}
	a.generation++
	if a.dialCancel != nil {
		a.dialCancel()
	}
	generation := a.generation
	ctx, cancel := context.WithCancel(a.ctx)
	a.dialCancel = cancel
	r := *a.remote
	a.note("Connecting · Ctrl-] q detach")
	a.dialWG.Add(1)
	go func() {
		defer a.dialWG.Done()
		c, err := r.connect(ctx, a.events)
		select {
		case a.dials <- dialResult{generation, c, err}:
		case <-ctx.Done():
			if c != nil {
				c.close()
			}
		}
	}()
}

func (a *app) disconnect(err error) error {
	if a.c != nil {
		a.c.close()
		a.c = nil
	}
	a.hello = false
	a.heartbeatAt = time.Time{}
	a.clipboardID = 0
	for key, p := range a.pending {
		p.cancel()
		delete(a.pending, key)
	}
	a.query = nil
	a.queries = nil
	if a.preview != nil {
		a.preview.nextInfo = time.Time{}
	}
	if a.tabs != nil {
		a.tabs.attachment = ""
		a.refreshTabs()
	}
	if !a.reconnect && a.tabs == nil {
		return err
	}
	if a.backoff == 0 {
		a.backoff = time.Second
	} else {
		a.backoff = min(15*time.Second, a.backoff*2)
	}
	if a.reconnect {
		a.retryAt = time.Now().Add(a.backoff)
	} else {
		a.retryAt = time.Time{}
	}
	a.note("Disconnected: " + err.Error() + " · Ctrl-] r reconnect · n new tab")
	return nil
}

func (a *app) retryNow() {
	if a.c != nil {
		a.c.close()
		a.c = nil
	}
	if a.dialCancel != nil {
		a.dialCancel()
	}
	a.generation++ // reject an in-flight dial for the previous view
	a.dialing = false
	a.hello = false
	a.heartbeatAt = time.Time{}
	a.framePending = false
	a.retryAt = time.Now()
	for key, p := range a.pending {
		p.cancel()
		delete(a.pending, key)
	}
	a.queries = nil
	a.query = nil
	clear(a.failed)
	if a.tabs != nil {
		a.tabs.attachment = ""
	}
}

func (a *app) run(parent context.Context, signals <-chan os.Signal) error {
	ctx, cancel := context.WithCancel(parent)
	a.ctx = ctx
	a.events = make(chan wireEvent, 16)
	a.dials = make(chan dialResult, 1)
	a.pending = make(map[string]pendingResource)
	a.resources = make(map[string]string)
	a.failed = make(map[string]bool)
	var tabResults <-chan serviceResult
	if a.tabs != nil {
		a.tabs.results = make(chan serviceResult, 8)
		tabResults = a.tabs.results
	}
	var err error
	a.cache, err = newCache(ctx)
	if err != nil {
		cancel()
		return err
	}
	input := make(chan readResult, 16)
	stopInput, err := readInput(ctx, a.fd, input)
	if err != nil {
		cancel()
		a.cache.close()
		return err
	}
	defer func() {
		cancel()
		if a.dialCancel != nil {
			a.dialCancel()
		}
		if a.c != nil {
			a.c.close()
		}
		a.dialWG.Wait()
		if a.tabs != nil {
			a.tabs.wg.Wait()
		}
		select {
		case result := <-a.dials:
			if result.c != nil {
				result.c.close()
			}
		default:
		}
		stopInput()
		a.cache.close()
	}()
	if err := a.resize(); err != nil {
		return err
	}
	if a.tabs == nil {
		a.dial()
	} else if a.tabs.startNew {
		a.tabs.preferred = ""
		a.newTab()
	} else {
		a.note("Loading service terminals…")
		a.refreshTabs()
	}
	var parser inputParser
	var inputErr error
	emit := func(event localEvent) {
		if inputErr == nil {
			inputErr = a.input(event)
		}
	}
	timer := time.NewTimer(time.Hour)
	timer.Stop()
	defer timer.Stop()
	for !a.quit {
		if inputErr != nil {
			return inputErr
		}
		if err := a.reconcile(); err != nil {
			return err
		}
		if a.dirty && !a.framePending {
			if err := a.paint(); err != nil {
				return err
			}
		}
		if a.statusDirty {
			if err := a.drawStatus(); err != nil {
				return err
			}
		}
		next := a.deadline(parser.deadline())
		var tick <-chan time.Time
		if !next.IsZero() {
			timer.Reset(max(time.Millisecond, time.Until(next)))
			tick = timer.C
		}
		select {
		case <-ctx.Done():
			return nil
		case sig := <-signals:
			if sig == syscall.SIGWINCH {
				if err := a.resize(); err != nil {
					return err
				}
			} else {
				return nil
			}
		case data := <-input:
			if len(data.data) > 0 {
				parser.feed(data.data, time.Now(), emit)
			}
			if data.err != nil {
				return data.err
			}
		case result := <-a.dials:
			if result.generation != a.generation {
				if result.c != nil {
					result.c.close()
				}
				break
			}
			a.dialing = false
			if result.err != nil {
				if err := a.disconnect(result.err); err != nil {
					return err
				}
				break
			}
			a.c = result.c
			a.remote.generation++
			for key, p := range a.pending {
				p.cancel()
				delete(a.pending, key)
			}
			a.screen.reset()
			a.preview = nil
			a.info = nil
			a.framePending = false
			clear(a.failed)
			clear(a.resources)
			if err := dcs(a.out, "region reset"); err != nil {
				return err
			}
			go a.c.read(ctx, a.events)
		case event := <-a.events:
			if event.connection != a.c {
				break
			}
			if event.err != nil {
				if err := a.disconnect(event.err); err != nil {
					return err
				}
				break
			}
			if err := a.message(event.message); err != nil {
				return err
			}
		case result := <-a.cache.results:
			if err := a.resourceReady(result); err != nil {
				return err
			}
		case result := <-tabResults:
			if err := a.serviceResult(result); err != nil {
				return err
			}
		case now := <-tick:
			parser.expire(now, emit)
			if err := a.tick(now); err != nil {
				return err
			}
		}
		timer.Stop()
	}
	return nil
}

func (a *app) resourceReady(result resourceResult) error {
	p, ok := a.pending[result.key]
	if !ok || p.id != result.id {
		return nil
	}
	p.cancel()
	delete(a.pending, result.key)
	if result.err != nil {
		a.failed[result.key] = true
		a.note("Preview: " + result.err.Error())
		return a.resourceFailure(result.key, result.err)
	}
	a.resources[result.key] = result.path
	a.dirty = true
	return nil
}

func (a *app) message(m serverMessage) error {
	switch m.Type {
	case "hello":
		if m.CellWidth < 1 || m.CellHeight < 1 || m.CellWidth > 1024 || m.CellHeight > 1024 || m.Border < 0 || m.Border > 1024 {
			return fmt.Errorf("invalid server font metrics")
		}
		a.screen.cw, a.screen.ch, a.screen.border = m.CellWidth, m.CellHeight, m.Border
		w, h := a.screen.dimensions()
		if w > 65535 || h > 65535 || int64(w)*int64(h) > 16<<20 {
			return fmt.Errorf("remote viewport exceeds pixel limit")
		}
		a.hello = true
		a.backoff = 0
		a.heartbeat = time.Duration(m.HeartbeatMS) * time.Millisecond
		if a.heartbeat < 250*time.Millisecond || a.heartbeat > 20*time.Second {
			a.heartbeat = 20 * time.Second
		}
		a.heartbeatAt = time.Now().Add(a.heartbeat)
		if a.tabs != nil {
			a.tabs.attachment = m.Attachment
			a.note("Connected · Ctrl-] n new · 1-9 tab · x close · q quit")
		} else {
			a.note("Connected · Ctrl-] q detach · c copy · r reconnect")
		}
		for _, msg := range []inputMessage{{Type: "resize", Width: w, Height: h}, {Type: "paint_ack", ID: 0}, {Type: "focus", Focused: true}} {
			if err := a.send(msg); err != nil {
				return err
			}
		}
	case "scene":
		if !a.hello {
			return fmt.Errorf("scene before hello")
		}
		if err := a.screen.apply(m); err != nil {
			return err
		}
		a.dirty = true
		a.framePending = true
	case "html":
		if !a.hello {
			return fmt.Errorf("preview before hello")
		}
		w, h := a.screen.dimensions()
		if m.Width != w || m.Height != h {
			return nil
		}
		if err := a.setPreview(m); err != nil {
			return err
		}
		a.framePending = true
	case "paint":
		a.framePending = false
		if err := a.paint(); err != nil {
			return err
		}
		return a.send(inputMessage{Type: "paint_ack", ID: m.ID})
	case "clipboard":
		if len(m.Text) <= 8<<20 {
			a.clipboard = m.Text
			a.note("Remote selection ready · Ctrl-] c copy")
		}
	case "clipboard_read":
		if !a.clipboardRead || m.Selection != "c" || a.clipboardID != 0 {
			return a.send(inputMessage{Type: "clipboard_reply", ID: m.ID})
		}
		a.clipboardID = m.ID
		a.clipboardUntil = time.Now().Add(2 * time.Second)
		return writeAll(a.out, []byte("\x1b]52;c;?\x1b\\"))
	case "document":
		return a.documents(m)
	case "open_pty":
		if m.OpenPTY == nil {
			return nil
		}
		if a.tabs != nil {
			if err := a.tabs.remote.validatePTY(*m.OpenPTY); err != nil {
				return err
			}
			a.tabs.revision++
			a.tabs.upsert(*m.OpenPTY)
			return a.selectTab(m.OpenPTY.ID)
		}
		if err := a.remote.selectView(*m.OpenPTY); err != nil {
			return err
		}
		a.retryNow()
	}
	return nil
}

func (a *app) setPreview(m serverMessage) error {
	if len(m.Overlays)+len(m.Media) > 1 {
		return fmt.Errorf("multiple simultaneous preview panes require more than the local terminal's one retained secondary region")
	}
	var next *previewState
	if len(m.Overlays) == 1 {
		o := m.Overlays[0]
		if len(o.Document) != 64 || strings.Trim(o.Document, "0123456789abcdef") != "" || !validRect(o.X, o.Y, o.Width, o.Height) {
			return fmt.Errorf("invalid HTML overlay")
		}
		r := a.screen.rect(o.X, o.Y, o.Width, o.Height)
		next = &previewState{rect: r, job: makeResource(a.remote, "html/"+o.Document, true, r.Dx(), r.Dy()), html: o.View}
	} else if len(m.Media) == 1 {
		o := m.Media[0]
		if o.View.ID == 0 || !validRect(o.X, o.Y, o.Width, o.Height) {
			return fmt.Errorf("invalid media overlay")
		}
		r := a.screen.rect(o.X, o.Y, o.Width, o.Height)
		next = &previewState{rect: r, job: makeResource(a.remote, "media/"+strconv.FormatUint(o.View.ID, 10), false, 0, 0), media: o.View}
	}
	if next != nil && next.rect.Empty() {
		next = nil
	}
	if old := a.preview; old != nil && next != nil && old.job.key == next.job.key && old.rect == next.rect {
		old.html, old.media = next.html, next.media
	} else {
		if a.query != nil && a.query.request.Query.Kind != "search" {
			if err := a.finishQuery(nil, fmt.Errorf("preview changed")); err != nil {
				return err
			}
		}
		a.preview = next
		if err := dcs(a.out, "region reset"); err != nil {
			return err
		}
		a.screen.previous = nil
	}
	a.dirty = true
	return nil
}

func (a *app) reconcile() error {
	if !a.hello {
		return nil
	}
	wanted := make(map[string]resourceJob)
	add := func(job resourceJob) { wanted[job.key] = job }
	if a.preview != nil {
		add(a.preview.job)
	}
	images := a.screen.images()
	if len(images) > 256 {
		return fmt.Errorf("too many inline media placements")
	}
	for _, im := range images {
		add(makeResource(a.remote, "media/"+strconv.FormatUint(im.media, 10), false, 0, 0))
	}
	for _, q := range a.queries {
		add(makeResource(a.remote, "media/"+strconv.FormatUint(q.Media, 10), false, 0, 0))
	}
	if a.query != nil {
		add(makeResource(a.remote, "media/"+strconv.FormatUint(a.query.request.Media, 10), false, 0, 0))
	}
	pins := make(map[string]bool)
	for key, job := range wanted {
		pins[key] = true
		pins[job.remote.resource(job.name)] = true
	}
	a.cache.pin(pins)
	for key, p := range a.pending {
		if !pins[key] {
			p.cancel()
			delete(a.pending, key)
		}
	}
	for key := range a.resources {
		if !pins[key] {
			delete(a.resources, key)
		}
	}
	for key := range a.failed {
		if !pins[key] {
			delete(a.failed, key)
		}
	}
	for key, job := range wanted {
		if _, ok := a.pending[key]; ok || a.resources[key] != "" || a.failed[key] {
			continue
		}
		ctx, cancel := context.WithCancel(a.ctx)
		a.resourceID++
		job.ctx, job.id = ctx, a.resourceID
		select {
		case a.cache.jobs <- job:
			a.pending[key] = pendingResource{job.id, cancel}
		default:
			cancel()
		}
	}
	return a.startQuery()
}

func (a *app) paint() error {
	if !a.hello {
		if a.tabs != nil && a.dirty {
			text, _ := tabText("Service tabs · Ctrl-] n new · 1-9 select · l refresh", a.screen.cols)
			if err := writeAll(a.out, []byte("\x1b[?25l\x1b[0m\x1b[2J\x1b[1;1H"+text)); err != nil {
				return err
			}
			a.tabs.lastBar = ""
			a.statusDirty = true
			a.dirty = false
		}
		return nil
	}
	if os.Getenv("ST_GO_BACKEND") == "fb" && time.Since(a.lastRefresh) > 500*time.Millisecond {
		if err := dcs(a.out, "refresh 33 1000"); err != nil {
			return err
		}
		a.lastRefresh = time.Now()
	}
	a.screen.imagePaths = make(map[uint64]string)
	for _, im := range a.screen.images() {
		key := a.mediaJob(im.media).key
		path := a.resources[key]
		if path == "" {
			continue
		}
		a.screen.imagePaths[im.media] = path
	}
	if a.tabs != nil && a.screen.force {
		a.tabs.lastBar = ""
	}
	if err := a.screen.draw(a.out); err != nil {
		return err
	}
	if err := a.presentPreview(); err != nil {
		return err
	}
	a.dirty = false
	a.statusDirty = true
	return nil
}

func (a *app) presentPreview() error {
	p := a.preview
	if p == nil {
		return nil
	}
	path := a.resources[p.job.key]
	if path == "" {
		return nil
	}
	first := p.path != path
	changed := first || p.html != p.appliedHTML || p.media != p.appliedMedia
	if first {
		r := p.rect
		if err := dcs(a.out, fmt.Sprintf("region overlay %d %d %d %d", r.Min.X+1, r.Min.Y+1, r.Dx(), r.Dy())); err != nil {
			return err
		}
		options := "fit-width continuous"
		if !strings.HasSuffix(path, ".pdf") {
			options = fitOption(p.media.Fit) + " viewer"
		}
		if err := dcs(a.out, "region secondary open "+quote(path)+" "+options); err != nil {
			return err
		}
		p.path = path
		p.ready = false
		p.nextInfo = time.Now()
	}
	command := func(kind string, value int) error {
		return dcs(a.out, fmt.Sprintf("region secondary %s %d", kind, value))
	}
	if p.job.html {
		if first || p.html.PositionID != p.appliedHTML.PositionID {
			if p.html.PositionID != 0 {
				if err := command("position", max(0, min(1000000, p.html.Position))); err != nil {
					return err
				}
			}
			p.appliedHTML.Scroll = 0
		}
		if delta := p.html.Scroll - p.appliedHTML.Scroll; delta != 0 {
			if err := command("scroll", delta); err != nil {
				return err
			}
		}
		p.appliedHTML = p.html
	} else {
		v, old := p.media, p.appliedMedia
		if first || v.Zoom != old.Zoom {
			if v.Zoom > 0 {
				zoom := min(6400, v.Zoom)
				if strings.HasSuffix(path, ".pdf") {
					zoom = max(10, min(500, zoom))
				}
				if err := command("zoom", zoom); err != nil {
					return err
				}
			}
		}
		if first || v.PositionID != old.PositionID {
			if strings.HasSuffix(path, ".pdf") && v.Position == 0 && v.Page > 0 {
				if err := command("page", v.Page); err != nil {
					return err
				}
			} else if v.PositionID != 0 {
				if err := command("position", max(0, min(1000000, v.Position))); err != nil {
					return err
				}
			}
			old.Scroll = 0
		}
		if delta := v.Scroll - old.Scroll; delta != 0 {
			if err := command("scroll", delta); err != nil {
				return err
			}
		}
		if v.Search != old.Search {
			cmd := "region secondary find clear"
			if v.Search != "" {
				cmd = "region secondary find forward " + quote(strings.Map(func(r rune) rune {
					if r < 32 {
						return ' '
					}
					return r
				}, v.Search))
			}
			if err := dcs(a.out, cmd); err != nil {
				return err
			}
		}
		p.appliedMedia = v
	}
	if changed {
		p.ready = false
		p.nextInfo = time.Now()
	}
	return nil
}

func (a *app) mediaJob(id uint64) resourceJob {
	return makeResource(a.remote, "media/"+strconv.FormatUint(id, 10), false, 0, 0)
}

func (a *app) drawStatus() error {
	row := a.screen.rows + 1
	if a.tabs != nil {
		if err := a.drawTabs(); err != nil {
			return err
		}
		row++
	}
	text := []rune(cleanText(a.status))
	if len(text) > a.screen.cols {
		text = text[:a.screen.cols]
	}
	err := writeAll(a.out, []byte(fmt.Sprintf("\x1b7\x1b[%d;1H\x1b[0;37;44m\x1b[2K%s\x1b[0m\x1b8", row, string(text))))
	a.statusDirty = false
	return err
}

func (a *app) input(e localEvent) error {
	if e.reply != "" {
		return a.localReply(e.reply)
	}
	if a.prefix {
		a.prefix = false
		if e.escape {
			return a.send(inputMessage{Type: "key", Key: "]", Modifiers: 4})
		}
		key := e.input.Key
		if key == "Tab" && e.input.Modifiers&1 != 0 {
			key = "p"
		}
		if handled, err := a.tabCommand(key); handled {
			return err
		}
		switch e.input.Key {
		case "q":
			a.quit = true
			return nil
		case "r":
			a.retryNow()
			return nil
		case "c":
			return writeAll(a.out, []byte("\x1b]52;c;"+base64.StdEncoding.EncodeToString([]byte(a.clipboard))+"\x1b\\"))
		}
		a.note("Ctrl-] q detach · c copy · r reconnect · Ctrl-] send prefix")
		return nil
	}
	if e.escape {
		a.prefix = true
		if a.tabs != nil {
			a.note("Tab: n new · 1-9 select · Tab/p next/prev · x close · l refresh · q quit")
		} else {
			a.note("Client command: q detach · c copy · r reconnect · Ctrl-] literal")
		}
		return nil
	}
	if handled, err := a.tabInput(e.input); handled {
		return err
	}
	if !a.hello {
		return nil
	}
	m := e.input
	if m.Type == "press" || m.Type == "release" || m.Type == "motion" {
		if m.Y > a.screen.rows || m.X > a.screen.cols {
			return nil
		}
		m.X, m.Y = a.screen.point(m.X, m.Y)
	}
	return a.send(m)
}

func (a *app) deadline(initial time.Time) time.Time {
	next := initial
	add := func(t time.Time) {
		if !t.IsZero() && (next.IsZero() || t.Before(next)) {
			next = t
		}
	}
	add(a.retryAt)
	add(a.heartbeatAt)
	if a.tabs != nil {
		add(a.tabs.refreshAt)
	}
	if a.preview != nil && a.hello {
		add(a.preview.nextInfo)
	}
	if a.query != nil {
		add(a.query.deadline)
	}
	for _, q := range a.queries {
		add(q.deadline)
	}
	if a.clipboardID != 0 {
		add(a.clipboardUntil)
	}
	return next
}

func (a *app) tick(now time.Time) error {
	if a.tabs != nil && !a.tabs.refreshAt.IsZero() && !now.Before(a.tabs.refreshAt) {
		a.refreshTabs()
	}
	live := a.queries[:0]
	for _, q := range a.queries {
		if !q.deadline.IsZero() && !now.Before(q.deadline) {
			if err := a.send(inputMessage{Type: "document_reply", ID: q.ID, Data: errorJSON(fmt.Errorf("document did not become available"))}); err != nil {
				return err
			}
		} else {
			live = append(live, q)
		}
	}
	a.queries = live
	if !a.retryAt.IsZero() && !now.Before(a.retryAt) {
		a.dial()
	}
	if !a.heartbeatAt.IsZero() && !now.Before(a.heartbeatAt) {
		if err := a.send(inputMessage{Type: "ping"}); err != nil {
			return a.disconnect(err)
		}
		if a.hello {
			a.heartbeatAt = now.Add(a.heartbeat)
		}
	}
	if p := a.preview; p != nil && !p.nextInfo.IsZero() && !now.Before(p.nextInfo) {
		if !a.hello {
			p.nextInfo = time.Time{}
			return nil
		}
		if a.info != nil && now.Before(a.info.until) {
			p.nextInfo = a.info.until
			return nil
		}
		if err := dcs(a.out, "region secondary info"); err != nil {
			return err
		}
		a.info = &previewInfo{preview: p, view: p.media, until: now.Add(3 * time.Second)}
		p.nextInfo = now.Add(time.Second)
	}
	if a.query != nil && !now.Before(a.query.deadline) {
		return a.finishQuery(nil, fmt.Errorf("local PDF query timed out"))
	}
	if a.clipboardID != 0 && !now.Before(a.clipboardUntil) {
		id := a.clipboardID
		a.clipboardID = 0
		return a.send(inputMessage{Type: "clipboard_reply", ID: id})
	}
	return nil
}

func (a *app) localReply(text string) error {
	if strings.HasPrefix(text, "52;") && a.clipboardID != 0 {
		parts := strings.SplitN(text, ";", 3)
		if len(parts) != 3 {
			return nil
		}
		data, err := base64.StdEncoding.DecodeString(parts[2])
		if len(data) > 8<<20 {
			err = fmt.Errorf("clipboard too large")
		}
		id := a.clipboardID
		a.clipboardID = 0
		return a.send(inputMessage{Type: "clipboard_reply", ID: id, Text: string(data), OK: err == nil})
	}
	if strings.HasPrefix(text, "pdfsearch data ") || strings.HasPrefix(text, "region secondary data ") {
		return a.queryReply(text)
	}
	p := a.preview
	if p == nil || p.path == "" {
		return nil
	}
	f := strings.Fields(text)
	if len(f) != 9 || strings.Join(f[:3], " ") != "region secondary status" {
		return nil
	}
	info := a.info
	a.info = nil
	if info == nil || info.preview != p || info.view != p.media {
		return nil
	}
	var n [6]int
	for i := range n {
		value, err := strconv.Atoi(f[i+3])
		if err != nil {
			return nil
		}
		n[i] = value
	}
	if n[0] < 0 || n[1] < 0 || n[1] > 100000 || n[2] < 0 || n[2] > 1000000 {
		return nil
	}
	p.ready = n[4] == 0 && n[5] == 0
	if p.job.html && p.ready || n[5] != 0 {
		p.nextInfo = time.Time{}
	}
	if n[5] != 0 {
		a.note("Local preview renderer failed; full st-go PDF support is required")
	}
	if p.media.ID != 0 {
		status := api.MediaStatus{ID: p.media.ID, Generation: p.media.Generation, PositionID: p.media.PositionID, Page: n[0], Pages: n[1], Position: n[2]}
		if n[5] != 0 {
			status.Error = "local preview rendering failed"
		}
		if (p.ready || n[5] != 0) && status != p.lastStatus {
			p.lastStatus = status
			return a.send(inputMessage{Type: "media_status", MediaStatus: &status})
		}
	}
	return nil
}

func errorJSON(err error) json.RawMessage {
	data, _ := json.Marshal(map[string]string{"error": err.Error()})
	return data
}
