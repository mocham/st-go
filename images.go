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
	"sort"

	"st-go/term"
)

// imageAtlas holds the raw RGBA pixel blocks of all image-cell glyphs.
// Fg in an image glyph indexes into this slice (cw*ch entries per cell).
//
// The atlas is a bounded cache of bitmaps: imageSlots divides it into
// contiguous regions, each holding one decoded image (≤ one terminal screen of
// cells). When the bound is reached the most dated bitmap that fits a new
// image is recycled (dated bitmaps too small for the image are discarded
// first). This keeps memory bounded instead of growing on every `open`.
var (
	imageAtlas    []uint32
	imageSlots    []*imageSlot
	imageStampSeq uint64
)

// imageCacheScreens bounds the atlas to this many full terminal screens of
// image cells.
const imageCacheScreens = 4

// imageSlot is one cached bitmap region in imageAtlas: a contiguous run of
// capacity cells (cw*ch uint32s each). used is the live cell count; stamp is
// the LRU order.
type imageSlot struct {
	offset   int
	capacity int // cells
	used     int // cells
	stamp    uint64
}

// allocImageCells reserves a bitmap region for an image of `need` terminal
// cells and returns its atlas offset. The atlas is bounded to
// imageCacheScreens full screens; when full, the most dated bitmap that fits
// is recycled (dated bitmaps too small for the image are discarded), and if
// nothing fits the cache is reset and starts fresh.
func (t *Terminal) allocImageCells(need int) int {
	maxCells := t.rows * t.cols * imageCacheScreens
	if maxCells < 1 {
		maxCells = 1
	}
	if need < 1 {
		need = 1
	}
	full := t.rows * t.cols
	if full < 1 {
		full = 1
	}
	if need > full {
		need = full // each bitmap must not exceed one terminal screen
	}
	imageStampSeq++
	now := imageStampSeq

	// 1. reuse a free slot that fits
	for _, s := range imageSlots {
		if s.used == 0 && s.capacity >= need {
			s.used = need
			s.stamp = now
			return s.offset
		}
	}

	// 2. recycle the most dated bitmap that fits; discard dated bitmaps that
	//    are too small for this image and keep looking.
	for _, s := range imageSlotsByStamp() {
		if s.capacity >= need {
			s.used = need
			s.stamp = now
			return s.offset
		}
		s.used = 0 // too small: free it
	}

	// 3. grow a new bitmap within the bound
	if imageSlotTotalCells()+need <= maxCells {
		off := len(imageAtlas)
		imageAtlas = append(imageAtlas, make([]uint32, need*t.cw*t.ch)...)
		imageSlots = append(imageSlots, &imageSlot{offset: off, capacity: need, used: need, stamp: now})
		return off
	}

	// 4. bound reached and nothing fits (fragmentation): reset and start
	//    fresh. Dangling image glyphs render blank via the draw bounds check.
	imageAtlas = nil
	imageSlots = nil
	imageAtlas = append(imageAtlas, make([]uint32, need*t.cw*t.ch)...)
	imageSlots = append(imageSlots, &imageSlot{offset: 0, capacity: need, used: need, stamp: now})
	return 0
}

func imageSlotsByStamp() []*imageSlot {
	out := make([]*imageSlot, len(imageSlots))
	copy(out, imageSlots)
	sort.Slice(out, func(i, j int) bool { return out[i].stamp < out[j].stamp })
	return out
}

func imageSlotTotalCells() int {
	n := 0
	for _, s := range imageSlots {
		n += s.capacity
	}
	return n
}

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
	cols, rows, glyphs = t.imageToGlyphs(w, h, rgba, opts)
	return cols, rows, glyphs, true
}

// ImageDecodeAnim decodes an animated WebP's metadata for the open DSL `anim`
// option: per-frame durations (ms), frame count and the canvas grid size. No
// bitmaps are allocated here; frames are decoded on demand by
// ImageDecodeAnimFrame as the animation plays.
func (t *Terminal) ImageDecodeAnim(encoded []byte, opts term.ImageDecodeOptions) (durations []int, frameCount, cols, rows int, ok bool) {
	durations, w, h, dOk := decodeWebPAnimInfo(encoded)
	if !dOk || w <= 0 || h <= 0 || len(durations) == 0 {
		return nil, 0, 0, 0, false
	}
	_, _, cols, rows = t.imageGrid(w, h, opts)
	return durations, len(durations), cols, rows, true
}

