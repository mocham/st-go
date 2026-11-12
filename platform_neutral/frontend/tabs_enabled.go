//go:build terminal_tabs

package frontend

import (
	"crypto/sha256"
	"fmt"
	"image"
	"os"
	"path/filepath"
	"strings"
	"unicode"

	"st-go/limits"
	"st-go/platform_neutral/api"
	term "st-go/platform_neutral/stterminal"
)

const tabsCompiled = true
const maxTerminalTabs = 8
const terminalTabWidthCells = 20
const tabRasterBudget = 256 << 20 // aggregate working + committed terminal rasters

type terminalTab struct {
	terminal *Terminal
	widget   api.Widget
	title    string
}
type tabResult struct {
	tab api.WidgetID
	err error
}
type tabHit struct {
	left, right int
	index       int
}

// All policy state is serialized by WidgetWindow's native event domain. PTY
// parsing only posts work; no policy callback runs inside a terminal lock.
type terminalTabs struct {
	control     api.WidgetWindow
	container   api.Widget
	entries     []terminalTab
	active      int
	bar         api.Widget
	renderer    Terminal
	hits        []tabHit
	results     map[string]tabResult
	resultOrder []string
}

func configureTerminalTabs(t *Terminal, allowed bool) {
	if !allowed || t.Widget() == nil {
		return
	}
	control, ok := t.Widget().(api.WidgetWindow)
	if !ok {
		return
	}
	token := newGeometryToken()
	if token == "" {
		return
	}
	m := &terminalTabs{control: control, container: t.Widget(), entries: []terminalTab{{t, t.Widget(), t.title}}, results: make(map[string]tabResult),
		renderer: Terminal{cw: t.cw, ch: t.ch, baseline: t.baseline}}
	t.cfg.TabSpawnToken = token
	t.tabs = m
	control.BindWindowHooks(api.WindowHooks{Resize: m.resize, KeyPress: m.keyPress})
	if host, ok := t.host.(api.TerminalTabButtonHost); ok {
		host.BindNewTerminalTab(m.newTerminalTab)
	}
}

func (m *terminalTabs) newTerminalTab() {
	m.control.PostWindow(func() {
		if m.active < 0 || m.active >= len(m.entries) {
			return
		}
		origin := m.entries[m.active].terminal
		if origin.closed() {
			return
		}
		// Use normal shell selection rather than repeating the original -e
		// command. createTab preserves the original PTY and lazily adds the bar.
		program, _, err := ResolveShell(origin.cfg, "")
		if err == nil {
			var directory string
			directory, err = os.Getwd()
			if err == nil {
				err = m.createTab(origin, api.TabSpawnRequest{Command: []string{program}, Directory: directory}).err
			}
		}
		if err != nil {
			limits.Report("tabs", "new terminal: %v", err)
		}
	})
}

func (m *terminalTabs) run(root *Terminal) { go root.run(); m.control.RunWindow() }

func (m *terminalTabs) spawn(origin *Terminal, id string, request api.TabSpawnRequest) bool {
	request.Command = append([]string(nil), request.Command...)
	return m.control.PostWindow(func() { m.spawnNow(origin, id, request) })
}

func (m *terminalTabs) reply(origin *Terminal, id string, result tabResult) {
	origin.mu.Lock()
	defer origin.mu.Unlock()
	origin.tabReply(id, result.tab, result.err)
}

func (m *terminalTabs) spawnNow(origin *Terminal, id string, request api.TabSpawnRequest) {
	if origin.closed() {
		return
	}
	key := fmt.Sprintf("%d:%s", origin.Widget().ID(), id)
	if result, ok := m.results[key]; ok {
		m.reply(origin, id, result)
		return
	}
	result := m.createTab(origin, request)
	if len(m.resultOrder) == 256 {
		delete(m.results, m.resultOrder[0])
		m.resultOrder = m.resultOrder[1:]
	}
	m.results[key] = result
	m.resultOrder = append(m.resultOrder, key)
	m.reply(origin, id, result)
}

