package main

// Image support for the DCS display DSL.
//
// When an image is loaded it is immediately decoded and broken into one
// glyph per terminal cell. Each image-cell glyph is a plain hashable value
// with U=ImageRune and Fg holding the offset of that cell's raw pixel block
// (cw*ch uint32s) in the shared atlas below. The original image buffer is
// then discarded. A cell therefore shows the image's actual pixels — many
// colors per cell — with no per-glyph color transformation.

import (
	"st-go/term"
)

// imageAtlas holds the raw RGBA pixel blocks of all image-cell glyphs.
// Fg in an image glyph indexes into this slice (cw*ch entries per cell).
var imageAtlas []uint32

// ImageDecode decodes encoded image bytes and breaks it into one glyph per
// terminal cell. fitW/fitH resize (scale, aspect-preserving) the image to
// the terminal width/height; otherwise it stays at native resolution, with
// the draw loop truncating the width and scrolling vertically like text.
// The glyphs are returned in row-major order (rows*cols entries).
func (t *Terminal) ImageDecode(encoded []byte, fitW, fitH bool) (cols, rows int, glyphs []term.Glyph, ok bool) {
	w, h, rgba, err := decodeImage(encoded)
	if err || w <= 0 || h <= 0 {
		return 0, 0, nil, false
	}

	// grid size
	if !fitW && !fitH {
		// native: each cell shows a raw cw x ch block of the image (many
		// colors per cell, no resampling). Truncate the width to the
		// terminal; columns beyond the terminal are discarded, not glyphed.
		cols = (w + t.cw - 1) / t.cw
		rows = (h + t.ch - 1) / t.ch
		if cols > t.cols {
			cols = t.cols
		}
	} else if fitH {
		// fit height: rows = terminal rows; scale width to preserve the
		// image aspect ratio accounting for the cell's pixel aspect
		// (cw x ch), so the rendered shape matches the image.
		rows = t.rows
		if rows < 1 {
			rows = 1
		}
		cols = w * rows * t.ch / (h * t.cw)
		if cols < 1 {
			cols = 1
		}
	} else { // fitW
		cols = t.cols
		if cols < 1 {
			cols = 1
		}
		rows = h * cols * t.cw / (w * t.ch)
		if rows < 1 {
			rows = 1
		}
	}

	glyphs = make([]term.Glyph, 0, cols*rows)
	for gy := 0; gy < rows; gy++ {
		for gx := 0; gx < cols; gx++ {
			offset := len(imageAtlas)
			t.appendImageBlock(w, h, rgba, gx, gy, cols, rows, fitW, fitH)
			glyphs = append(glyphs, term.Glyph{U: term.ImageRune, Fg: uint32(offset)})
		}
	}
	return cols, rows, glyphs, true
}

// appendImageBlock appends the cw*ch pixel block for cell (gx,gy) to the
// atlas. In native mode it copies the image's raw pixels for that cell
// (1 image pixel = 1 cell pixel), clamping at the image edge. In fit mode
// the image block for the cell is scaled to fill the cell.
func (t *Terminal) appendImageBlock(w, h int, rgba []byte, gx, gy, cols, rows int, fitW, fitH bool) {
	if !fitW && !fitH {
		// native: each cell shows a raw cw x ch block of the image at its
		// natural resolution (many colors per cell). At the image edge the
		// block is partial; the remainder is filled with black.
		for yy := 0; yy < t.ch; yy++ {
			sy := gy*t.ch + yy
			for xx := 0; xx < t.cw; xx++ {
				sx := gx*t.cw + xx
				if sy < h && sx < w {
					o := (sy*w + sx) * 4
					imageAtlas = append(imageAtlas, 0xFF000000|
						uint32(rgba[o])<<16|
						uint32(rgba[o+1])<<8|
						uint32(rgba[o+2]))
				} else {
					imageAtlas = append(imageAtlas, 0xFF000000)
				}
			}
		}
		return
	}
	// fit: scale the image block for this cell to fill the cell
	x0 := gx * w / cols
	x1 := (gx + 1) * w / cols
	y0 := gy * h / rows
	y1 := (gy + 1) * h / rows
	if x1 > w {
		x1 = w
	}
	if y1 > h {
		y1 = h
	}
	for yy := 0; yy < t.ch; yy++ {
		sy := y0 + (yy * (y1 - y0) / t.ch)
		if sy >= y1 {
			sy = y1 - 1
		}
		if sy < y0 {
			sy = y0
		}
		for xx := 0; xx < t.cw; xx++ {
			sx := x0 + (xx * (x1 - x0) / t.cw)
			if sx >= x1 {
				sx = x1 - 1
			}
			if sx < x0 {
				sx = x0
			}
			o := (sy*w + sx) * 4
			imageAtlas = append(imageAtlas, 0xFF000000|
				uint32(rgba[o])<<16|
				uint32(rgba[o+1])<<8|
				uint32(rgba[o+2]))
		}
	}
}

// drawImageCell blits the raw pixel block for an image-cell glyph from the
// atlas into the framebuffer at cell (x, y).
func (t *Terminal) drawImageCell(g term.Glyph, x, y int) {
	if g.U != term.ImageRune {
		return
	}
	offset := int(g.Fg)
	if offset < 0 || offset+t.cw*t.ch > len(imageAtlas) {
		return
	}
	if fbW < 1 || fbH < 1 {
		return
	}
	px := t.borderpx + x*t.cw
	py := t.borderpx + y*t.ch
	for yy := 0; yy < t.ch; yy++ {
		dst := (py+yy)*fbW + px
		if dst+t.cw > len(framebuf) {
			break
		}
		src := offset + yy*t.cw
		copy(framebuf[dst:dst+t.cw], imageAtlas[src:src+t.cw])
	}
	fbDirty = true
}

// ImageClearAll clears the image glyph atlas (terminal reset / clear).
func (t *Terminal) ImageClearAll() {
	imageAtlas = nil
}