// ImageDecodeAnimFrame decodes one frame (frameIdx, 0-based) of an animated
// WebP into atlas glyphs. Each call allocates a bounded bitmap (recycling the
// most dated one), so a playing animation's memory stays bounded.
func (t *Terminal) ImageDecodeAnimFrame(encoded []byte, frameIdx int, opts term.ImageDecodeOptions) (cols, rows int, glyphs []term.Glyph, ok bool) {
	w, h, rgba, dOk := decodeWebPAnimFrame(encoded, frameIdx)
	if !dOk || w <= 0 || h <= 0 {
		return 0, 0, nil, false
	}
	cols, rows, glyphs = t.imageToGlyphs(w, h, rgba, opts)
	return cols, rows, glyphs, true
}

// imageToGlyphs breaks a decoded RGBA image into one glyph per terminal cell,
// allocating a bounded atlas bitmap and writing each cell's pixel block into
// it. Returns the grid size and the glyphs in row-major order.
func (t *Terminal) imageToGlyphs(w, h int, rgba []byte, opts term.ImageDecodeOptions) (cols, rows int, glyphs []term.Glyph) {
	logicalCols, logicalRows, cols, rows := t.imageGrid(w, h, opts)
	scaled := opts.FitWidth || opts.FitHeight || opts.FitContain

	base := t.allocImageCells(cols * rows)
	glyphs = make([]term.Glyph, 0, cols*rows)
	for gy := 0; gy < rows; gy++ {
		for gx := 0; gx < cols; gx++ {
			offset := base + (gy*cols+gx)*t.cw*t.ch
			t.writeImageBlock(offset, w, h, rgba, gx, gy, logicalCols, logicalRows, scaled)
			glyphs = append(glyphs, term.Glyph{U: term.ImageRune, Fg: uint32(offset)})
		}
	}
	return cols, rows, glyphs
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
	base := t.allocImageCells(cols * rows)
	for gy := 0; gy < rows; gy++ {
		for gx := 0; gx < cols; gx++ {
			offset := base + (gy*cols+gx)*t.cw*t.ch
			t.writeImageBlockFromUint(offset, w, h, rgba, gx, gy, logicalCols, logicalRows, scaled)
			glyphs = append(glyphs, term.Glyph{U: term.ImageRune, Fg: uint32(offset)})
		}
	}
	return cols, rows, glyphs, true
}

// writeImageBlockFromUint is like writeImageBlock but consumes a []uint32 ARGB
// buffer instead of raw RGBA bytes.
func (t *Terminal) writeImageBlockFromUint(offset, w, h int, rgba []uint32, gx, gy, cols, rows int, scaled bool) {
	if !scaled {
		for yy := 0; yy < t.ch; yy++ {
			sy := gy*t.ch + yy
			for xx := 0; xx < t.cw; xx++ {
				sx := gx*t.cw + xx
				o := offset + yy*t.cw + xx
				if sy < h && sx < w {
					imageAtlas[o] = rgba[sy*w+sx]
				} else {
					imageAtlas[o] = 0xFF000000
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
			o := offset + yy*t.cw + xx
			imageAtlas[o] = rgba[sy*w+sx]
		}
	}
}

// writeImageBlock writes the cw*ch pixel block for cell (gx,gy) into the
// atlas at `offset`. In native mode it copies the image's raw pixels for that
// cell (1 image pixel = 1 cell pixel), clamping at the image edge. In fit mode
// the image block for the cell is scaled to fill the cell.
func (t *Terminal) writeImageBlock(offset, w, h int, rgba []byte, gx, gy, cols, rows int, scaled bool) {
	if !scaled {
		// native: each cell shows a raw cw x ch block of the image at its
		// natural resolution (many colors per cell). At the image edge the
		// block is partial; the remainder is filled with black.
		for yy := 0; yy < t.ch; yy++ {
			sy := gy*t.ch + yy
			for xx := 0; xx < t.cw; xx++ {
				sx := gx*t.cw + xx
				o := offset + yy*t.cw + xx
				if sy < h && sx < w {
					po := (sy*w + sx) * 4
					imageAtlas[o] = 0xFF000000|
						uint32(rgba[po])<<16|
						uint32(rgba[po+1])<<8|
						uint32(rgba[po+2])
				} else {
					imageAtlas[o] = 0xFF000000
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
			o := offset + yy*t.cw + xx
			po := (sy*w + sx) * 4
			imageAtlas[o] = 0xFF000000|
				uint32(rgba[po])<<16|
				uint32(rgba[po+1])<<8|
				uint32(rgba[po+2])
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
	imageSlots = nil
	imageStampSeq = 0
}
