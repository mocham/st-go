package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"

	term "st-go/platform_neutral/stterminal"
)

const serviceRefreshInterval = 10 * time.Second

// The service outlives its selected PTY. Only the selected tab holds a socket;
// inactive PTYs keep running on the server without client rendering work.
type serviceTabs struct {
	remote                *remote
	entries               []ptyInfo
	selected, preferred   string
	startNew              bool
	directory, title      string
	attachment            string
	released              map[string]time.Time
	selection, revision   uint64
	listing, refreshAgain bool
	busy, closing         string
	refreshAt             time.Time
	backoff               time.Duration
	results               chan serviceResult
	wg                    sync.WaitGroup
	hits                  []tabHit
	first                 int
	lastBar               string
}

type serviceResult struct {
	kind, target        string
	revision, selection uint64
	entries             []ptyInfo
	pty                 ptyInfo
	err                 error
}

type tabHit struct {
	left, right int
	id, action  string
}

func (t *serviceTabs) index(id string) int {
	for i, p := range t.entries {
		if p.ID == id {
			return i
		}
	}
	return -1
}

func (t *serviceTabs) upsert(p ptyInfo) {
	if i := t.index(p.ID); i >= 0 {
		t.entries[i] = p
	} else {
		t.entries = append(t.entries, p)
	}
}

func (t *serviceTabs) available(p ptyInfo) bool {
	// A list can race server-side cleanup of a socket we just released. Allow
	// a quick switch back; the server still arbitrates the actual attachment.
	return !p.Occupied || time.Now().Before(t.released[p.ID])
}

func (a *app) startServiceJob(result serviceResult, work func(context.Context, *serviceResult)) {
	t := a.tabs
	t.wg.Add(1)
	go func() {
		defer t.wg.Done()
		ctx, cancel := context.WithTimeout(a.ctx, 10*time.Second)
		defer cancel()
		work(ctx, &result)
		select {
		case t.results <- result:
		case <-a.ctx.Done():
		}
	}()
}

func (a *app) refreshTabs() {
	t := a.tabs
	if t == nil {
		return
	}
	t.refreshAt = time.Time{}
	if t.listing || t.busy != "" {
		t.refreshAgain = true
		return
	}
	t.listing = true
	r := *t.remote
	a.startServiceJob(serviceResult{kind: "list", revision: t.revision}, func(ctx context.Context, result *serviceResult) {
		result.entries, result.err = r.sessions(ctx)
		if result.err != nil {
			return
		}
		if len(result.entries) > 256 {
			result.err = fmt.Errorf("too many service terminals")
			return
		}
		seen := make(map[string]bool)
		for _, p := range result.entries {
			if result.err = r.validatePTY(p); result.err != nil {
				return
			}
			if seen[p.ID] {
				result.err = fmt.Errorf("duplicate service terminal")
				return
			}
			seen[p.ID] = true
		}
	})
}

func (a *app) newTab() {
	t := a.tabs
	if t.busy != "" {
		a.note("A tab operation is already pending")
		return
	}
	t.busy = "create"
	t.revision++
	a.note("Creating terminal…")
	r := *t.remote
	body := map[string]string{"directory": t.directory, "title": t.title}
	a.startServiceJob(serviceResult{kind: "create", selection: t.selection}, func(ctx context.Context, result *serviceResult) {
		var data []byte
		data, result.err = r.request(ctx, "POST", r.resource("api/ptys"), body, 1<<20)
		if result.err == nil {
			result.err = json.Unmarshal(data, &result.pty)
		}
		if result.err == nil {
			result.err = r.validatePTY(result.pty)
		}
	})
}

func (a *app) closeTab() {
	t := a.tabs
	if t.busy != "" {
		a.note("A tab operation is already pending")
		return
	}
	if t.selected == "" {
		a.note("No terminal selected · Ctrl-] n creates one")
		return
	}
	if !a.hello || t.attachment == "" {
		a.note("Reconnect to the selected terminal before closing it")
		return
	}
	t.busy, t.closing = "close", t.selected
	t.revision++
	a.note("Closing terminal…")
	r, id, attachment := *t.remote, t.selected, t.attachment
	a.startServiceJob(serviceResult{kind: "close", target: id}, func(ctx context.Context, result *serviceResult) {
		_, result.err = r.requestAttached(ctx, "DELETE", r.resource("api/ptys/"+id), nil, 1<<20, attachment)
		var response *httpError
		if errors.As(result.err, &response) && response.code == http.StatusNotFound {
			result.err = nil
		}
	})
}

