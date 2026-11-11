package web

import (
	"encoding/binary"
	"image"

	"st-go/limits"
)

// Cache the final, opaque FreeType cell rasters rather than asking the browser
// to reproduce font rendering. Pixel identity includes colors, selection,
// cursor/overlay composition and font changes. Wide glyphs use two cells.
// Only repeated cells are admitted, so one-off image tiles remain PNG damage.
// This cache belongs to a socket writer, never to the terminal paint lock.
type glyphEncoder struct {
	cellWidth, cellHeight, border int
	maxBytes, maxEntries          int
	bytes                         int
	entries                       map[glyphFingerprint]cachedGlyph
	seen                          map[glyphFingerprint]image.Point
}

type glyphFingerprint struct {
	width, height int
	hash          uint64
}

type cachedGlyph struct {
	id     uint32
	pixels []uint32
}

const (
	glyphReset = 0
	glyphDraw  = 2
	glyphAtlas = 3

	// Bound cold-cache work per presentation, regardless of how many colors or
	// image tiles appear. A warmed screen needs no atlas encoding at all.
	maxGlyphDefinitions = 256
	maxFallbackPNGs     = 4
	maxAtlasSide        = 4096
)

func newGlyphEncoder(width, height, border int) *glyphEncoder {
	return &glyphEncoder{cellWidth: width, cellHeight: height, border: border,
		maxBytes: limits.GlyphCacheBytes, maxEntries: limits.GlyphCacheEntries,
		entries: make(map[glyphFingerprint]cachedGlyph), seen: make(map[glyphFingerprint]image.Point)}
}

// Hashing is only an index: every hit is checked against the actual pixels.
// Alpha is normalized because the existing PNG transport is always opaque.
func fingerprint(f frame, r image.Rectangle) glyphFingerprint {
	hash := uint64(14695981039346656037)
	for y := r.Min.Y; y < r.Max.Y; y++ {
		at := (y-f.rect.Min.Y)*f.rect.Dx() + r.Min.X - f.rect.Min.X
		for _, pixel := range f.pixels[at : at+r.Dx()] {
			hash = (hash ^ uint64(pixel|0xff000000)) * 1099511628211
		}
	}
	return glyphFingerprint{r.Dx(), r.Dy(), hash}
}

func sameGlyph(f frame, r image.Rectangle, pixels []uint32) bool {
	for y := r.Min.Y; y < r.Max.Y; y++ {
		at := (y-f.rect.Min.Y)*f.rect.Dx() + r.Min.X - f.rect.Min.X
		to := (y - r.Min.Y) * r.Dx()
		for x, pixel := range f.pixels[at : at+r.Dx()] {
			if pixel|0xff000000 != pixels[to+x]|0xff000000 {
				return false
			}
		}
	}
	return true
}

func cropFrame(f frame, r image.Rectangle) frame {
	patch := frame{f.width, f.height, r, make([]uint32, r.Dx()*r.Dy())}
	for y := r.Min.Y; y < r.Max.Y; y++ {
		at := (y-f.rect.Min.Y)*f.rect.Dx() + r.Min.X - f.rect.Min.X
		copy(patch.pixels[(y-r.Min.Y)*r.Dx():], f.pixels[at:at+r.Dx()])
	}
	return patch
}

func appendWords(data []byte, values ...uint32) []byte {
	for _, value := range values {
		data = binary.LittleEndian.AppendUint32(data, value)
	}
	return data
}

// STG1: magic, canvas width/height, operation count (all integers uint32 LE).
// Operations: reset(0); draw(2,id,x,y,count);
// atlas(3,first ID,count,cell width,cell height,columns,PNG length,PNG).
// Atlas IDs are consecutive, with cells in row-major order. One PNG decode
// installs all new glyphs in the group; draws reference rectangles in it.
// Definitions/resets are processed even for obsolete viewport sizes. They and
// their draws share wire order, including cache resets in the middle of a batch.
func (e *glyphEncoder) encode(f frame) ([][]byte, error) {
	budget := maxGlyphDefinitions
	return e.encodeWithBudget(f, &budget)
}

func atlasSize(count, cellWidth int) (columns, rows int) {
	columns = min(16, count, maxAtlasSide/cellWidth)
	if columns != 0 {
		rows = (count + columns - 1) / columns
	}
	return
}

