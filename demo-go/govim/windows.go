package main

import (
	"fmt"
	"sort"
)

func (e *Editor) windows() []*Window {
	var windows []*Window
	walkPanes(e.tabs[e.activeTab].root, func(w *Window) { windows = append(windows, w) })
	return windows
}
func findPane(root *pane, w *Window) *pane {
	if root == nil {
		return nil
	}
	if root.window == w {
		return root
	}
	if found := findPane(root.first, w); found != nil {
		return found
	}
	return findPane(root.second, w)
}
func removePane(root *pane, w *Window) *pane {
	if root == nil || root.window == w {
		return nil
	}
	if root.window != nil {
		return root
	}
	root.first = removePane(root.first, w)
	root.second = removePane(root.second, w)
	if root.first == nil {
		return root.second
	}
	if root.second == nil {
		return root.first
	}
	return root
}
func (e *Editor) splitWindow(vertical bool) error {
	if len(e.windows()) >= 16 {
		return fmt.Errorf("window limit reached")
	}
	w := e.window()
	leaf := findPane(e.tabs[e.activeTab].root, w)
	if leaf == nil {
		return fmt.Errorf("window unavailable")
	}
	copy := *w
	copy.rows = nil
	*leaf = pane{vertical: vertical, ratio: 0.5, first: &pane{window: w}, second: &pane{window: &copy}}
	e.tabs[e.activeTab].active = &copy
	e.dirty = true
	return nil
}
func (e *Editor) visibleElsewhere(b *Buffer, exclude *Window) bool {
	found := false
	for _, tab := range e.tabs {
		walkPanes(tab.root, func(w *Window) {
			if w != exclude && w.buffer == b {
				found = true
			}
		})
	}
	return found
}
func (e *Editor) quitWindow(force bool) {
	w := e.window()
	if w.buffer.dirty() && !force && !e.visibleElsewhere(w.buffer, w) {
		e.error(fmt.Errorf("unsaved changes (use :w or :q!)"))
		return
	}
	if len(e.windows()) == 1 {
		if len(e.tabs) > 1 {
			e.closeTab(force)
			return
		}
		if !force {
			for _, b := range e.buffers {
				if b.dirty() {
					e.error(fmt.Errorf("unsaved changes in %s", fileLabel(b)))
					return
				}
			}
		}
		e.quit = true
		e.discard = force
		return
	}
	tab := e.tabs[e.activeTab]
	tab.root = removePane(tab.root, w)
	walkPanes(tab.root, func(next *Window) {
		if tab.active == w {
			tab.active = next
		}
	})
	e.mode = normalMode
	e.reset()
	e.dirty = true
}
func (e *Editor) onlyWindow(force bool) {
	active := e.window()
	if !force {
		for _, w := range e.windows() {
			if w != active && w.buffer != active.buffer && w.buffer.dirty() {
				e.error(fmt.Errorf("unsaved changes in another window"))
				return
			}
		}
	}
	e.tabs[e.activeTab].root = &pane{window: active}
	e.dirty = true
}
func (e *Editor) closeTab(force bool) {
	if len(e.tabs) == 1 {
		e.quitWindow(force)
		return
	}
	current := e.tabs[e.activeTab]
	if !force {
		blocked := false
		walkPanes(current.root, func(w *Window) {
			if w.buffer.dirty() {
				elsewhere := false
				for _, t := range e.tabs {
					if t == current {
						continue
					}
					walkPanes(t.root, func(other *Window) {
						if other.buffer == w.buffer {
							elsewhere = true
						}
					})
				}
				if !elsewhere {
					blocked = true
				}
			}
		})
		if blocked {
			e.error(fmt.Errorf("unsaved changes in tab"))
			return
		}
	}
	e.tabs = append(e.tabs[:e.activeTab], e.tabs[e.activeTab+1:]...)
	e.activeTab = min(e.activeTab, len(e.tabs)-1)
	e.dirty = true
}
func (e *Editor) deleteBuffer(force bool) {
	b := e.buffer()
	if b.dirty() && !force {
		e.error(fmt.Errorf("unsaved changes"))
		return
	}
	var replacement *Buffer
	for _, other := range e.buffers {
		if other != b {
			replacement = other
			break
		}
	}
	if replacement == nil {
		var err error
		replacement, err = e.open("", false)
		if err != nil {
			e.error(err)
			return
		}
	}
	for _, tab := range e.tabs {
		walkPanes(tab.root, func(w *Window) {
			if w.buffer == b {
				w.buffer = replacement
				w.cursor = 0
				w.topLine, w.topWrap = 0, 0
			}
		})
	}
	if b.journal != nil {
		if err := b.journal.Close(true); err != nil {
			e.error(err)
		}
	}
	for i, buffer := range e.buffers {
		if buffer == b {
			e.buffers = append(e.buffers[:i], e.buffers[i+1:]...)
			break
		}
	}
	for mark, loc := range e.marks {
		if loc.buffer == b {
			delete(e.marks, mark)
		}
	}
	filter := func(values []location) []location {
		result := values[:0]
		for _, loc := range values {
			if loc.buffer != b {
				result = append(result, loc)
			}
		}
		return result
	}
	e.jumps, e.changes = filter(e.jumps), filter(e.changes)
	e.jumpIndex, e.changeIndex = len(e.jumps), len(e.changes)
	if e.alternate == b {
		e.alternate = nil
	}
	if e.lastJump.buffer == b {
		e.lastJump = location{}
	}
	if e.lastVisual.buffer == b {
		e.lastVisual = visualSelection{}
	}
	e.dirty = true
}
func (e *Editor) windowCommand(key string, count int) {
	switch key {
	case "s", "S", "\x13":
		e.error(e.splitWindow(false))
	case "v", "\x16":
		e.error(e.splitWindow(true))
	case "c", "q":
		e.quitWindow(false)
	case "o", "\x0f":
		e.onlyWindow(false)
	case "n":
		e.execute("new")
	case "w", "W", "\x17", "t", "b", "p":
		windows := e.windows()
		index := 0
		for i, w := range windows {
			if w == e.window() {
				index = i
			}
		}
		switch key {
		case "W":
			index = (index - count%len(windows) + len(windows)) % len(windows)
		case "t":
			index = 0
		case "b":
			index = len(windows) - 1
		default:
			index = (index + count) % len(windows)
		}
		e.tabs[e.activeTab].active = windows[index]
	case "h", "j", "k", "l", "<Left>", "<Right>", "<Up>", "<Down>":
		current := e.window()
		candidates := e.windows()
		cx, cy := current.rect.x+current.rect.w/2, current.rect.y+current.rect.h/2
		score := func(w *Window) int {
			x, y := w.rect.x+w.rect.w/2, w.rect.y+w.rect.h/2
			dx, dy := x-cx, y-cy
			switch key {
			case "h", "<Left>":
				if dx >= 0 {
					return 1 << 30
				}
			case "l", "<Right>":
				if dx <= 0 {
					return 1 << 30
				}
			case "j", "<Down>":
				if dy <= 0 {
					return 1 << 30
				}
			case "k", "<Up>":
				if dy >= 0 {
					return 1 << 30
				}
			}
			return dx*dx + dy*dy
		}
		sort.SliceStable(candidates, func(i, j int) bool { return score(candidates[i]) < score(candidates[j]) })
		if len(candidates) > 0 && score(candidates[0]) < 1<<30 {
			e.tabs[e.activeTab].active = candidates[0]
		}
	case "=":
		var reset func(*pane)
		reset = func(p *pane) {
			if p == nil {
				return
			}
			p.ratio = 0.5
			reset(p.first)
			reset(p.second)
		}
		reset(e.tabs[e.activeTab].root)
	case "+", "-", ">", "<", "_", "|":
		w := e.window()
		var parent *pane
		var find func(*pane)
		find = func(p *pane) {
			if p == nil || p.window != nil {
				return
			}
			if p.first.window == w || p.second.window == w {
				parent = p
			}
			find(p.first)
			find(p.second)
		}
		find(e.tabs[e.activeTab].root)
		if parent != nil {
			delta := 0.05 * float64(min(count, 10))
			if key == "-" || key == "<" {
				delta = -delta
			}
			if parent.second.window == w {
				delta = -delta
			}
			if key == "_" || key == "|" {
				if parent.first.window == w {
					parent.ratio = 0.85
				} else {
					parent.ratio = 0.15
				}
			} else {
				parent.ratio = max(0.1, min(0.9, parent.ratio+delta))
			}
		}
	default:
		e.message = "Unsupported window command: " + key
	}
	e.dirty = true
}