func (m *terminalTabs) createTab(origin *Terminal, request api.TabSpawnRequest) tabResult {
	fail := func(err error) tabResult { return tabResult{err: err} }
	if err := request.Validate(); err != nil {
		return fail(err)
	}
	found := false
	for _, entry := range m.entries {
		if entry.terminal == origin {
			found = true
			break
		}
	}
	if !found {
		return fail(fmt.Errorf("requesting terminal has closed"))
	}
	if request.Probe {
		return tabResult{}
	}
	bounds := m.control.WindowBounds()
	if len(m.entries) >= maxTerminalTabs || int64(bounds.Dx())*int64(bounds.Dy())*8*int64(len(m.entries)+1) > tabRasterBudget {
		return fail(fmt.Errorf("terminal tab resource limit reached"))
	}
	content, barBounds := m.rectangles(bounds.Dx(), bounds.Dy())
	if barBounds.Empty() {
		return fail(fmt.Errorf("window too small for a tab bar"))
	}
	info, err := os.Stat(request.Directory)
	if err != nil || !info.IsDir() {
		return fail(fmt.Errorf("tab directory is unavailable"))
	}
	// Build the bar hidden. Failed exec must not initialize tabs or resize the
	// original PTY, and must never display a provisional blank surface.
	bar := m.bar
	if bar == nil {
		bar, err = m.container.NewWidget(api.WidgetOptions{Kind: api.BarWidget, Bounds: barBounds, Hidden: true})
		if err != nil {
			return fail(err)
		}
	}
	committed := false
	defer func() {
		if !committed && m.bar == nil {
			bar.Close()
		}
	}()
	widget, err := m.container.NewWidget(api.WidgetOptions{Kind: api.TerminalWidget, Bounds: content, Hidden: true})
	if err != nil {
		return fail(err)
	}
	cfg := *origin.cfg
	cfg.GeometryToken = newGeometryToken()
	cfg.NativeAudioToken = newGeometryToken()
	cfg.TabSpawnToken = newGeometryToken()
	if cfg.ClipboardSyncToken != "" {
		cfg.ClipboardSyncToken = newGeometryToken()
	}
	if cfg.TabSpawnToken == "" {
		widget.Close()
		return fail(fmt.Errorf("tab capability generation failed"))
	}
	if cfg.NativeVideoToken != "" {
		cfg.NativeVideoToken = newGeometryToken()
	}
	cfg.Cols = uint(max(1, (content.Dx()-2*origin.borderpx)/origin.cw))
	cfg.Rows = uint(max(1, (content.Dy()-2*origin.borderpx)/origin.ch))
	child, err := NewTerminalWidget(&cfg, widget, origin.ratio)
	if err != nil {
		widget.Close()
		return fail(err)
	}
	child.tabs = m
	child.appBroker = origin.appBroker
	title := request.Title
	if title == "" {
		title = filepath.Base(request.Command[0])
	}
	child.mu.Lock()
	child.setTitle(title)
	child.mu.Unlock()
	command := append([]string(nil), request.Command...)
	var temporary string
	if request.CopyFile != nil {
		data, copyErr := limits.ReadFile(command[*request.CopyFile])
		if copyErr != nil {
			child.Close()
			return fail(copyErr)
		}
		temporary, err = os.MkdirTemp("", "st-go-tab-file-")
		if err == nil {
			path := filepath.Join(temporary, filepath.Base(command[*request.CopyFile]))
			err = os.WriteFile(path, data, 0600)
			command[*request.CopyFile] = path
		}
		if err != nil {
			if temporary != "" {
				_ = os.RemoveAll(temporary)
			}
			child.Close()
			return fail(err)
		}
		owned := temporary
		ownedFile := command[*request.CopyFile]
		initialHash := sha256.Sum256(data)
		child.sessionCleanup = func(waitErr error) {
			if waitErr != nil {
				// An editor's recovery journal can refer to this path. Do not
				// destroy its backing file after a crash or forced tab close.
				limits.Report("tab-file", "retained %q after abnormal child exit: %v", owned, waitErr)
				return
			}
			current, readErr := limits.ReadFile(ownedFile)
			entries, dirErr := os.ReadDir(owned)
			if readErr != nil || dirErr != nil || len(entries) != 1 || sha256.Sum256(current) != initialHash {
				limits.Report("tab-file", "retained edited or incomplete temporary copy %q", owned)
				return
			}
			_ = os.RemoveAll(owned)
		}
	}
	if err := child.StartSession(SessionOptions{Command: command, Directory: request.Directory}); err != nil {
		if temporary != "" {
			_ = os.RemoveAll(temporary)
		}
		child.Close()
		return fail(err)
	}
	m.entries = append(m.entries, terminalTab{child, widget, title})
	if m.bar == nil {
		m.bar = bar
		bar.Bind(api.Callbacks{PointerPress: func(button byte, _ uint16, x, _ int) { m.click(button, x) }})
		// The old full-client terminal becomes tab 1; no PTY is replaced.
		m.entries[0].widget.(api.WidgetWindow).SetAutoResize(false)
	}
	committed = true
	m.resize(bounds.Dx(), bounds.Dy())
	if !request.Detached {
		m.selectTab(len(m.entries) - 1)
	}
	go child.run()
	return tabResult{tab: widget.ID()}
}

