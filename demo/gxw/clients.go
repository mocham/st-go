package main

import (
	"fmt"
	"image"
	"slices"

	"github.com/jezek/xgb/xproto"
)

func (w *wm) manage(id xproto.Window, activate bool) {
	if id == w.support || id == w.tabbar || id == w.systembar || w.clients[id] != nil {
		return
	}
	a, err := xproto.GetWindowAttributes(w.conn, id).Reply()
	if err != nil {
		return
	}
	if a.OverrideRedirect || a.Class != xproto.WindowClassInputOutput {
		xproto.MapWindow(w.conn, id)
		return
	}
	g, err := xproto.GetGeometry(w.conn, xproto.Drawable(id)).Reply()
	if err != nil {
		return
	}
	c := &client{id: id, input: true, mapped: a.MapState != xproto.MapStateUnmapped, border: g.BorderWidth, original: image.Rect(int(g.X), int(g.Y), int(g.X)+int(g.Width), int(g.Y)+int(g.Height)), requested: image.Pt(int(g.Width), int(g.Height))}
	w.clients[id] = c
	w.order = append(w.order, id)
	w.readMetadata(c)
	state := w.words(id, "_NET_WM_STATE")
	c.fullscreen = w.has(state, "_NET_WM_STATE_FULLSCREEN")
	c.modal = w.has(state, "_NET_WM_STATE_MODAL")
	xproto.ChangeWindowAttributes(w.conn, id, xproto.CwEventMask|xproto.CwBorderPixel, []uint32{w.pixels.pixel(196, 223, 181), xproto.EventMaskPropertyChange})
	// Replay the original click after focusing, so applications see a complete
	// press/release pair. This grab never handles override-redirect popups.
	xproto.GrabButton(w.conn, false, id, xproto.EventMaskButtonPress, xproto.GrabModeSync, xproto.GrabModeAsync, 0, 0, xproto.ButtonIndexAny, xproto.ModMaskAny)
	w.setWords(id, "_NET_WM_DESKTOP", xproto.AtomCardinal, 0)
	w.setWords(id, "_NET_WM_ALLOWED_ACTIONS", xproto.AtomAtom, uint32(w.atoms["_NET_WM_ACTION_CLOSE"]), uint32(w.atoms["_NET_WM_ACTION_FULLSCREEN"]))
	if activate || w.active == 0 {
		w.active = family(id, w.clients)
		w.focused = id
	}
	if activate {
		w.layout()
	}
}

func (w *wm) readTitle(c *client) {
	c.title = w.text(c.id, "_NET_WM_NAME")
	if c.title == "" {
		c.title = w.text(c.id, "WM_NAME")
	}
	if c.title == "" {
		c.title = w.text(c.id, "WM_CLASS")
	}
	if c.title == "" {
		c.title = fmt.Sprintf("Window %x", c.id)
	}
}

func (w *wm) readMetadata(c *client) {
	w.readTitle(c)
	c.transient = 0
	if p := w.words(c.id, "WM_TRANSIENT_FOR"); len(p) > 0 && xproto.Window(p[0]) != c.id {
		c.transient = xproto.Window(p[0])
	}
	// Reject cycles before associating the dialog with a tab.
	for parent, n := c.transient, 0; parent != 0 && n <= len(w.clients); n++ {
		if parent == c.id {
			c.transient = 0
			break
		}
		p := w.clients[parent]
		if p == nil {
			break
		}
		parent = p.transient
	}
	c.explicitDialog = w.has(w.words(c.id, "_NET_WM_WINDOW_TYPE"), "_NET_WM_WINDOW_TYPE_DIALOG")
	c.dialog = isDialog(c.explicitDialog, c.transient, c.id, w.root, w.clients)
	protocols := w.words(c.id, "WM_PROTOCOLS")
	c.takeFocus = w.has(protocols, "WM_TAKE_FOCUS")
	c.deleteWindow = w.has(protocols, "WM_DELETE_WINDOW")
	c.input = true
	if h := w.words(c.id, "WM_HINTS"); len(h) >= 2 {
		if h[0]&1 != 0 {
			c.input = h[1] != 0
		}
		c.urgent = h[0]&(1<<8) != 0
	}
	c.hints = sizeHints{}
	if h := w.words(c.id, "WM_NORMAL_HINTS"); len(h) >= 15 {
		point := func(i int) image.Point { return image.Pt(int(int32(h[i])), int(int32(h[i+1]))) }
		if h[0]&(1<<4) != 0 {
			c.hints.min = point(5)
			c.hints.base = c.hints.min
		}
		if h[0]&(1<<5) != 0 {
			c.hints.max = point(7)
		}
		if h[0]&(1<<6) != 0 {
			c.hints.increment = point(9)
		}
		if h[0]&(1<<8) != 0 && len(h) >= 17 {
			c.hints.base = point(15)
		}
	}
}