func layoutPane(p *pane, rect rectangle) {
	if p == nil {
		return
	}
	if p.window != nil {
		p.window.rect = rect
		return
	}
	ratio := p.ratio
	if ratio <= 0 {
		ratio = 0.5
	}
	if p.vertical {
		first := clamp(int(float64(rect.w)*ratio), 1, max(1, rect.w-2))
		layoutPane(p.first, rectangle{rect.x, rect.y, first, rect.h})
		layoutPane(p.second, rectangle{rect.x + first + 1, rect.y, max(1, rect.w-first-1), rect.h})
	} else {
		first := clamp(int(float64(rect.h)*ratio), 1, max(1, rect.h-1))
		layoutPane(p.first, rectangle{rect.x, rect.y, rect.w, first})
		layoutPane(p.second, rectangle{rect.x, rect.y + first, rect.w, max(1, rect.h-first)})
	}
}
func (e *Editor) scroll(delta int, moveCursor bool) {
	w, b := e.window(), e.buffer()
	row := b.row(e.pos())
	if moveCursor {
		row = clamp(row+delta, 0, len(b.lineStarts())-1)
		e.setPos(firstNonblank(b, row))
	}
	w.topLine = clamp(w.topLine+delta, 0, len(b.lineStarts())-1)
	w.topWrap = 0
	if !moveCursor {
		if row < w.topLine {
			e.setPos(firstNonblank(b, w.topLine))
		} else if row >= w.topLine+max(1, w.rect.h-1) {
			e.setPos(firstNonblank(b, min(len(b.lineStarts())-1, w.topLine+max(1, w.rect.h-2))))
		}
	}
}
