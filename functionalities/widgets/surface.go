package widgets

import (
	"fmt"
	"image"

	"st-go/platform_neutral/api"
)

// surface keeps preview geometry local to its terminal widget. The same paint
// lock guards uploads, visibility, close, and native surface reconfiguration.
type surface struct {
	owner          *Host
	native         api.Surface
	bounds         image.Rectangle
	background     uint32
	hidden, closed bool
	nativeBounds   image.Rectangle
	committed      Buffer
}

func (h *Host) NewSurface(bounds image.Rectangle, background uint32) (api.Surface, error) {
	w := h.window
	w.paintMu.Lock()
	defer w.paintMu.Unlock()
	if h.isClosed() || bounds.Empty() {
		return nil, fmt.Errorf("invalid widget surface")
	}
	s := &surface{owner: h, bounds: bounds, background: background}
	r := s.windowBounds()
	if r.Empty() {
		return nil, fmt.Errorf("surface outside widget")
	}
	visible := h.Visible()
	var native api.Surface
	var err error
	if factory, ok := w.native.(api.WidgetSurfaceFactory); ok {
		native, err = factory.NewWidgetSurfaceVisible(api.TerminalWidget, r, background, visible)
	} else {
		native, err = w.native.NewSurface(r, background)
	}
	if err != nil {
		return nil, err
	}
	s.native = native
	s.nativeBounds = r
	w.mu.Lock()
	w.surfaces[native] = s
	w.mu.Unlock()
	s.setVisibleLocked(visible)
	return s, nil
}

func (s *surface) windowBounds() image.Rectangle {
	h := s.owner
	w := h.window
	w.mu.Lock()
	bounds := h.options.Bounds
	width, height := w.width, w.height
	w.mu.Unlock()
	return s.bounds.Add(bounds.Min).Intersect(bounds).Intersect(image.Rect(0, 0, width, height))
}
func (s *surface) Configure(bounds image.Rectangle, background uint32) {
	w := s.owner.window
	w.paintMu.Lock()
	defer w.paintMu.Unlock()
	if s.closed {
		return
	}
	s.bounds, s.background = bounds, background
	s.configureLocked()
}
func (s *surface) configureLocked() {
	if len(s.committed.Pixels) > 0 {
		s.committed.Resize(s.bounds.Dx(), s.bounds.Dy())
	}
	r := s.windowBounds()
	if r.Empty() {
		s.setVisibleLocked(false)
		return
	}
	s.nativeBounds = r
	s.native.Configure(r, s.background)
	s.setVisibleLocked(s.owner.Visible())
}
func (s *surface) setVisibleLocked(visible bool) {
	s.hidden = !visible
	if native, ok := s.native.(api.SurfaceVisibility); ok {
		native.SetVisible(visible)
	}
	if visible {
		s.restoreLocked()
	}
}
func (s *surface) Present(pixels []uint32, width, height, scrollRows int, reuse bool) {
	w := s.owner.window
	w.paintMu.Lock()
	defer w.paintMu.Unlock()
	s.presentLocked(pixels, width, height, scrollRows, reuse)
}

func (s *surface) PresentHTML(generation uint64, view api.HTMLView) bool {
	w := s.owner.window
	w.paintMu.Lock()
	defer w.paintMu.Unlock()
	current, visible := s.owner.PresentationState()
	if s.closed || s.hidden || !visible || current != generation {
		return false
	}
	native, ok := s.native.(api.HTMLSurface)
	if !ok {
		return false
	}
	if view.Document != nil {
		s.committed = Buffer{}
	}
	return native.PresentHTML(0, view)
}

func (s *surface) PresentIfCurrent(generation uint64, pixels []uint32, width, height, scrollRows int, reuse bool) bool {
	w := s.owner.window
	w.paintMu.Lock()
	defer w.paintMu.Unlock()
	current, visible := s.owner.PresentationState()
	if !visible || current != generation {
		return false
	}
	return s.presentLocked(pixels, width, height, scrollRows, reuse)
}

func (s *surface) presentLocked(pixels []uint32, width, height, scrollRows int, reuse bool) bool {
	if s.closed || s.hidden || !s.owner.Visible() || width != s.bounds.Dx() || height != s.bounds.Dy() || width <= 0 || height <= 0 || len(pixels) != width*height {
		return false
	}
	r := s.windowBounds()
	if r.Empty() {
		return false
	}
	if !s.committed.Resize(width, height) {
		return false
	}
	s.committed.Copy(pixels, width, image.Rect(0, 0, width, height), image.Point{})
	if r.Dx() == width && r.Dy() == height {
		s.native.Present(pixels, width, height, scrollRows, reuse)
		return true
	}
	origin := s.owner.Bounds().Min
	source := r.Sub(origin).Sub(s.bounds.Min)
	var clipped Buffer
	if !clipped.Resize(r.Dx(), r.Dy()) {
		return false
	}
	clipped.Copy(pixels, width, source, image.Point{})
	s.native.Present(clipped.Pixels, r.Dx(), r.Dy(), 0, false)
	return true
}

func (s *surface) restoreLocked() {
	if s.closed || s.hidden || len(s.committed.Pixels) == 0 {
		return
	}
	r := s.windowBounds()
	if r.Empty() {
		return
	}
	source := r.Sub(s.owner.Bounds().Min).Sub(s.bounds.Min)
	var clipped Buffer
	if !clipped.Resize(r.Dx(), r.Dy()) {
		return
	}
	clipped.Copy(s.committed.Pixels, s.committed.Width, source, image.Point{})
	s.native.Present(clipped.Pixels, clipped.Width, clipped.Height, 0, false)
}
func (s *surface) Expose() bool {
	w := s.owner.window
	w.paintMu.Lock()
	defer w.paintMu.Unlock()
	return !s.closed && !s.hidden && s.owner.Visible() && s.native.Expose()
}
func (s *surface) Close() {
	w := s.owner.window
	w.paintMu.Lock()
	defer w.paintMu.Unlock()
	s.closeLocked()
}
func (s *surface) closeLocked() {
	if s.closed {
		return
	}
	s.closed = true
	s.committed = Buffer{}
	w := s.owner.window
	w.mu.Lock()
	delete(w.surfaces, s.native)
	w.mu.Unlock()
	s.native.Close()
}
