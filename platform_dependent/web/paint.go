package web

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"image"
	"image/png"

	"st-go/limits"
	"st-go/platform_neutral/api"
)

func (d *Driver) notify() {
	select {
	case d.wake <- struct{}{}:
	default:
	}
}

// Caller holds d.mu. Keep separate damage spans rather than bridging clean rows.
func (d *Driver) damage(r image.Rectangle) {
	r = r.Intersect(image.Rect(0, 0, d.width, d.height))
	if r.Empty() {
		return
	}
	merged := false
	for i, old := range d.dirty {
		union := old.Union(r)
		overlap := old.Intersect(r)
		if union.Dx()*union.Dy() == old.Dx()*old.Dy()+r.Dx()*r.Dy()-overlap.Dx()*overlap.Dy() {
			d.dirty[i] = union
			merged = true
			break
		}
	}
	if !merged {
		d.dirty = append(d.dirty, r)
	}
	// Near-identical overlapping rectangles can otherwise multiply snapshot
	// memory even with a bounded rectangle count. At most one screen of pixels
	// is retained in a pending batch; a full refresh is the bounded fallback.
	pixels := 0
	for _, rect := range d.dirty {
		pixels += rect.Dx() * rect.Dy()
	}
	if len(d.dirty) > 128 || pixels > d.width*d.height {
		d.dirty = []image.Rectangle{image.Rect(0, 0, d.width, d.height)}
	}
	d.notify()
}

func (d *Driver) Upload(pixels []uint32, stride, x, y, w, h int) {
	if w <= 0 || h <= 0 || stride <= 0 {
		return
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	r := image.Rect(x, y, x+w, y+h).Intersect(image.Rect(0, 0, d.width, d.height))
	if r.Empty() || stride < r.Max.X || r.Max.Y > len(pixels)/max(1, stride) {
		return
	}
	for row := r.Min.Y; row < r.Max.Y; row++ {
		copy(d.pixels[row*d.width+r.Min.X:row*d.width+r.Max.X], pixels[row*stride+r.Min.X:row*stride+r.Max.X])
	}
	d.damage(r)
}

type surface struct {
	driver *Driver
	bounds image.Rectangle
	pixels []uint32
	closed bool
}

func (d *Driver) NewSurface(bounds image.Rectangle, background uint32) (api.Surface, error) {
	if _, ok := limits.RGBA("web surface", bounds.Dx(), bounds.Dy()); !ok {
		return nil, fmt.Errorf("invalid surface dimensions")
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if len(d.surfaces) >= 8 {
		return nil, fmt.Errorf("web surface limit reached")
	}
	s := &surface{driver: d, bounds: bounds}
	s.fill(background)
	d.surfaces = append(d.surfaces, s)
	d.damage(bounds)
	return s, nil
}

func (s *surface) fill(background uint32) {
	s.pixels = make([]uint32, s.bounds.Dx()*s.bounds.Dy())
	for i := range s.pixels {
		s.pixels[i] = background
	}
}
func (s *surface) Configure(bounds image.Rectangle, background uint32) {
	if _, ok := limits.RGBA("web surface", bounds.Dx(), bounds.Dy()); !ok {
		return
	}
	d := s.driver
	d.mu.Lock()
	defer d.mu.Unlock()
	if s.closed {
		return
	}
	old := s.bounds
	s.bounds = bounds
	s.fill(background)
	d.damage(old)
	d.damage(bounds)
}
func (s *surface) Present(pixels []uint32, w, h, scrollRows int, reuse bool) {
	d := s.driver
	d.mu.Lock()
	defer d.mu.Unlock()
	if s.closed || w != s.bounds.Dx() || h != s.bounds.Dy() || len(pixels) != len(s.pixels) {
		return
	}
	copy(s.pixels, pixels)
	d.damage(s.bounds)
}
func (s *surface) Expose() bool {
	d := s.driver
	d.mu.Lock()
	defer d.mu.Unlock()
	if s.closed {
		return false
	}
	d.damage(s.bounds)
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
	for i, candidate := range d.surfaces {
		if candidate == s {
			d.surfaces = append(d.surfaces[:i], d.surfaces[i+1:]...)
			break
		}
	}
	d.damage(s.bounds)
}

type frame struct {
	width, height int
	rect          image.Rectangle
	pixels        []uint32
}

// Snapshots copy pixels under the driver lock; PNG encoding/network I/O never
// hold the terminal lock. Overlay retained panes even on full terminal erases.
func (d *Driver) frames() []frame {
	d.mu.Lock()
	defer d.mu.Unlock()
	frames := make([]frame, 0, len(d.dirty))
	for _, r := range d.dirty {
		f := frame{d.width, d.height, r, make([]uint32, r.Dx()*r.Dy())}
		for y := r.Min.Y; y < r.Max.Y; y++ {
			copy(f.pixels[(y-r.Min.Y)*r.Dx():], d.pixels[y*d.width+r.Min.X:y*d.width+r.Max.X])
		}
		for _, s := range d.surfaces {
			clip := s.bounds.Intersect(r)
			for y := clip.Min.Y; y < clip.Max.Y; y++ {
				at := (y-r.Min.Y)*r.Dx() + clip.Min.X - r.Min.X
				from := (y-s.bounds.Min.Y)*s.bounds.Dx() + clip.Min.X - s.bounds.Min.X
				copy(f.pixels[at:at+clip.Dx()], s.pixels[from:from+clip.Dx()])
			}
		}
		frames = append(frames, f)
	}
	d.dirty = nil
	return frames
}

func encodeFrame(f frame) ([]byte, error) {
	var out bytes.Buffer
	var header [28]byte
	copy(header[:4], "STW1")
	for i, v := range []int{f.width, f.height, f.rect.Min.X, f.rect.Min.Y, f.rect.Dx(), f.rect.Dy()} {
		binary.LittleEndian.PutUint32(header[4+i*4:], uint32(v))
	}
	out.Write(header[:])
	rgba := image.NewRGBA(image.Rect(0, 0, f.rect.Dx(), f.rect.Dy()))
	for i, pixel := range f.pixels {
		rgba.Pix[i*4] = byte(pixel >> 16)
		rgba.Pix[i*4+1] = byte(pixel >> 8)
		rgba.Pix[i*4+2] = byte(pixel)
		rgba.Pix[i*4+3] = 255
	}
	encoder := png.Encoder{CompressionLevel: png.BestSpeed}
	if err := encoder.Encode(&out, rgba); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}
