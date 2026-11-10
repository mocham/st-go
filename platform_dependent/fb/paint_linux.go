//go:build linux

package fb

import (
	"fmt"
	"image"

	"st-go/limits"
	"st-go/platform_neutral/api"
)

const (
	pointerScale  = 2
	pointerWidth  = 9 * pointerScale
	pointerHeight = 16 * pointerScale
)

func pointerBounds(p image.Point) image.Rectangle {
	return image.Rectangle{Min: p, Max: p.Add(image.Pt(pointerWidth, pointerHeight))}
}

func (d *Driver) Show() {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.systemBar != nil {
		d.invalidate(d.systemBar.bounds)
	}
}

// Caller holds d.mu. Compose a complete damaged rectangle in ordinary RAM,
// never directly into visible scanout or the retained glyph/preview buffers.
func (d *Driver) composeRect(memory []byte, r image.Rectangle) {
	l := d.screen.layout
	r = r.Intersect(image.Rect(0, 0, l.width, l.height))
	// Content is copied in row spans. Layer intersection is determined once
	// per rectangle, not once for every glyph/background pixel.
	// A full opaque PDF/list update need not redraw the terminal behind it.
	// Start at the highest pane covering this entire rectangle, if any.
	first := -1
	for i := len(d.surfaces) - 1; i >= 0; i-- {
		if !d.surfaces[i].hidden && d.surfaces[i].kind != api.BarWidget && r.In(d.surfaces[i].bounds) {
			first = i
			break
		}
	}
	if first < 0 {
		l.blit(memory, r, image.Point{}, d.pixels, l.width)
		first = 0
	}
	for _, s := range d.surfaces[first:] {
		if s.hidden || s.kind == api.BarWidget {
			continue
		}
		l.blit(memory, r.Intersect(s.bounds), s.bounds.Min, s.pixels, s.bounds.Dx())
	}
	c := d.textCursor
	l.blit(memory, r.Intersect(c.Bounds), c.Bounds.Min, c.Pixels, c.Bounds.Dx())
	if b := d.systemBar; b != nil && b.widget == nil {
		l.blit(memory, r.Intersect(b.bounds), b.bounds.Min, b.pixels, b.bounds.Dx())
	}
	for _, s := range d.surfaces {
		if !s.hidden && s.kind == api.BarWidget {
			l.blit(memory, r.Intersect(s.bounds), s.bounds.Min, s.pixels, s.bounds.Dx())
		}
	}
	if d.pointerVisible {
		clip := r.Intersect(pointerBounds(d.pointer))
		for y := clip.Min.Y; y < clip.Max.Y; y++ {
			for x := clip.Min.X; x < clip.Max.X; x++ {
				if pixel, ok := pointerPixel(x-d.pointer.X, y-d.pointer.Y); ok {
					l.pixel(memory, x, y, pixel)
				}
			}
		}
	}
}

