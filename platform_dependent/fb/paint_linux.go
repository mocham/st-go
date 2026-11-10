//go:build linux

package fb

import (
	"fmt"
	"image"

	"st-go/limits"
	"st-go/platform_neutral/api"
)

// Caller holds d.mu. Compose retained panes and the software pointer only in
// scanout, never into the terminal's stored pixels or image/cell metadata.
func (d *Driver) paintRect(r image.Rectangle) {
	if d.closed || !d.active {
		return
	}
	l := d.screen.layout
	r = r.Intersect(image.Rect(0, 0, l.width, l.height))
	for y := r.Min.Y; y < r.Max.Y; y++ {
		for x := r.Min.X; x < r.Max.X; x++ {
			pixel := d.pixels[y*l.width+x]
			for _, s := range d.surfaces {
				if image.Pt(x, y).In(s.bounds) {
					pixel = s.pixels[(y-s.bounds.Min.Y)*s.bounds.Dx()+x-s.bounds.Min.X]
				}
			}
			if d.pointerVisible {
				if value, ok := pointerPixel(x-d.pointer.X, y-d.pointer.Y); ok {
					pixel = value
				}
			}
			l.pixel(d.screen.memory, x, y, pixel)
		}
	}
}
func pointerPixel(x, y int) (uint32, bool) {
	if x < 0 || y < 0 || y >= 16 || x > y/2 || x >= 9 {
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
	d.pointer, d.pointerVisible = p, visible
	if oldVisible {
		d.paintRect(image.Rectangle{Min: old, Max: old.Add(image.Pt(9, 16))})
	}
	if visible {
		d.paintRect(image.Rectangle{Min: p, Max: p.Add(image.Pt(9, 16))})
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
	d.paintRect(r)
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
	d.paintRect(bounds)
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
	d.paintRect(old)
	d.paintRect(bounds)
}
func (s *surface) Present(pixels []uint32, w, h, scrollRows int, reuse bool) {
	d := s.driver
	d.mu.Lock()
	defer d.mu.Unlock()
	if s.closed || d.closed || w != s.bounds.Dx() || h != s.bounds.Dy() || len(pixels) != len(s.pixels) {
		return
	}
	copy(s.pixels, pixels)
	d.paintRect(s.bounds)
}
func (s *surface) Expose() bool {
	d := s.driver
	d.mu.Lock()
	defer d.mu.Unlock()
	if s.closed || d.closed {
		return false
	}
	d.paintRect(s.bounds)
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
	d.paintRect(s.bounds)
}