func (e *glyphEncoder) encodeWithBudget(f frame, budget *int) ([][]byte, error) {
	w, h := e.cellWidth, e.cellHeight
	if w < 1 || h < 1 || w > maxAtlasSide || h > maxAtlasSide || w*h*4 > e.maxBytes {
		data, err := encodeFrame(f)
		return [][]byte{data}, err
	}
	// Only whole cells are eligible. Partial-cell cursor damage and borders
	// keep the existing PNG path; the grid anchor is stable across row spans.
	x0 := e.border + (max(0, f.rect.Min.X-e.border)+w-1)/w*w
	y0 := e.border + (max(0, f.rect.Min.Y-e.border)+h-1)/h*h
	x1 := x0 + max(0, (f.rect.Max.X-x0)/w)*w
	y1 := y0 + max(0, (f.rect.Max.Y-y0)/h)*h
	// Very small cells or oversized borders must not turn one bounded pixel
	// snapshot into millions of draw commands and PNG definitions.
	if x0 >= x1 || y0 >= y1 || (x1-x0)/w*((y1-y0)/h) > 4*limits.GlyphCacheEntries {
		data, err := encodeFrame(f)
		return [][]byte{data}, err
	}
	data := appendWords([]byte("STG1"), uint32(f.width), uint32(f.height), 0)
	var operations uint32
	var draws []byte
	var drawOperations uint32
	var pending []cachedGlyph
	lastDraw := -1
	flushGlyphs := func() error {
		if len(pending) != 0 {
			cols, rows := atlasSize(len(pending), w)
			atlas := frame{cols * w, rows * h, image.Rect(0, 0, cols*w, rows*h), make([]uint32, cols*w*rows*h)}
			for i, entry := range pending {
				x, y := i%cols*w, i/cols*h
				for row := range h {
					copy(atlas.pixels[(y+row)*atlas.width+x:][:w], entry.pixels[row*w:][:w])
				}
			}
			png, err := encodeFrame(atlas)
			if err != nil {
				return err
			}
			data = appendWords(data, glyphAtlas, pending[0].id, uint32(len(pending)), uint32(w), uint32(h), uint32(cols), uint32(len(png)-28))
			data = append(data, png[28:]...)
			operations++
		}
		data = append(data, draws...)
		operations += drawOperations
		pending, draws = nil, nil
		drawOperations, lastDraw = 0, -1
		return nil
	}
	var fallback []image.Rectangle
	add := func(r image.Rectangle) {
		if !r.Empty() && len(fallback) <= maxFallbackPNGs {
			fallback = append(fallback, r)
		}
	}
	add(image.Rect(f.rect.Min.X, f.rect.Min.Y, f.rect.Max.X, y0))
	add(image.Rect(f.rect.Min.X, y1, f.rect.Max.X, f.rect.Max.Y))
	add(image.Rect(f.rect.Min.X, y0, x0, y1))
	add(image.Rect(x1, y0, f.rect.Max.X, y1))
	// Merge equal PNG runs vertically, so a novel image stays a few rectangles
	// instead of becoming one PNG per tile. Bound pathological mixed damage.
	previous := make(map[[2]int]int)
	for y := y0; y < y1; y += h {
		next := make(map[[2]int]int)
		run := -1
		flush := func(x int) {
			if run < 0 {
				return
			}
			span := [2]int{run, x}
			if index, ok := previous[span]; ok {
				fallback[index].Max.Y = y + h
				next[span] = index
			} else if len(fallback) <= maxFallbackPNGs {
				next[span] = len(fallback)
				add(image.Rect(run, y, x, y+h))
			}
			run = -1
		}
		for x := x0; x < x1; x += w {
			r := image.Rect(x, y, x+w, y+h)
			key := fingerprint(f, r)
			entry, hit := e.entries[key]
			if hit && !sameGlyph(f, r, entry.pixels) {
				// A hash collision can never substitute unrelated glyph pixels.
				if run < 0 {
					run = x
				}
				continue
			}
			if !hit {
				// Repainting the same photograph in place is not glyph reuse.
				// Require reuse at another position before admitting a bitmap.
				at, repeated := e.seen[key]
				if !repeated || at == r.Min || *budget == 0 {
					if len(e.seen) >= 2*e.maxEntries {
						clear(e.seen)
					}
					e.seen[key] = r.Min
					if run < 0 {
						run = x
					}
					continue
				}
				oldCols, oldRows := atlasSize(len(pending), w)
				cols, rows := atlasSize(len(pending)+1, w)
				if rows*h > maxAtlasSide {
					if err := flushGlyphs(); err != nil {
						return nil, err
					}
					oldCols, oldRows, cols, rows = 0, 0, 1, 1
				}
				growth := (cols*rows - oldCols*oldRows) * w * h * 4
				if e.bytes+growth > e.maxBytes || len(e.entries) >= e.maxEntries {
					if err := flushGlyphs(); err != nil {
						return nil, err
					}
					clear(e.entries)
					e.bytes = 0
					data = appendWords(data, glyphReset)
					operations++
					growth = w * h * 4
				}
				patch := cropFrame(f, r)
				entry = cachedGlyph{uint32(len(e.entries) + 1), patch.pixels}
				e.entries[key] = entry
				e.bytes += growth
				pending = append(pending, entry)
				*budget -= 1
			}
			flush(x)
			if lastDraw >= 0 &&
				binary.LittleEndian.Uint32(draws[lastDraw+4:]) == entry.id &&
				binary.LittleEndian.Uint32(draws[lastDraw+12:]) == uint32(y) &&
				int(binary.LittleEndian.Uint32(draws[lastDraw+8:]))+int(binary.LittleEndian.Uint32(draws[lastDraw+16:]))*w == x {
				count := binary.LittleEndian.Uint32(draws[lastDraw+16:])
				binary.LittleEndian.PutUint32(draws[lastDraw+16:], count+1)
			} else {
				lastDraw = len(draws)
				draws = appendWords(draws, glyphDraw, entry.id, uint32(x), uint32(y), 1)
				drawOperations++
			}
		}
		flush(x1)
		previous = next
	}
	if err := flushGlyphs(); err != nil {
		return nil, err
	}
	if operations == 0 {
		data, err := encodeFrame(f)
		return [][]byte{data}, err
	}
	var messages [][]byte
	if len(fallback) > maxFallbackPNGs {
		fallback = []image.Rectangle{f.rect}
	}
	for _, r := range fallback {
		patch := f
		if r != f.rect {
			patch = cropFrame(f, r)
		}
		png, err := encodeFrame(patch)
		if err != nil {
			return nil, err
		}
		messages = append(messages, png)
	}
	binary.LittleEndian.PutUint32(data[12:16], operations)
	return append(messages, data), nil
}
