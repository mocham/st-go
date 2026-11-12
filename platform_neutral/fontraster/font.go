// Package fontraster provides bounded font fallback and cell composition without
// a display connection. Rasterizers supply immutable grayscale masks.
package fontraster

import (
	"fmt"
	"image"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"st-go/limits"
)

// Reserve a quarter of the terminal glyph budget for color-independent masks.
// The frontend uses the remainder for ready-to-blit colored cells.
const MaskCacheBytes = limits.GlyphCacheBytes / 4
const MaskCacheEntries = limits.GlyphCacheEntries / 4

type Glyph struct {
	Mask *image.Alpha // Owned, immutable, tightly packed, with a zero origin.
	Top  int          // Bitmap top above the baseline, in pixels.
}

// Face calls are serialized by Set. A successful empty glyph (e.g. space) is
// different from a missing character. Glyph must not return borrowed storage.
type Face interface {
	Glyph(rune) (glyph Glyph, found bool, err error)
	Close() error
}

type Loader func(data []byte, pixelHeight int) (Face, error)

// Set owns faces and their mask cache. It may be shared by terminal widgets.
type Set struct {
	mu    sync.Mutex
	faces []Face
	cache map[rune]Glyph
	bytes int
}

// Load retains the requested primary, embedded text fallback, and optional icon
// and CJK faces in that order. File inputs are regular files with bounded reads.
func Load(loader Loader, primary string, embedded []byte, pixelHeight int) (*Set, error) {
	if _, ok := limits.RGBA("font size", pixelHeight, pixelHeight); !ok {
		return nil, fmt.Errorf("invalid font size")
	}
	s := &Set{cache: make(map[rune]Glyph)}
	total := 0
	add := func(data []byte) {
		if len(data) == 0 || len(data) > limits.FileBytes-total {
			return
		}
		face, err := loader(data, pixelHeight)
		if err == nil {
			s.faces = append(s.faces, face)
			total += len(data)
		}
	}
	addFile := func(name string) {
		for _, path := range fontPaths(name) {
			data, err := limits.ReadFile(path)
			if err == nil {
				add(data)
				return
			}
		}
	}
	if primary != "" {
		addFile(primary)
	}
	add(embedded)
	for _, name := range []string{"fontawesome-solid.ttf", "msyh.ttc"} {
		addFile(name)
	}
	if len(s.faces) == 0 {
		return nil, fmt.Errorf("no usable font (primary %q)", primary)
	}
	return s, nil
}

func fontPaths(name string) []string {
	if filepath.IsAbs(name) || strings.ContainsRune(name, filepath.Separator) {
		return []string{name}
	}
	var paths []string
	if home, err := os.UserHomeDir(); err == nil {
		paths = append(paths, filepath.Join(home, ".local/share/fonts", name))
	}
	return append(paths, name)
}

func (s *Set) Close() error {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	var first error
	for _, face := range s.faces {
		if err := face.Close(); first == nil {
			first = err
		}
	}
	s.faces, s.cache, s.bytes = nil, nil, 0
	return first
}

func (s *Set) Glyph(r rune) Glyph {
	if s == nil || r < 0x20 || r == 0x7f {
		return Glyph{}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if glyph, ok := s.cache[r]; ok {
		return glyph
	}
	var glyph Glyph
	for _, face := range s.faces {
		candidate, found, err := face.Glyph(r)
		if err != nil {
			limits.Report("font", "glyph %U: %v", r, err)
			continue
		}
		if found {
			glyph = candidate
			break
		}
	}
	size := 0
	if glyph.Mask != nil {
		size = len(glyph.Mask.Pix)
	}
	if s.cache != nil && size <= MaskCacheBytes {
		if s.bytes+size > MaskCacheBytes || len(s.cache) >= MaskCacheEntries {
			clear(s.cache)
			s.bytes = 0
		}
		s.cache[r], s.bytes = glyph, s.bytes+size
	}
	return glyph
}

// Compose preserves the terminal's ink-centered placement and explicit baseline.
// Character advance never determines cell width. Clipping includes glyphs above
// the baseline/cell, oversized ink, and two-column cells.
func Compose(glyph Glyph, fg, bg uint32, width, height, baseline int) []uint32 {
	size, ok := limits.RGBA("glyph cell", width, height)
	if !ok {
		return nil
	}
	pixels := make([]uint32, size/4)
	for i := range pixels {
		pixels[i] = bg
	}
	mask := glyph.Mask
	if mask == nil || mask.Rect.Empty() {
		return pixels
	}
	x, y := max(0, (width-mask.Rect.Dx())/2), baseline-glyph.Top
	clip := image.Rect(x, y, x+mask.Rect.Dx(), y+mask.Rect.Dy()).Intersect(image.Rect(0, 0, width, height))
	for row := clip.Min.Y; row < clip.Max.Y; row++ {
		source := (row-y)*mask.Stride + clip.Min.X - x
		for col := clip.Min.X; col < clip.Max.X; col++ {
			pixels[row*width+col] = Blend(mask.Pix[source+col-clip.Min.X], fg, bg)
		}
	}
	return pixels
}

func Blend(alpha byte, fg, bg uint32) uint32 {
	if alpha == 0 {
		return bg
	}
	if alpha == 255 {
		return fg
	}
	a, pixel := uint32(alpha), uint32(0xff000000)
	for shift := uint(0); shift <= 16; shift += 8 {
		f, b := fg>>shift&255, bg>>shift&255
		pixel |= ((f*a + b*(255-a) + 127) / 255) << shift
	}
	return pixel
}
