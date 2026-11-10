package widgets

import (
	"fmt"
	"image"
	"sync"

	"st-go/platform_neutral/api"
)

// Host keeps the last committed widget pixels independently of every other
// widget. Hidden uploads are successful no-ops, not queued future paints.
type Host struct {
	window           *Window
	id               api.WidgetID
	options          api.WidgetOptions
	visible, closed  bool
	callbacks        api.Callbacks
	buffer           Buffer
	bar              api.Surface
	cursor           api.TextCursor
	generation       uint64
	title, iconTitle string
	done             chan struct{}
	closeOnce        sync.Once
}

func (h *Host) ID() api.WidgetID                                  { return h.id }
func (h *Host) Kind() api.WidgetKind                              { return h.options.Kind }
func (h *Host) NativeHost() api.Host                              { return h.window.native }
func (h *Host) NewWidget(o api.WidgetOptions) (api.Widget, error) { return h.window.NewWidget(o) }
func (h *Host) Bind(c api.Callbacks)                              { h.window.mu.Lock(); h.callbacks = c; h.window.mu.Unlock() }
func (h *Host) Bounds() image.Rectangle {
	h.window.mu.Lock()
	defer h.window.mu.Unlock()
	return h.options.Bounds
}
func (h *Host) Visible() bool {
	h.window.mu.Lock()
	defer h.window.mu.Unlock()
	return h.visible && !h.closed
}
func (h *Host) ClientSize() (int, int, error) { r := h.Bounds(); return r.Dx(), r.Dy(), nil }
func (h *Host) ScreenSize() (int, int)        { return h.window.native.ScreenSize() }
func (h *Host) WindowRect() (api.Rect, error) { return h.window.native.WindowRect() }
func (h *Host) RequestWindowRect(r api.Rect) {
	if h.isFocused() {
		h.window.native.RequestWindowRect(r)
	}
}
func (h *Host) SetupKeys() { h.window.native.SetupKeys() }
func (h *Host) Show() {
	w := h.window
	w.native.Show()
	if width, height, err := w.native.ClientSize(); err == nil {
		w.resize(width, height)
	}
}
func (h *Host) BackendName() string {
	if native, ok := h.window.native.(api.BackendIdentity); ok {
		return native.BackendName()
	}
	return "unknown"
}
func (h *Host) SupportsWindowGeometry() bool {
	if native, ok := h.window.native.(api.GeometrySupport); ok {
		return native.SupportsWindowGeometry()
	}
	return true
}
func (h *Host) BeginPaint() {
	if batch, ok := h.window.native.(api.PaintBatcher); ok {
		batch.BeginPaint()
	}
}
func (h *Host) EndPaint() {
	if batch, ok := h.window.native.(api.PaintBatcher); ok {
		batch.EndPaint()
	}
}
func (h *Host) SetTitle(s string) {
	h.window.paintMu.Lock()
	defer h.window.paintMu.Unlock()
	h.window.mu.Lock()
	h.title = s
	h.window.mu.Unlock()
	if h.isFocused() {
		h.window.native.SetTitle(s)
	}
}
func (h *Host) SetIconTitle(s string) {
	h.window.paintMu.Lock()
	defer h.window.paintMu.Unlock()
	h.window.mu.Lock()
	h.iconTitle = s
	h.window.mu.Unlock()
	if h.isFocused() {
		h.window.native.SetIconTitle(s)
	}
}
func (h *Host) SetClipboard(selection byte, text string) {
	h.window.post(func() {
		if !h.isClosed() {
			h.window.native.SetClipboard(selection, text)
		}
	})
}
func (h *Host) QueryClipboard(selection byte, reply func(string, bool)) {
	if !h.window.post(func() {
		if h.isClosed() {
			return
		}
		h.window.native.QueryClipboard(selection, func(text string, ok bool) {
			h.invoke(func(api.Callbacks) { reply(text, ok) })
		})
	}) {
		reply("", false)
	}
}
func (h *Host) RequestPaste() {
	// Capture the requesting widget, not whichever widget is focused when an
	// asynchronous native clipboard transfer finishes.
	w := h.window
	w.mu.Lock()
	epoch := w.focusEpoch
	w.mu.Unlock()
	w.post(func() {
		if !h.isFocused() {
			return
		}
		w.native.QueryClipboard('c', func(text string, ok bool) {
			h.invoke(func(c api.Callbacks) {
				w.mu.Lock()
				current := w.focusEpoch == epoch && w.focused == h && h.visible
				w.mu.Unlock()
				if ok && current && c.Paste != nil {
					c.Paste(text)
				}
			})
		})
	})
}
func (h *Host) isClosed() bool {
	h.window.mu.Lock()
	defer h.window.mu.Unlock()
	return h.closed || h.window.closed.Load()
}
func (h *Host) isFocused() bool {
	h.window.mu.Lock()
	defer h.window.mu.Unlock()
	return !h.closed && h.visible && h.window.focused == h
}

