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
func (t *Terminal) ImageDecode(encoded []byte, opts term.ImageDecodeOptions) (cols, rows int, glyphs []term.Glyph, ok bool) {
	// PDF: render the requested page (default first) to a bitmap via poppler.
	if isPDF(encoded) {
		return t.imageDecodePDF(encoded, opts)
	}
	w, h, rgba, err := decodeImage(encoded)
	if err || w <= 0 || h <= 0 {
		return 0, 0, nil, false
	}

	logicalCols, logicalRows, cols, rows := t.imageGrid(w, h, opts)
	scaled := opts.FitWidth || opts.FitHeight || opts.FitContain

	glyphs = make([]term.Glyph, 0, cols*rows)
	for gy := 0; gy < rows; gy++ {
		for gx := 0; gx < cols; gx++ {
			offset := len(imageAtlas)
			t.appendImageBlock(w, h, rgba, gx, gy, logicalCols, logicalRows, scaled)
			glyphs = append(glyphs, term.Glyph{U: term.ImageRune, Fg: uint32(offset)})
		}
	}
	return cols, rows, glyphs, true
}

func (t *Terminal) imageGrid(w, h int, opts term.ImageDecodeOptions) (logicalCols, logicalRows, cols, rows int) {
	targetCols, targetRows := t.cols, t.rows
	if opts.ViewCols > 0 {
		targetCols = opts.ViewCols
	}
	if opts.ViewRows > 0 {
		targetRows = opts.ViewRows
	}
	if targetCols < 1 {
		targetCols = 1
	}
	if targetRows < 1 {
		targetRows = 1
	}

	switch {
	case opts.FitContain:
		logicalCols = targetCols
		logicalRows = h * logicalCols * t.cw / (w * t.ch)
		if logicalRows > targetRows {
			logicalRows = targetRows
			logicalCols = w * logicalRows * t.ch / (h * t.cw)
		}
	case opts.FitHeight:
		logicalRows = targetRows
		logicalCols = w * logicalRows * t.ch / (h * t.cw)
	case opts.FitWidth:
		logicalCols = targetCols
		logicalRows = h * logicalCols * t.cw / (w * t.ch)
	default:
		logicalCols = (w + t.cw - 1) / t.cw
		logicalRows = (h + t.ch - 1) / t.ch
	}
	if logicalCols < 1 {
		logicalCols = 1
	}
	if logicalRows < 1 {
		logicalRows = 1
	}
	cols, rows = logicalCols, logicalRows
	if cols > targetCols {
		cols = targetCols
	}
	if opts.ViewRows > 0 && rows > targetRows {
		rows = targetRows
	}
	return logicalCols, logicalRows, cols, rows
}

// isPDF reports whether the bytes look like a PDF file ("%PDF-" header).
func isPDF(b []byte) bool {
	return len(b) >= 5 && b[0] == '%' && b[1] == 'P' && b[2] == 'D' && b[3] == 'F' && b[4] == '-'
}

// PDFPageCount returns the page count of a PDF (0 for non-PDF / failure).
func (t *Terminal) PDFPageCount(encoded []byte) int {
	if !isPDF(encoded) {
		return 0
	}
	return pdfPageCount(encoded)
}

// imageDecodePDF renders the first page of a PDF to a bitmap (BGRA) via
// poppler, then breaks it into image-cell glyphs like any other image.
func (t *Terminal) imageDecodePDF(encoded []byte, opts term.ImageDecodeOptions) (cols, rows int, glyphs []term.Glyph, ok bool) {
	// target bitmap size in pixels
	var pw, ph int
	if opts.ViewCols > 0 && opts.ViewRows > 0 {
		pw = opts.ViewCols * t.cw
		ph = opts.ViewRows * t.ch
	} else {
		// native: render at a reasonable resolution (600dpi-ish is too big;
		// use the terminal size) and let the draw loop scroll like text.
		pw = t.cols * t.cw
		ph = t.rows * t.ch
	}
	if pw < 1 {
		pw = 1
	}
	if ph < 1 {
		ph = 1
	}
	// The script just does +/-1 on its page counter, so the requested page can
	// be negative or past the end. Wrap it modulo the page count (like the
	// shell's $((N % pages)) arithmetic) so navigation always lands on a valid
	// page.
	n := pdfPageCount(encoded)
	if n < 1 {
		return 0, 0, nil, false
	}
	page := ((opts.Page % n) + n) % n
	bgra, _, ok := renderPDFPage(encoded, page, pw, ph)
	if !ok {
		return 0, 0, nil, false
	}

	// Build the glyph grid the same way as the raster path. For a PDF we
	// always render a bitmap at pw x ph, then treat it as w=pw, h=ph in
	// appendImageBlock (fit mode scales per-cell, native shows raw pixels).
	w, h := pw, ph

	logicalCols, logicalRows, cols, rows := t.imageGrid(w, h, opts)
	scaled := opts.FitWidth || opts.FitHeight || opts.FitContain

	// The atlas stores uint32 ARGB cells; convert the BGRA bitmap.
	rgba := make([]uint32, len(bgra)/4)
	for i := 0; i < len(rgba); i++ {
		b := bgra[i*4]
		g := bgra[i*4+1]
		r := bgra[i*4+2]
		rgba[i] = 0xFF000000 | uint32(r)<<16 | uint32(g)<<8 | uint32(b)
	}

	glyphs = make([]term.Glyph, 0, cols*rows)
	for gy := 0; gy < rows; gy++ {
		for gx := 0; gx < cols; gx++ {
			offset := len(imageAtlas)
			t.appendImageBlockFromUint(w, h, rgba, gx, gy, logicalCols, logicalRows, scaled)
			glyphs = append(glyphs, term.Glyph{U: term.ImageRune, Fg: uint32(offset)})
		}
	}
	return cols, rows, glyphs, true
}

// appendImageBlockFromUint is like appendImageBlock but consumes a []uint32
// ARGB buffer instead of raw RGBA bytes.
func (t *Terminal) appendImageBlockFromUint(w, h int, rgba []uint32, gx, gy, cols, rows int, scaled bool) {
	if !scaled {
		for yy := 0; yy < t.ch; yy++ {
			sy := gy*t.ch + yy
			for xx := 0; xx < t.cw; xx++ {
				sx := gx*t.cw + xx
				if sy < h && sx < w {
					imageAtlas = append(imageAtlas, rgba[sy*w+sx])
				} else {
					imageAtlas = append(imageAtlas, 0xFF000000)
				}
			}
		}
		return
	}
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
			imageAtlas = append(imageAtlas, rgba[sy*w+sx])
		}
	}
}

// appendImageBlock appends the cw*ch pixel block for cell (gx,gy) to the
// atlas. In native mode it copies the image's raw pixels for that cell
// (1 image pixel = 1 cell pixel), clamping at the image edge. In fit mode
// the image block for the cell is scaled to fill the cell.
func (t *Terminal) appendImageBlock(w, h int, rgba []byte, gx, gy, cols, rows int, scaled bool) {
	if !scaled {
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
