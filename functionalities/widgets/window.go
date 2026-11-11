package widgets

import (
	"fmt"
	"image"
	"sync"
	"sync/atomic"

	"st-go/platform_neutral/api"
)

// Window coordinates native callbacks and presentation, not terminal parsing.
// Never call client callbacks while holding mu or paintMu: callbacks can paint.
type Window struct {
	native               api.Host
	mu, paintMu, eventMu sync.Mutex
	widgets              []*Host
	surfaces             map[api.Surface]*surface
	next                 api.WidgetID
	focused, captured    *Host
	nativeFocused        bool
	focusEpoch           uint64
	buttons              uint32
	width, height        int
	closed               atomic.Bool
	done                 chan struct{}
	nativeDone           chan struct{}
	operations           chan func()
	runOnce              sync.Once
	hooks                api.WindowHooks
}

// Wrap creates the single, full-client terminal widget used by today's layout.
// Existing widget hosts are returned unchanged (useful for embedded clients).
func Wrap(native api.Host) (api.Widget, error) {
	if widget, ok := native.(api.Widget); ok {
		return widget, nil
	}
	w, h, err := native.ClientSize()
	if err != nil {
		return nil, err
	}
	window := &Window{native: native, width: w, height: h, done: make(chan struct{}), nativeDone: make(chan struct{}),
		operations: make(chan func(), 64), surfaces: make(map[api.Surface]*surface)}
	root, err := window.NewWidget(api.WidgetOptions{Kind: api.TerminalWidget, Bounds: image.Rect(0, 0, w, h), AutoResize: true})
	if err != nil {
		return nil, err
	}
	native.Bind(window.callbacks())
	if owner, ok := native.(api.WidgetAttacher); ok {
		owner.AttachWidgets(window)
	}
	root.FocusWidget()
	go window.operate()
	return root, nil
}

func (w *Window) NewWidget(options api.WidgetOptions) (api.Widget, error) {
	w.paintMu.Lock()
	defer w.paintMu.Unlock()
	if options.Bounds.Empty() || options.Kind > api.BarWidget {
		return nil, fmt.Errorf("invalid widget options")
	}
	h := &Host{window: w, options: options, visible: !options.Hidden, done: make(chan struct{})}
	factory, semantic := w.native.(api.SceneFactory)
	if semantic {
		var err error
		h.scene, err = factory.NewSceneSurface(options.Kind, options.Bounds, 0xff000000, h.visible)
		if err != nil {
			return nil, err
		}
	} else if !h.buffer.Resize(options.Bounds.Dx(), options.Bounds.Dy()) {
		return nil, fmt.Errorf("invalid widget size")
	}
	w.mu.Lock()
	if w.closed.Load() {
		w.mu.Unlock()
		return nil, fmt.Errorf("window closed")
	}
	w.next++
	h.id = w.next
	w.widgets = append(w.widgets, h)
	w.mu.Unlock()
	if options.Kind == api.BarWidget && h.scene == nil {
		var err error
		if native, ok := w.native.(api.WidgetSurfaceFactory); ok {
			h.bar, err = native.NewWidgetSurfaceVisible(api.BarWidget, options.Bounds, 0xff000000, h.visible)
		} else if native, ok := w.native.(api.WidgetSurfaceHost); ok {
			h.bar, err = native.NewWidgetSurface(api.BarWidget, options.Bounds, 0xff000000)
		} else {
			h.bar, err = w.native.NewSurface(options.Bounds, 0xff000000)
		}
		if err != nil {
			w.mu.Lock()
			h.closed, h.visible = true, false
			for i, widget := range w.widgets {
				if widget == h {
					w.widgets = append(w.widgets[:i], w.widgets[i+1:]...)
					break
				}
			}
			w.mu.Unlock()
			return nil, err
		}
		if native, ok := h.bar.(api.SurfaceVisibility); ok {
			native.SetVisible(h.visible)
		}
	}
	h.notifyBarLayout()
	if _, ok := w.native.(api.NativeVideoHost); ok {
		return &videoHost{Host: h}, nil
	}
	return h, nil
}

func (w *Window) visible() []*Host {
	w.mu.Lock()
	defer w.mu.Unlock()
	var result []*Host
	for _, h := range w.widgets {
		if h.visible && !h.closed {
			result = append(result, h)
		}
	}
	return result
}

func (w *Window) close() {
	if !w.closed.CompareAndSwap(false, true) {
		return
	}
	w.mu.Lock()
	var all []*Host
	for _, h := range w.widgets {
		if !h.closed {
			all = append(all, h)
		}
	}
	w.mu.Unlock()
	for _, h := range all {
		h.invoke(func(c api.Callbacks) {
			if c.Close != nil {
				c.Close()
			}
		})
	}
	for _, h := range all {
		h.Close()
	}
	w.native.Close()
	close(w.done)
}

// Clipboard operations now use the native event domain, not an arbitrary
// PTY's lock. Never wait here while holding a terminal lock. Replies reacquire
// the requesting widget's lock even when a backend answers synchronously.
func (w *Window) post(operation func()) bool {
	if w.closed.Load() {
		return false
	}
	select {
	case w.operations <- operation:
		return true
	default:
		return false
	}
}

func (w *Window) operate() {
	for {
		select {
		case <-w.done:
			return
		case operation := <-w.operations:
			w.eventMu.Lock()
			if !w.closed.Load() {
				operation()
			}
			w.eventMu.Unlock()
		}
	}
}

func (w *Window) upload(pixels []uint32, stride int, r image.Rectangle, at image.Point) {
	delta := at.Sub(r.Min)
	destination := r.Add(delta).Intersect(image.Rect(0, 0, w.width, w.height))
	r, at = destination.Sub(delta), destination.Min
	if r.Empty() {
		return
	}
	if painter, ok := w.native.(api.PositionedPainter); ok {
		painter.UploadAt(pixels, stride, r, at)
		return
	}
	if at == r.Min {
		w.native.Upload(pixels, stride, r.Min.X, r.Min.Y, r.Dx(), r.Dy())
		return
	}
	// Legacy test/embedding hosts can still accept translated rectangles. Real
	// backends implement UploadAt, so no window-sized scratch is needed there.
	destination = r.Sub(r.Min).Add(at)
	if destination.Empty() {
		return
	}
	var scratch Buffer
	if !scratch.Resize(destination.Max.X, destination.Max.Y) {
		return
	}
	scratch.Copy(pixels, stride, r, at)
	w.native.Upload(scratch.Pixels, scratch.Width, destination.Min.X, destination.Min.Y, destination.Dx(), destination.Dy())
}

// Unwrap returns the native host for backend-specific diagnostics only.
func Unwrap(host api.Host) api.Host {
	for {
		scoped, ok := host.(interface{ NativeHost() api.Host })
		if !ok {
			return host
		}
		host = scoped.NativeHost()
	}
}