func (d *Driver) SetTextCursor(cursor api.TextCursor) {
	if !cursor.Valid() {
		cursor = api.TextCursor{}
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.closed || d.textCursor.Equal(cursor) {
		return
	}
	old := d.textCursor.Bounds
	d.textCursor.Bounds = cursor.Bounds
	d.textCursor.Pixels = append(d.textCursor.Pixels[:0], cursor.Pixels...)
	d.textCursor.Base = append(d.textCursor.Base[:0], cursor.Base...)
	d.invalidate(old)
	d.invalidate(cursor.Bounds)
}
func pointerPixel(x, y int) (uint32, bool) {
	if x < 0 || y < 0 || x >= pointerWidth || y >= pointerHeight {
		return 0, false
	}
	x, y = x/pointerScale, y/pointerScale
	if x > y/2 {
		return 0, false
	}
	if x == 0 || x == y/2 || y == 15 {
		return 0xff000000, true
	}
	return 0xffffffff, true
}
func (d *Driver) movePointer(p image.Point, visible bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	l := d.screen.layout
	p.X = max(0, min(p.X, l.width-1))
	p.Y = max(0, min(p.Y, l.height-1))
	old, oldVisible := d.pointer, d.pointerVisible
	if old == p && oldVisible == visible {
		return
	}
	d.pointer, d.pointerVisible = p, visible
	if oldVisible {
		d.invalidate(pointerBounds(old))
	}
	if visible {
		d.invalidate(pointerBounds(p))
	}
}
func (d *Driver) Upload(pixels []uint32, stride, x, y, w, h int) {
	d.UploadAt(pixels, stride, image.Rect(x, y, x+w, y+h), image.Pt(x, y))
}

func (d *Driver) UploadAt(pixels []uint32, stride int, source image.Rectangle, destination image.Point) {
	if stride <= 0 || source.Empty() {
		return
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.closed {
		return
	}
	l := d.screen.layout
	delta := destination.Sub(source.Min)
	source = source.Intersect(image.Rect(0, 0, stride, len(pixels)/stride))
	r := source.Add(delta).Intersect(image.Rect(0, 0, l.width, l.height))
	if r.Empty() {
		return
	}
	for row := r.Min.Y; row < r.Max.Y; row++ {
		at := (row-delta.Y)*stride + r.Min.X - delta.X
		copy(d.pixels[row*l.width+r.Min.X:row*l.width+r.Max.X], pixels[at:at+r.Dx()])
	}
	d.invalidate(r)
}

type surface struct {
	driver *Driver
	bounds image.Rectangle
	pixels []uint32
	closed bool
	hidden bool
	kind   api.WidgetKind
}

func (d *Driver) NewSurface(bounds image.Rectangle, background uint32) (api.Surface, error) {
	return d.NewWidgetSurface(api.TerminalWidget, bounds, background)
}
func (d *Driver) NewWidgetSurface(kind api.WidgetKind, bounds image.Rectangle, background uint32) (api.Surface, error) {
	if _, ok := limits.RGBA("fb surface", bounds.Dx(), bounds.Dy()); !ok {
		return nil, fmt.Errorf("invalid surface bounds")
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	previews := 0
	for _, surface := range d.surfaces {
		if surface.kind == api.TerminalWidget {
			previews++
		}
	}
	if d.closed || (kind == api.TerminalWidget && previews >= 8) {
		return nil, fmt.Errorf("framebuffer closed or surface limit reached")
	}
	s := &surface{driver: d, bounds: bounds, kind: kind}
	s.fill(background)
	d.surfaces = append(d.surfaces, s)
	d.invalidate(bounds)
	return s, nil
}
func (s *surface) fill(background uint32) {
	s.pixels = make([]uint32, s.bounds.Dx()*s.bounds.Dy())
	for i := range s.pixels {
		s.pixels[i] = background
	}
}
func (s *surface) SetVisible(visible bool) {
	d := s.driver
	d.mu.Lock()
	defer d.mu.Unlock()
	if !s.closed && s.hidden == visible {
		s.hidden = !visible
		d.invalidate(s.bounds)
	}
}
func (s *surface) Configure(bounds image.Rectangle, background uint32) {
	if _, ok := limits.RGBA("fb surface", bounds.Dx(), bounds.Dy()); !ok {
		return
	}
	d := s.driver
	d.mu.Lock()
	defer d.mu.Unlock()
	if s.closed || d.closed {
		return
	}
	old := s.bounds
	s.bounds = bounds
	if old.Size() != bounds.Size() {
		s.fill(background)
	}
	d.invalidate(old)
	d.invalidate(bounds)
}
func (s *surface) Present(pixels []uint32, w, h, scrollRows int, reuse bool) {
	d := s.driver
	d.mu.Lock()
	defer d.mu.Unlock()
	if s.closed || d.closed || w != s.bounds.Dx() || h != s.bounds.Dy() || len(pixels) != len(s.pixels) {
		return
	}
	copy(s.pixels, pixels)
	d.invalidate(s.bounds)
}
func (s *surface) Expose() bool {
	d := s.driver
	d.mu.Lock()
	defer d.mu.Unlock()
	if s.closed || d.closed {
		return false
	}
	d.invalidate(s.bounds)
	return true
}
func (s *surface) Close() {
	d := s.driver
	d.mu.Lock()
	defer d.mu.Unlock()
	if s.closed {
		return
	}
	s.closed = true
	s.pixels = nil
	for i, view := range d.surfaces {
		if view == s {
			d.surfaces = append(d.surfaces[:i], d.surfaces[i+1:]...)
			break
		}
	}
	d.invalidate(s.bounds)
}
