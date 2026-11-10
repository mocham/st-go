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
		if r.In(d.surfaces[i].bounds) {
			first = i
			break
		}
	}
	if first < 0 {
		l.blit(memory, r, image.Point{}, d.pixels, l.width)
		first = 0
	}
	for _, s := range d.surfaces[first:] {
		l.blit(memory, r.Intersect(s.bounds), s.bounds.Min, s.pixels, s.bounds.Dx())
	}
	c := d.textCursor
	l.blit(memory, r.Intersect(c.Bounds), c.Bounds.Min, c.Pixels, c.Bounds.Dx())
	if b := d.systemBar; b != nil {
		l.blit(memory, r.Intersect(b.bounds), b.bounds.Min, b.pixels, b.bounds.Dx())
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
	if stride <= 0 || w <= 0 || h <= 0 {
		return
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.closed {
		return
	}
	l := d.screen.layout
	r := image.Rect(x, y, x+w, y+h).Intersect(image.Rect(0, 0, l.width, l.height))
	if r.Empty() || stride < r.Max.X || r.Max.Y > len(pixels)/stride {
		return
	}
	for row := r.Min.Y; row < r.Max.Y; row++ {
		copy(d.pixels[row*l.width+r.Min.X:row*l.width+r.Max.X], pixels[row*stride+r.Min.X:row*stride+r.Max.X])
	}
	d.invalidate(r)
}

type surface struct {
	driver *Driver
	bounds image.Rectangle
	pixels []uint32
	closed bool
}

func (d *Driver) NewSurface(bounds image.Rectangle, background uint32) (api.Surface, error) {
	if _, ok := limits.RGBA("fb surface", bounds.Dx(), bounds.Dy()); !ok {
		return nil, fmt.Errorf("invalid surface bounds")
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.closed || len(d.surfaces) >= 8 {
		return nil, fmt.Errorf("framebuffer closed or surface limit reached")
	}
	s := &surface{driver: d, bounds: bounds}
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
	s.fill(background)
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