func (h *Host) PresentationState() (uint64, bool) {
	h.window.mu.Lock()
	defer h.window.mu.Unlock()
	return h.generation, h.visible && !h.closed && !h.window.closed.Load()
}
func (h *Host) invoke(call func(api.Callbacks)) {
	h.window.mu.Lock()
	c := h.callbacks
	closed := h.closed
	h.window.mu.Unlock()
	if closed {
		return
	}
	if c.Lock != nil {
		c.Lock()
	}
	defer func() {
		if c.Unlock != nil {
			c.Unlock()
		}
	}()
	if c.Closed == nil || !c.Closed() {
		call(c)
	}
}

func (h *Host) Run() {
	w := h.window
	w.runOnce.Do(func() { go func() { defer close(w.nativeDone); w.native.Run(); w.close() }() })
	select {
	case <-h.done:
	case <-w.done:
	}
	// Console cleanup happens in native Run, not in Close. Do not let the
	// executable exit before its VT and input modes have been restored.
	if w.closed.Load() {
		<-w.nativeDone
	}
}

func (h *Host) Upload(pixels []uint32, stride, x, y, width, height int) {
	w := h.window
	w.paintMu.Lock()
	defer w.paintMu.Unlock()
	w.mu.Lock()
	visible := h.visible && !h.closed && !w.closed.Load()
	bounds := h.options.Bounds
	w.mu.Unlock()
	if !visible || width <= 0 || height <= 0 {
		return
	}
	r := h.buffer.Copy(pixels, stride, image.Rect(x, y, x+width, y+height), image.Pt(x, y))
	if r.Empty() {
		return
	}
	if h.bar != nil {
		h.bar.Present(h.buffer.Pixels, h.buffer.Width, h.buffer.Height, 0, false)
		return
	}
	w.upload(h.buffer.Pixels, h.buffer.Width, r, r.Min.Add(bounds.Min))
}

func (h *Host) SetTextCursor(cursor api.TextCursor) {
	w := h.window
	w.paintMu.Lock()
	defer w.paintMu.Unlock()
	w.mu.Lock()
	h.cursor.Bounds = cursor.Bounds
	h.cursor.Pixels = append(h.cursor.Pixels[:0], cursor.Pixels...)
	h.cursor.Base = append(h.cursor.Base[:0], cursor.Base...)
	focused := h.visible && !h.closed && w.focused == h
	origin := h.options.Bounds.Min
	w.mu.Unlock()
	if !focused {
		return
	}
	if painter, ok := w.native.(api.TextCursorPainter); ok {
		cursor.Bounds = cursor.Bounds.Add(origin)
		painter.SetTextCursor(cursor)
	}
}

func (h *Host) Close() {
	h.closeOnce.Do(func() {
		w := h.window
		w.paintMu.Lock()
		w.mu.Lock()
		h.closed = true
		h.visible = false
		h.generation++
		if w.focused == h {
			w.focused = nil
			w.focusEpoch++
			if painter, ok := w.native.(api.TextCursorPainter); ok {
				painter.SetTextCursor(api.TextCursor{})
			}
		}
		if w.captured == h {
			w.captured = nil
		}
		last := h.options.Kind == api.TerminalWidget
		for _, other := range w.widgets {
			if other != h && !other.closed && other.options.Kind == api.TerminalWidget {
				last = false
			}
		}
		for i, widget := range w.widgets {
			if widget == h {
				w.widgets = append(w.widgets[:i], w.widgets[i+1:]...)
				break
			}
		}
		// A long-lived window must not retain closed terminals through callback
		// closures (which otherwise retain their private atlases and PDF jobs).
		h.callbacks = api.Callbacks{}
		h.cursor = api.TextCursor{}
		var children []*surface
		for _, s := range w.surfaces {
			if s.owner == h {
				children = append(children, s)
			}
		}
		w.mu.Unlock()
		for _, s := range children {
			s.closeLocked()
		}
		if h.bar != nil {
			h.bar.Close()
			h.bar = nil
		}
		bounds := h.options.Bounds
		h.buffer = Buffer{}
		if !last && !w.closed.Load() {
			w.eraseLocked(bounds)
			w.repaintLocked()
		}
		w.paintMu.Unlock()
		if last {
			w.close()
		}
		close(h.done)
	})
}

type videoHost struct{ *Host }

func (h *videoHost) StartNativeVideo(r api.NativeVideoRequest, done func(error)) error {
	if !h.isFocused() {
		return fmt.Errorf("native video requires the focused terminal widget")
	}
	return h.window.native.(api.NativeVideoHost).StartNativeVideo(r, done)
}
