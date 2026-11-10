// Package widgets provides buffered, rectangular hosts without tab or tiling
// policy. Native resources remain in the display backend.
package widgets

import (
	"image"

	"st-go/limits"
)

// Buffer is privately owned by a widget. Its owner serializes pixel access;
// glyph rasters may be shared, but terminal images and PDF state are not.
type Buffer struct {
	Width, Height int
	Pixels        []uint32
}

func (b *Buffer) Resize(width, height int) bool {
	size, ok := limits.RGBA("widget buffer", width, height)
	if !ok {
		return false
	}
	if b.Width == width && b.Height == height && len(b.Pixels) == size/4 {
		return true
	}
	pixels := make([]uint32, size/4)
	for y := 0; y < min(height, b.Height); y++ {
		copy(pixels[y*width:y*width+min(width, b.Width)], b.Pixels[y*b.Width:y*b.Width+min(width, b.Width)])
	}
	b.Width, b.Height, b.Pixels = width, height, pixels
	return true
}

func (b *Buffer) Bounds() image.Rectangle { return image.Rect(0, 0, b.Width, b.Height) }

// Copy clips source and destination together, never confusing a translated
// destination coordinate with a source-buffer index.
func (b *Buffer) Copy(pixels []uint32, stride int, source image.Rectangle, destination image.Point) image.Rectangle {
	if stride <= 0 || len(pixels) < stride {
		return image.Rectangle{}
	}
	delta := destination.Sub(source.Min)
	source = source.Intersect(image.Rect(0, 0, stride, len(pixels)/stride))
	r := source.Add(delta).Intersect(b.Bounds())
	for y := r.Min.Y; y < r.Max.Y; y++ {
		start := (y-delta.Y)*stride + r.Min.X - delta.X
		copy(b.Pixels[y*b.Width+r.Min.X:y*b.Width+r.Max.X], pixels[start:start+r.Dx()])
	}
	return r
}
