package widgets

import (
	"image"

	"st-go/platform_neutral/api"
)

func (w *Window) callbacks() api.Callbacks {
	return api.Callbacks{
		Lock: w.eventMu.Lock, Unlock: w.eventMu.Unlock, Closed: w.closed.Load, Close: w.close,
		Expose: func() {
			for _, h := range w.visible() {
				h.invoke(func(c api.Callbacks) {
					if c.Expose != nil && h.Visible() {
						c.Expose()
					}
				})
			}
		},
		ExposeSurface: func(native api.Surface) {
			if native == nil {
				return
			}
			w.paintMu.Lock()
			w.mu.Lock()
			s := w.surfaces[native]
			var bar *Host
			if s == nil {
				for _, h := range w.widgets {
					if h.bar == native && h.visible && !h.closed {
						bar = h
						break
					}
				}
			}
			w.mu.Unlock()
			if bar != nil && !bar.bar.Expose() {
				bar.bar.Present(bar.buffer.Pixels, bar.buffer.Width, bar.buffer.Height, 0, false)
			}
			w.paintMu.Unlock()
			if s != nil {
				if !s.owner.Visible() {
					return
				}
				s.owner.invoke(func(c api.Callbacks) {
					if c.ExposeSurface != nil && s.owner.Visible() {
						c.ExposeSurface(s)
					}
				})
			}
		},
		KeyPress: func(key uint, mods uint16) {
			w.mu.Lock()
			intercept := w.hooks.KeyPress
			w.mu.Unlock()
			if intercept != nil && intercept(key, mods) {
				return
			}
			w.withFocus(func(c api.Callbacks) {
				if c.KeyPress != nil {
					c.KeyPress(key, mods)
				}
			})
		},
		Text: func(text string) {
			w.withFocus(func(c api.Callbacks) {
				if c.Text != nil {
					c.Text(text)
				}
			})
		},
		Paste: func(text string) {
			w.withFocus(func(c api.Callbacks) {
				if c.Paste != nil {
					c.Paste(text)
				}
			})
		},
		PointerPress:   func(button byte, mods uint16, x, y int) { w.pointer(0, button, mods, x, y) },
		PointerRelease: func(button byte, mods uint16, x, y int) { w.pointer(1, button, mods, x, y) },
		PointerMotion:  func(mods uint16, x, y int) { w.pointer(2, 0, mods, x, y) },
		Resize:         w.resize,
		Focus: func(focused bool) {
			w.mu.Lock()
			w.nativeFocused = focused
			w.mu.Unlock()
			w.withFocus(func(c api.Callbacks) {
				if c.Focus != nil {
					c.Focus(focused)
				}
			})
		},
		PaintDirty: func() {
			for _, h := range w.visible() {
				h.invoke(func(c api.Callbacks) {
					if c.PaintDirty != nil && h.Visible() {
						c.PaintDirty()
					}
				})
			}
		},
	}
}

func (w *Window) withFocus(call func(api.Callbacks)) {
	w.mu.Lock()
	h := w.focused
	w.mu.Unlock()
	if h != nil && h.isFocused() {
		h.invoke(func(c api.Callbacks) {
			if h.isFocused() {
				call(c)
			}
		})
	}
}

func (w *Window) pointer(kind int, button byte, mods uint16, x, y int) {
	w.mu.Lock()
	h := w.captured
	if h == nil && w.buttons == 0 {
		for i := len(w.widgets) - 1; i >= 0; i-- {
			candidate := w.widgets[i]
			if candidate.visible && !candidate.closed && !candidate.options.PointerTransparent && image.Pt(x, y).In(candidate.options.Bounds) {
				h = candidate
				break
			}
		}
	}
	mask := uint32(0)
	if button > 0 && button <= 32 {
		mask = 1 << (button - 1)
	}
	if kind == 0 && (button < 4 || button > 7) && mask != 0 {
		w.buttons |= mask
		w.captured = h
	}
	if kind == 1 {
		w.buttons &^= mask
		if w.buttons == 0 {
			w.captured = nil
		}
	}
	var origin image.Point
	if h != nil {
		origin = h.options.Bounds.Min
	}
	w.mu.Unlock()
	if h == nil || !h.Visible() {
		return
	}
	if kind == 0 && h.Kind() == api.TerminalWidget {
		h.FocusWidget()
	}
	h.invoke(func(c api.Callbacks) {
		if !h.Visible() {
			return
		}
		switch kind {
		case 0:
			if c.PointerPress != nil {
				c.PointerPress(button, mods, x-origin.X, y-origin.Y)
			}
		case 1:
			if c.PointerRelease != nil {
				c.PointerRelease(button, mods, x-origin.X, y-origin.Y)
			}
		case 2:
			if c.PointerMotion != nil {
				c.PointerMotion(mods, x-origin.X, y-origin.Y)
			}
		}
	})
}

func (w *Window) resize(width, height int) {
	if width <= 0 || height <= 0 {
		return
	}
	w.paintMu.Lock()
	w.mu.Lock()
	w.width, w.height = width, height
	resize := w.hooks.Resize
	var follow []*Host
	for _, h := range w.widgets {
		if h.options.AutoResize && !h.closed {
			follow = append(follow, h)
		}
	}
	w.mu.Unlock()
	w.paintMu.Unlock()
	for _, h := range follow {
		h.SetBounds(image.Rect(0, 0, width, height))
	}
	if resize != nil {
		resize(width, height)
	}
}