func (a *app) serviceResult(result serviceResult) error {
	t := a.tabs
	a.statusDirty = true
	if result.kind == "list" {
		t.listing = false
		if result.revision != t.revision {
			t.refreshAgain = true
		} else if result.err != nil {
			if t.backoff == 0 {
				t.backoff = time.Second
			} else {
				t.backoff = min(30*time.Second, t.backoff*2)
			}
			t.refreshAt = time.Now().Add(t.backoff)
			a.note("Service unavailable: " + result.err.Error() + " · Ctrl-] l refresh")
		} else {
			t.backoff = 0
			t.refreshAt = time.Now().Add(serviceRefreshInterval)
			oldIndex := t.index(t.selected)
			t.entries = result.entries
			for id, until := range t.released {
				if time.Now().After(until) || t.index(id) < 0 {
					delete(t.released, id)
				}
			}
			if t.selected != "" && t.index(t.selected) < 0 {
				if err := a.removeSelectedTab(); err != nil {
					return err
				}
				a.note("Terminal closed")
			}
			if t.preferred != "" {
				id := t.preferred
				t.preferred = ""
				if t.index(id) >= 0 {
					if err := a.selectTab(id); err != nil {
						return err
					}
				}
			}
			if t.selected == "" {
				if err := a.availableTab(max(0, oldIndex)); err != nil {
					return err
				}
			}
			if t.selected != "" && !a.hello && !a.dialing && a.retryAt.IsZero() && a.reconnect {
				a.retryAt = time.Now()
			}
		}
	} else {
		t.busy, t.closing = "", ""
		t.revision++
		if result.err != nil {
			// Mutations are never automatically replayed: the response may have
			// been lost after the server already created/closed the terminal.
			a.note("Tab " + result.kind + ": " + result.err.Error() + " · Ctrl-] l refresh")
		} else if result.kind == "create" {
			t.upsert(result.pty)
			if result.selection == t.selection {
				if err := a.selectTab(result.pty.ID); err != nil {
					return err
				}
			}
		} else if result.kind == "close" {
			index := t.index(result.target)
			if index >= 0 {
				t.entries = append(t.entries[:index], t.entries[index+1:]...)
			}
			if t.selected == result.target {
				if err := a.removeSelectedTab(); err != nil {
					return err
				}
				if err := a.availableTab(max(0, index)); err != nil {
					return err
				}
			}
		}
		t.refreshAgain = true
	}
	if t.refreshAgain && t.busy == "" {
		t.refreshAgain = false
		a.refreshTabs()
	}
	return nil
}

func (a *app) removeSelectedTab() error {
	a.retryNow()
	a.retryAt = time.Time{}
	a.tabs.selected = ""
	a.tabs.selection++
	return a.clearTabPresentation()
}

func (a *app) clearTabPresentation() error {
	a.preview = nil
	a.info = nil
	a.clipboard = ""
	a.clipboardID = 0
	clear(a.resources)
	clear(a.failed)
	if a.cache != nil {
		a.cache.pin(map[string]bool{})
	}
	a.screen.reset()
	a.dirty = true
	a.tabs.attachment = ""
	a.tabs.lastBar = ""
	a.statusDirty = true
	return dcs(a.out, "pdfsearch clear; region reset")
}

func (a *app) selectTab(id string) error {
	t := a.tabs
	i := t.index(id)
	if i < 0 {
		a.note("Terminal is no longer available · Ctrl-] l refresh")
		a.refreshTabs()
		return nil
	}
	p := t.entries[i]
	if !t.available(p) && id != t.selected {
		a.note("Terminal is occupied · Ctrl-] l refresh")
		a.refreshTabs()
		return nil
	}
	t.selection++
	if t.selected == id && (a.hello || a.dialing) {
		return nil
	}
	r := *t.remote
	r.generation = a.remote.generation
	if err := r.selectView(p); err != nil {
		return err
	}
	if old := t.index(t.selected); old >= 0 && a.c != nil {
		t.entries[old].Occupied = false
		if t.released == nil {
			t.released = make(map[string]time.Time)
		}
		t.released[t.selected] = time.Now().Add(2 * time.Second)
	}
	a.retryNow()
	t.selected = id
	a.remote = &r
	if err := a.clearTabPresentation(); err != nil {
		return err
	}
	a.note("Connecting to " + cleanText(p.Title) + " · Ctrl-] n new · x close")
	return nil
}

func (a *app) availableTab(start int) error {
	t := a.tabs
	for n := 0; n < len(t.entries); n++ {
		p := t.entries[(start+n)%len(t.entries)]
		if t.available(p) {
			return a.selectTab(p.ID)
		}
	}
	a.note("No available terminal · Ctrl-] n new · l refresh · q quit")
	return nil
}

func (a *app) cycleTab(delta int) error {
	t := a.tabs
	if len(t.entries) == 0 {
		a.note("No terminals · Ctrl-] n creates one")
		return nil
	}
	index := t.index(t.selected)
	if index < 0 {
		index = 0
		if delta > 0 {
			index = -1
		}
	}
	for n := 0; n < len(t.entries); n++ {
		index = (index + delta + len(t.entries)) % len(t.entries)
		p := t.entries[index]
		if t.available(p) || p.ID == t.selected {
			return a.selectTab(p.ID)
		}
	}
	return nil
}

