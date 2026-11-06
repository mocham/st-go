package main

import (
	"image"
	"sync"

	"st-go/limits"
	"st-go/term"
)

// framebuffer sized to the window
var (
	fbW, fbH int
	framebuf []uint32
)

func ensureFramebuffer(w, h int) bool {
	size, valid := limits.RGBA("framebuffer", w, h)
	if !valid {
		return false
	}
	if len(framebuf) >= w*h && fbW == w && fbH == h {
		return true
	}
	framebuf = make([]uint32, size/4)
	fbW, fbH = w, h
	return true
}

func (t *Terminal) clearFramebuffer() {
	bg := t.colorAt(ColDefaultBg)
	for i := range framebuf {
		framebuf[i] = bg
	}
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}

// Include raster dimensions so a cached cell cannot be reused at another size.
type glyphKey struct {
	u              rune
	fg, bg         uint32
	w, h, baseline int
}

// True-color output can produce a new key on every cell. Bound both pixel
// storage and map overhead instead of retaining every color seen forever.
const maxGlyphCacheBytes = limits.GlyphCacheBytes
const maxGlyphCacheEntries = limits.GlyphCacheEntries

var (
	glyphCache      = make(map[glyphKey][]uint32)
	glyphCacheBytes int
	glyphMu         sync.RWMutex
)

// drawGlyphAt renders rune u with fg/bg colors into the framebuffer at cell (x,y).
func (t *Terminal) drawGlyphAt(u rune, fg, bg uint32, x, y int, wide bool) {
	if fbW < 1 || fbH < 1 {
		return
	}
	w := t.cw
	if wide {
		w *= 2
	}
	key := glyphKey{u: u, fg: fg, bg: bg, w: w, h: t.ch, baseline: t.baseline}
	glyphMu.RLock()
	img, ok := glyphCache[key]
	glyphMu.RUnlock()
	if !ok {
		_, img = makeGlyph(u, fg, bg, w, t.ch, t.baseline)
		if len(img) == 0 {
			return
		}
		glyphMu.Lock()
		if size := len(img) * 4; size <= maxGlyphCacheBytes {
			if _, exists := glyphCache[key]; !exists {
				if glyphCacheBytes+size > maxGlyphCacheBytes || len(glyphCache) >= maxGlyphCacheEntries {
					clear(glyphCache)
					glyphCacheBytes = 0
				}
				glyphCache[key] = img
				glyphCacheBytes += size
			}
		}
		glyphMu.Unlock()
	}
	// blit into framebuffer
	px := t.borderpx + x*t.cw
	py := t.borderpx + y*t.ch
	t.blitTerminalPixels(px, py, w, t.ch, img)
}

// Clip at the final pixel write as well as at the logical terminal grid. In
// particular, a wide glyph/cursor at the divider must not touch the preview.
func (t *Terminal) blitTerminalPixels(px, py, w, h int, pixels []uint32) {
	r := image.Rect(px, py, px+w, py+h).Intersect(t.terminalClip())
	for y := r.Min.Y; y < r.Max.Y; y++ {
		src := (y-py)*w + r.Min.X - px
		copy(framebuf[y*fbW+r.Min.X:y*fbW+r.Max.X], pixels[src:src+r.Dx()])
	}
}

// fillRect fills a region of the framebuffer with argb color.
func (t *Terminal) fillRect(argb uint32, px, py, w, h int) {
	if fbW < 1 || fbH < 1 {
		return
	}
	if w <= 0 || h <= 0 {
		return
	}
	r := image.Rect(px, py, px+w, py+h).Intersect(t.terminalClip())
	for yy := r.Min.Y; yy < r.Max.Y; yy++ {
		dst := framebuf[yy*fbW+r.Min.X : yy*fbW+r.Max.X]
		for i := range dst {
			dst[i] = argb
		}
	}
}

// term.Hooks implementation

func (t *Terminal) Bell()     {}
func (t *Terminal) ClipCopy() {}

