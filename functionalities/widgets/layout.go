package widgets

import (
	"image"

	"st-go/platform_neutral/api"
)

func (h *Host) SetBounds(bounds image.Rectangle) {
	w := h.window
	w.paintMu.Lock()
	if h.Bounds() == bounds {
		w.paintMu.Unlock()
		return
	}
	if h.isClosed() || bounds.Empty() || (h.scene == nil && !h.buffer.Resize(bounds.Dx(), bounds.Dy())) {
		w.paintMu.Unlock()
		return
	}
	w.mu.Lock()
	old := h.options.Bounds
	h.options.Bounds = bounds
	h.generation++
	var children []*surface
	for _, s := range w.surfaces {
		if s.owner == h {
			children = append(children, s)
		}
	}
	w.mu.Unlock()
	if h.scene != nil {
		h.scene.Configure(bounds, 0xff000000)
	}
	if h.bar != nil {
		h.bar.Configure(bounds, 0xff000000)
	}
	for _, s := range children {
		s.configureLocked()
	}
	if h.bar != nil && h.Visible() {
		h.bar.Present(h.buffer.Pixels, h.buffer.Width, h.buffer.Height, 0, false)
	} else if h.bar == nil && h.Visible() {
		w.eraseLocked(old)
		w.repaintLocked()
	}
	w.paintMu.Unlock()
	h.invoke(func(c api.Callbacks) {
		if c.Resize != nil {
			c.Resize(bounds.Dx(), bounds.Dy())
		}
	})
	h.notifyBarLayout()
}

func (h *Host) SetVisible(visible bool) {
	w := h.window
	w.paintMu.Lock()
	w.mu.Lock()
	if h.closed || h.visible == visible {
		w.mu.Unlock()
		w.paintMu.Unlock()
		return
	}
	h.visible = visible
	h.generation++
	wasFocused := w.focused == h
	if !visible && wasFocused {
		w.focused = nil
		w.focusEpoch++
	}
	if !visible && w.captured == h {
		w.captured = nil
	} // consume the outstanding release, never redirect it.
	var children []*surface
	for _, s := range w.surfaces {
		if s.owner == h {
			children = append(children, s)
		}
	}
	bounds := h.options.Bounds
	w.mu.Unlock()
	if h.scene != nil {
		h.scene.SetVisible(visible)
	}
	if h.bar != nil {
		if native, ok := h.bar.(api.SurfaceVisibility); ok {
			native.SetVisible(visible)
		}
	}
	for _, s := range children {
		s.setVisibleLocked(visible && !s.windowBounds().Empty())
	}
	if wasFocused {
		if painter, ok := w.native.(api.TextCursorPainter); ok {
			painter.SetTextCursor(api.TextCursor{})
		}
	}
	if h.bar == nil && !visible {
		w.eraseLocked(bounds)
	}
	w.repaintLocked()
	w.paintMu.Unlock()
	if !visible {
		h.invoke(func(c api.Callbacks) {
			if c.CancelInput != nil {
				c.CancelInput()
			}
			if wasFocused && c.Focus != nil {
				c.Focus(false)
			}
		})
	}
	if visible {
		h.invoke(func(c api.Callbacks) {
			if c.Expose != nil {
				c.Expose()
			}
		})
	}
	h.notifyBarLayout()
}

func (h *Host) FocusWidget() {
	w := h.window
	w.paintMu.Lock()
	w.mu.Lock()
	if h.closed || !h.visible || h.options.Kind != api.TerminalWidget || w.focused == h {
		w.mu.Unlock()
		w.paintMu.Unlock()
		return
	}
	old := w.focused
	w.focused = h
	w.focusEpoch++
	focused := w.nativeFocused
	title, iconTitle := h.title, h.iconTitle
	cursor := h.cursor
	cursor.Bounds = cursor.Bounds.Add(h.options.Bounds.Min)
	w.mu.Unlock()
	if painter, ok := w.native.(api.TextCursorPainter); ok {
		painter.SetTextCursor(cursor)
	}
	if title != "" {
		w.native.SetTitle(title)
	}
	if iconTitle != "" {
		w.native.SetIconTitle(iconTitle)
	}
	w.paintMu.Unlock()
	if old != nil {
		old.invoke(func(c api.Callbacks) {
			if c.Focus != nil {
				c.Focus(false)
			}
		})
	}
	h.invoke(func(c api.Callbacks) {
		if c.Focus != nil {
			c.Focus(focused)
		}
	})
}

func (w *Window) eraseLocked(bounds image.Rectangle) {
	if _, ok := w.native.(api.SceneFactory); ok {
		return
	}
	r := bounds.Intersect(image.Rect(0, 0, w.width, w.height))
	if r.Empty() {
		return
	}
	var blank Buffer
	if !blank.Resize(r.Dx(), r.Dy()) {
		return
	}
	for i := range blank.Pixels {
		blank.Pixels[i] = 0xff000000
	}
	w.upload(blank.Pixels, blank.Width, blank.Bounds(), r.Min)
}
func (w *Window) repaintLocked() {
	for _, h := range w.visible() {
		if h.scene != nil {
			continue
		}
		if h.bar != nil {
			h.bar.Present(h.buffer.Pixels, h.buffer.Width, h.buffer.Height, 0, false)
		} else {
			w.upload(h.buffer.Pixels, h.buffer.Width, h.buffer.Bounds(), h.Bounds().Min)
		}
	}
}