func (w *wm) remove(id xproto.Window) {
	c := w.clients[id]
	if c == nil {
		return
	}
	delete(w.clients, id)
	w.order = slices.DeleteFunc(w.order, func(v xproto.Window) bool { return v == id })
	xproto.UngrabButton(w.conn, xproto.ButtonIndexAny, id, xproto.ModMaskAny)
	xproto.DeleteProperty(w.conn, id, w.atoms["WM_STATE"])
	xproto.DeleteProperty(w.conn, id, w.atoms["_NET_WM_STATE"])
	xproto.DeleteProperty(w.conn, id, w.atoms["_NET_WM_ALLOWED_ACTIONS"])
	if w.focused == id {
		w.focused = 0
	}
	if w.active == id {
		w.active = 0
	}
	w.layout()
}

func (w *wm) layout() {
	w.tabs = w.tabs[:0]
	for _, c := range w.clients {
		c.dialog = isDialog(c.explicitDialog, c.transient, c.id, w.root, w.clients)
	}
	for _, id := range w.order {
		if family(id, w.clients) == id {
			w.tabs = append(w.tabs, id)
		}
	}
	if c := w.clients[w.active]; c != nil {
		w.active = family(c.id, w.clients)
	}
	if !slices.Contains(w.tabs, w.active) {
		w.active = 0
		if len(w.tabs) > 0 {
			w.active = w.tabs[len(w.tabs)-1]
		}
	}
	var visible, hidden []uint32
	// Configure each client to its final geometry before making it visible.
	for _, id := range w.order {
		c := w.clients[id]
		show := family(id, w.clients) == w.active
		if show {
			visible = append(visible, uint32(id))
		} else {
			hidden = append(hidden, uint32(id))
		}
		border := 0
		bounds := w.workarea()
		if c.dialog {
			border = 2
			bounds = dialogBounds(bounds, c.requested, c.hints, border)
		}
		if c.bounds != bounds {
			c.bounds = bounds
			xproto.ConfigureWindow(w.conn, id, xproto.ConfigWindowX|xproto.ConfigWindowY|xproto.ConfigWindowWidth|xproto.ConfigWindowHeight|xproto.ConfigWindowBorderWidth, []uint32{uint32(bounds.Min.X), uint32(bounds.Min.Y), uint32(bounds.Dx()), uint32(bounds.Dy()), uint32(border)})
			w.configureNotify(c)
		}
		w.setWords(id, "_NET_FRAME_EXTENTS", xproto.AtomCardinal, uint32(border), uint32(border), uint32(border), uint32(border))
		if show && !c.mapped {
			xproto.MapWindow(w.conn, id)
			c.mapped = true
		}
		if !show && c.mapped {
			cookie := xproto.UnmapWindow(w.conn, id)
			c.unmaps = append(c.unmaps, cookie.Sequence)
			c.mapped = false
		}
		w.updateState(c)
	}
	// Normal client first, followed by its transients in increasing depth.
	slices.SortStableFunc(visible, func(a, b uint32) int { return w.depth(xproto.Window(a)) - w.depth(xproto.Window(b)) })
	for _, id := range visible {
		xproto.ConfigureWindow(w.conn, xproto.Window(id), xproto.ConfigWindowStackMode, []uint32{xproto.StackModeAbove})
	}
	for _, id := range []xproto.Window{w.tabbar, w.systembar} {
		xproto.ConfigureWindow(w.conn, id, xproto.ConfigWindowStackMode, []uint32{xproto.StackModeAbove})
	}
	list := make([]uint32, 0, len(w.order))
	for _, id := range w.order {
		list = append(list, uint32(id))
	}
	w.setWords(w.root, "_NET_CLIENT_LIST", xproto.AtomWindow, list...)
	w.setWords(w.root, "_NET_CLIENT_LIST_STACKING", xproto.AtomWindow, append(hidden, visible...)...)
	target := w.focused
	if c := w.clients[target]; c == nil || !c.mapped {
		target = w.active
	}
	if len(visible) > 0 {
		last := w.clients[xproto.Window(visible[len(visible)-1])]
		if last.dialog {
			target = last.id
		}
	}
	w.focus(target)
	w.drawBars()
}