func (m *terminalTabs) rectangles(width, height int) (image.Rectangle, image.Rectangle) {
	barHeight, barWidth := m.renderer.ch+4, width
	passiveHeight := 0
	if layout, ok := m.control.(api.WindowBarLayout); ok {
		for _, passive := range layout.PassiveBarBounds() {
			if passive.Max == image.Pt(width, height) {
				passiveHeight = max(passiveHeight, passive.Dy())
				barHeight = max(barHeight, passive.Dy())
				barWidth = min(barWidth, max(0, passive.Min.X))
			}
		}
	}
	// A framebuffer system bar can fill a narrow screen. Keep at least one
	// usable tab label by giving tabs their own band above the native controls.
	if passiveHeight > 0 && barWidth < terminalTabWidthCells*max(1, m.renderer.cw) {
		bottom := max(0, height-passiveHeight)
		barHeight = min(m.renderer.ch+4, max(0, bottom-1))
		return image.Rect(0, 0, width, bottom-barHeight), image.Rect(0, bottom-barHeight, width, bottom)
	}
	barHeight = min(barHeight, max(0, height-1))
	return image.Rect(0, 0, width, height-barHeight), image.Rect(0, height-barHeight, barWidth, height)
}

func (m *terminalTabs) resize(width, height int) {
	if m.bar == nil {
		return
	}
	content, bar := m.rectangles(width, height)
	if width < 1 || height < 1 {
		return
	}
	if batch, ok := m.container.(api.PaintBatcher); ok {
		batch.BeginPaint()
		defer batch.EndPaint()
	}
	// Hidden PTYs receive their deferred geometry on activation. A background
	// image decode must not stall the native input loop just because the window
	// resized, and inactive widgets do not receive native event callbacks.
	if m.active < len(m.entries) && !m.entries[m.active].terminal.closed() {
		m.entries[m.active].widget.SetBounds(content)
	}
	if bar.Empty() {
		m.bar.SetVisible(false)
		m.hits = nil
		return
	}
	m.bar.SetBounds(bar)
	m.bar.SetVisible(true)
	m.paintBar()
}

func (m *terminalTabs) selectTab(index int) {
	if index < 0 || index >= len(m.entries) || m.entries[index].terminal.closed() {
		return
	}
	if batch, ok := m.container.(api.PaintBatcher); ok {
		batch.BeginPaint()
		defer batch.EndPaint()
	}
	bounds := m.control.WindowBounds()
	content, _ := m.rectangles(bounds.Dx(), bounds.Dy())
	m.entries[index].widget.SetBounds(content)
	if m.active >= 0 && m.active < len(m.entries) && m.active != index {
		m.entries[m.active].widget.SetVisible(false)
	}
	m.active = index
	m.entries[index].widget.SetVisible(true)
	m.entries[index].widget.FocusWidget()
	m.paintBar()
}