func (a *app) tabCommand(key string) (bool, error) {
	if a.tabs == nil {
		return false, nil
	}
	switch key {
	case "n":
		a.newTab()
	case "x":
		a.closeTab()
	case "l":
		a.refreshTabs()
		a.note("Refreshing terminals…")
	case "r":
		a.refreshTabs()
		if a.tabs.selected != "" {
			a.retryNow()
		}
	case "Tab", "]":
		return true, a.cycleTab(1)
	case "p", "[":
		return true, a.cycleTab(-1)
	default:
		if len(key) != 1 || key[0] < '1' || key[0] > '9' {
			return false, nil
		}
		index := int(key[0] - '1')
		if index < len(a.tabs.entries) {
			return true, a.selectTab(a.tabs.entries[index].ID)
		}
		if index == len(a.tabs.entries) {
			a.newTab()
		} else {
			a.note("No tab " + key + " · Ctrl-] n new")
		}
	}
	return true, nil
}

func (a *app) tabInput(m inputMessage) (bool, error) {
	if a.tabs == nil {
		return false, nil
	}
	if m.Type == "key" {
		if m.Modifiers == 8 && len(m.Key) == 1 && m.Key[0] >= '1' && m.Key[0] <= '9' {
			return a.tabCommand(m.Key)
		}
		if m.Modifiers == 4 && m.Key == "PageDown" {
			return true, a.cycleTab(1)
		}
		if m.Modifiers == 4 && m.Key == "PageUp" {
			return true, a.cycleTab(-1)
		}
	}
	if (m.Type == "press" || m.Type == "release" || m.Type == "motion") && m.Y > a.screen.rows {
		if m.Y == a.screen.rows+1 && m.Type == "press" {
			if m.Button == 4 {
				return true, a.cycleTab(-1)
			}
			if m.Button == 5 {
				return true, a.cycleTab(1)
			}
			if m.Button == 1 {
				for _, hit := range a.tabs.hits {
					if m.X >= hit.left && m.X < hit.right {
						if hit.id != "" {
							return true, a.selectTab(hit.id)
						}
						return a.tabCommand(hit.action)
					}
				}
			}
		}
		return true, nil
	}
	return false, nil
}

// Limit by terminal cells, not bytes/runes: a wide title must not move the
// click targets or overwrite the close button. Combining suffixes stay intact.
func tabText(text string, limit int) (string, int) {
	var b strings.Builder
	cells := 0
	for _, r := range text {
		if unicode.IsControl(r) {
			r = ' '
		}
		w := term.CellWidth(r)
		if w < 0 {
			r = '?'
			w = 1
		}
		if w == 0 && cells == 0 {
			continue
		}
		if cells+w > limit {
			break
		}
		b.WriteRune(r)
		cells += w
	}
	return b.String(), cells
}

func (a *app) drawTabs() error {
	t := a.tabs
	cols := a.screen.cols
	t.hits = nil
	var b strings.Builder
	b.WriteString("\x1b[0;37;40m\x1b[2K[+]")
	t.hits = append(t.hits, tabHit{left: 1, right: 4, action: "n"})
	// Keep the selected tab visible, including on narrow terminals.
	active := t.index(t.selected)
	labelFor := func(i int) string {
		p := t.entries[i]
		name := p.Title
		if name == "" {
			name = p.ID
		}
		if !t.available(p) && p.ID != t.selected {
			name = "!" + name
		}
		return strconv.Itoa(i+1) + ":" + name
	}
	if active >= 0 {
		t.first = min(t.first, active)
		for t.first < active {
			needed := 0
			for i := t.first; i <= active; i++ {
				_, width := tabText(labelFor(i), 20)
				needed += width
				if i < active {
					needed++
				}
			}
			if needed <= cols-6 {
				break
			}
			t.first++
		}
	}
	t.first = max(0, min(t.first, max(0, len(t.entries)-1)))
	x, end := 4, cols-2
	for i := t.first; i < len(t.entries) && x < end; i++ {
		p := t.entries[i]
		label, width := tabText(labelFor(i), min(20, end-x))
		if p.ID == t.selected {
			b.WriteString("\x1b[0;30;46m")
		} else {
			b.WriteString("\x1b[0;37;40m")
		}
		b.WriteString(label)
		t.hits = append(t.hits, tabHit{left: x, right: x + width, id: p.ID})
		x += width
		if x < end {
			b.WriteByte(' ')
			x++
		}
	}
	if cols >= 7 {
		fmt.Fprintf(&b, "\x1b[%d;%dH\x1b[0;37;40m[x]", a.screen.rows+1, cols-2)
		t.hits = append(t.hits, tabHit{left: cols - 2, right: cols + 1, action: "x"})
	}
	bar := b.String()
	if bar == t.lastBar {
		return nil
	}
	t.lastBar = bar
	return writeAll(a.out, []byte(fmt.Sprintf("\x1b7\x1b[%d;1H%s\x1b[0m\x1b8", a.screen.rows+1, bar)))
}