func (t *Terminal) DrawLine(line []term.Glyph, x1, y1, x2 int) {
	// clear row first
	px0 := t.borderpx + x1*t.cw
	py := t.borderpx + y1*t.ch
	w := (x2 - x1) * t.cw
	t.fillRect(t.defaultBgARGB(), px0, py, w, t.ch)
	for x := x1; x < x2; x++ {
		g := line[x]
		// image cell: blit the raw image pixels (many colors per cell)
		if g.U == term.ImageRune {
			t.drawImageCell(g, x, y1)
			continue
		}
		if g.U == 0 || g.Mode&term.ATTRWdummy != 0 {
			continue
		}
		if t.termCore.Selected(x, y1) {
			g.Mode ^= term.ATTRReverse
		}
		fg := t.resolveFG(g)
		bg := t.resolveBG(g)
		wide := g.Mode&term.ATTRWide != 0
		t.drawGlyphAt(g.U, fg, bg, x, y1, wide)
	}
}

// DrawCursor ports x.c xdrawcursor.
func (t *Terminal) DrawCursor(cx, cy int, g term.Glyph, ox, oy int, og term.Glyph) {
	// remove the old cursor
	if t.termCore.Selected(ox, oy) {
		og.Mode ^= term.ATTRReverse
	}
	if og.U == term.ImageRune {
		t.drawImageCell(og, ox, oy)
	} else {
		t.drawGlyphAt(og.U, t.resolveFG(og), t.resolveBG(og), ox, oy, og.Mode&term.ATTRWide != 0)
	}

	if t.winModeIs(term.ModeHide) {
		return
	}

	// select the right colors
	g.Mode &= term.ATTRBold | term.ATTRItalic | term.ATTRUnderline | term.ATTRStruck | term.ATTRWide

	var drawcol uint32
	sel := t.termCore.Selected(cx, cy)
	if t.winModeIs(term.ModeReverse) {
		g.Mode |= term.ATTRReverse
		g.Bg = ColDefaultFg
		if sel {
			drawcol = t.colorAt(ColDefaultCs)
			g.Fg = ColDefaultRcs
		} else {
			drawcol = t.colorAt(ColDefaultRcs)
			g.Fg = ColDefaultCs
		}
	} else {
		if sel {
			g.Fg = ColDefaultFg
			g.Bg = ColDefaultRcs
		} else {
			g.Fg = ColDefaultBg
			g.Bg = ColDefaultCs
		}
		drawcol = t.colorAt(g.Bg)
	}

	// draw the new one
	if t.winModeIs(term.ModeFocused) {
		switch t.cursorShape {
		case 7: // snowman
			g.U = 0x2603
			fallthrough
		case 0, 1, 2: // block
			if g.U == term.ImageRune {
				t.drawImageCell(g, cx, cy)
			} else {
				t.drawGlyphAt(g.U, t.resolveFG(g), t.resolveBG(g), cx, cy, g.Mode&term.ATTRWide != 0)
			}
		case 3, 4: // underline
			t.fillRect(drawcol, t.borderpx+cx*t.cw,
				t.borderpx+(cy+1)*t.ch-int(t.cursorThick),
				t.cw, int(t.cursorThick))
		case 5, 6: // bar
			t.fillRect(drawcol, t.borderpx+cx*t.cw,
				t.borderpx+cy*t.ch,
				int(t.cursorThick), t.ch)
		}
	} else {
		// unfocused: 1px outline box
		x := t.borderpx + cx*t.cw
		y := t.borderpx + cy*t.ch
		t.fillRect(drawcol, x, y, t.cw-1, 1)
		t.fillRect(drawcol, x, y, 1, t.ch-1)
		t.fillRect(drawcol, x+t.cw-1, y, 1, t.ch-1)
		t.fillRect(drawcol, x, y+t.ch-1, t.cw, 1)
	}
}

// FinishDraw is a no-op: paint.go submits the completed dirty region after the
// terminal core finishes rasterizing it.
func (t *Terminal) FinishDraw() {}