func (m *terminalTabs) keyPress(key uint, mods uint16) bool {
	if mods == Mod1Mask && key >= '1' && key <= '9' {
		if identity, ok := m.container.(api.BackendIdentity); ok && identity.BackendName() == "fb" {
			index := int(key - '1')
			if index >= len(m.entries) {
				m.newTerminalTab()
			} else if m.bar != nil {
				m.selectTab(index)
			} else {
				// Keep the single-PTY layout unchanged until the tab bar exists.
				m.entries[index].widget.FocusWidget()
			}
			return true
		}
	}
	if m.bar == nil || mods&5 != 5 || mods&8 != 0 {
		return false
	} // Ctrl+Shift, never Alt
	switch key {
	case XKPrior:
		m.selectTab((m.active + len(m.entries) - 1) % len(m.entries))
	case XKNext:
		m.selectTab((m.active + 1) % len(m.entries))
	case 'w', 'W':
		m.entries[m.active].terminal.closeSession()
	default:
		return false
	}
	return true
}

func (m *terminalTabs) click(button byte, x int) {
	if len(m.entries) == 0 {
		return
	}
	// Fixed-width tabs leave unused bar space; wheel navigation still works
	// over that space, but clicks must never select/close an invisible label.
	switch button {
	case 4:
		m.selectTab((m.active + len(m.entries) - 1) % len(m.entries))
		return
	case 5:
		m.selectTab((m.active + 1) % len(m.entries))
		return
	}
	for _, hit := range m.hits {
		if x < hit.left || x >= hit.right {
			continue
		}
		switch button {
		case 1:
			m.selectTab(hit.index)
		case 2:
			m.entries[hit.index].terminal.closeSession()
		}
		return
	}
}

func (m *terminalTabs) titleChanged(id api.WidgetID, title string) {
	m.control.PostWindow(func() {
		for i := range m.entries {
			if m.entries[i].widget.ID() == id {
				m.entries[i].title = title
				m.paintBar()
				return
			}
		}
	})
}

func (m *terminalTabs) terminalClosed(id api.WidgetID) {
	m.control.PostWindow(func() {
		index := -1
		for i, entry := range m.entries {
			if entry.widget.ID() == id {
				index = i
				break
			}
		}
		if index < 0 {
			return
		}
		m.entries = append(m.entries[:index], m.entries[index+1:]...)
		if len(m.entries) == 0 {
			return
		}
		if index < m.active {
			m.active--
		} else if index == m.active {
			m.active = min(index, len(m.entries)-1)
		}
		m.selectTab(m.active)
	})
}

func tabLabel(title string, limit int) string {
	var out strings.Builder
	used := 0
	for _, ch := range title {
		if unicode.IsControl(ch) {
			ch = '?'
		}
		width := term.CellWidth(ch)
		if width < 1 {
			continue
		}
		if used+width > limit {
			break
		}
		out.WriteRune(ch)
		used += width
	}
	return out.String()
}

func (m *terminalTabs) paintBar() {
	if host, ok := m.bar.(api.ScenePainter); ok && host.Scene() != nil {
		m.paintSceneBar(host.Scene())
		return
	}
	if m.bar == nil || !m.bar.Visible() {
		return
	}
	r := m.bar.Bounds()
	w, h := r.Dx(), r.Dy()
	size, ok := limits.RGBA("tab bar", w, h)
	if !ok {
		return
	}
	pixels := make([]uint32, size/4)
	m.paintSceneBar(rasterScene{pixels: pixels, width: w, height: h, clip: image.Rect(0, 0, w, h), glyph: cachedGlyphPixels})
	m.bar.Upload(pixels, w, 0, 0, w, h)
}