func (w *wm) depth(id xproto.Window) int {
	n := 0
	for c := w.clients[id]; c != nil && c.dialog && w.clients[c.transient] != nil && n < len(w.clients); c = w.clients[c.transient] {
		n++
	}
	return n
}

func (w *wm) activate(id xproto.Window) {
	if w.clients[id] == nil {
		return
	}
	w.active = family(id, w.clients)
	w.focused = id
	w.clients[id].urgent = false
	w.layout()
}

func (w *wm) focus(id xproto.Window) {
	c := w.clients[id]
	if c != nil && !c.mapped {
		return
	}
	// Modal dialogs keep focus in their own subtree even on a parent click.
	for _, other := range w.order {
		d := w.clients[other]
		if d.mapped && d.dialog && d.modal && w.depth(d.id) >= w.depth(id) {
			id = d.id
			c = d
		}
	}
	w.focused = id
	if c == nil {
		xproto.SetInputFocus(w.conn, xproto.InputFocusPointerRoot, w.root, xproto.TimeCurrentTime)
	} else {
		if c.input {
			xproto.SetInputFocus(w.conn, xproto.InputFocusPointerRoot, id, xproto.TimeCurrentTime)
		} else {
			xproto.SetInputFocus(w.conn, xproto.InputFocusPointerRoot, w.root, xproto.TimeCurrentTime)
		}
		if c.takeFocus {
			w.sendProtocol(c, "WM_TAKE_FOCUS", w.timestamp)
		}
	}
	w.setWords(w.root, "_NET_ACTIVE_WINDOW", xproto.AtomWindow, uint32(id))
}

func (w *wm) cycle(delta int) {
	if len(w.tabs) == 0 {
		return
	}
	i := slices.Index(w.tabs, w.active)
	i = (i + delta + len(w.tabs)) % len(w.tabs)
	w.activate(w.tabs[i])
}

func (w *wm) configureNotify(c *client) {
	b := uint16(0)
	if c.dialog {
		b = 2
	}
	e := xproto.ConfigureNotifyEvent{Event: c.id, Window: c.id, X: int16(c.bounds.Min.X), Y: int16(c.bounds.Min.Y), Width: uint16(c.bounds.Dx()), Height: uint16(c.bounds.Dy()), BorderWidth: b}
	xproto.SendEvent(w.conn, false, c.id, xproto.EventMaskStructureNotify, string(e.Bytes()))
}

func (w *wm) configureRequest(e xproto.ConfigureRequestEvent) {
	if c := w.clients[e.Window]; c != nil {
		if c.dialog {
			if e.ValueMask&xproto.ConfigWindowWidth != 0 {
				c.requested.X = int(e.Width)
			}
			if e.ValueMask&xproto.ConfigWindowHeight != 0 {
				c.requested.Y = int(e.Height)
			}
			w.layout()
		}
		w.configureNotify(c)
		return
	}
	values := []uint32{}
	for i, v := range []uint32{uint32(int32(e.X)), uint32(int32(e.Y)), uint32(e.Width), uint32(e.Height), uint32(e.BorderWidth), uint32(e.Sibling), uint32(e.StackMode)} {
		if e.ValueMask&(1<<i) != 0 {
			values = append(values, v)
		}
	}
	xproto.ConfigureWindow(w.conn, e.Window, e.ValueMask, values)
}
