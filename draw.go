package main

import (
	"sync"

	"st-go/term"
)

// framebuffer sized to the window
var (
	fbW, fbH int
	framebuf []uint32
	fbDirty  bool
	fbMu     sync.Mutex
)

func ensureFramebuffer(w, h int) {
	if len(framebuf) >= w*h && fbW == w && fbH == h {
		return
	}
	framebuf = make([]uint32, w*h)
	fbW, fbH = w, h
}

func (t *Terminal) clearFramebuffer() {
	bg := t.colorAt(ColDefaultBg)
	for i := range framebuf {
		framebuf[i] = bg
	}
	fbDirty = true
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

// glyphCache keyed by (rune, fg, bg, wide)
type glyphKey struct {
	u    rune
	fg   uint32
	bg   uint32
	wide bool
}

var (
	glyphCache  = make(map[glyphKey][]uint32)
	glyphMu     sync.RWMutex
)

// drawGlyphAt renders rune u with fg/bg colors into the framebuffer at cell (x,y).
func (t *Terminal) drawGlyphAt(u rune, fg, bg uint32, x, y int, wide bool) {
	if fbW < 1 || fbH < 1 {
		return
	}
	key := glyphKey{u, fg, bg, wide}
	glyphMu.RLock()
	img, ok := glyphCache[key]
	glyphMu.RUnlock()
	if !ok {
		w := t.cw
		if wide {
			w *= 2
		}
		_, img = makeGlyph(u, fg, bg, w, t.ch, t.baseline)
		glyphMu.Lock()
		glyphCache[key] = img
		glyphMu.Unlock()
	}
	// blit into framebuffer
	px := t.borderpx + x*t.cw
	py := t.borderpx + y*t.ch
	w := t.cw
	if wide {
		w *= 2
	}
	if py < 0 || py >= fbH {
		return
	}
	if px < 0 || px >= fbW {
		return
	}
	if px+w > fbW {
		w = fbW - px
	}
	if py+t.ch > fbH {
		return
	}
	for yy := 0; yy < t.ch; yy++ {
		dst := (py+yy)*fbW + px
		src := yy * w
		copy(framebuf[dst:dst+w], img[src:src+w])
	}
	fbDirty = true
}

// fillRect fills a region of the framebuffer with argb color.
func (t *Terminal) fillRect(argb uint32, px, py, w, h int) {
	if fbW < 1 || fbH < 1 {
		return
	}
	px = clampInt(px, 0, fbW-1)
	py = clampInt(py, 0, fbH-1)
	if px+w > fbW {
		w = fbW - px
	}
	if py+h > fbH {
		h = fbH - py
	}
	for yy := 0; yy < h; yy++ {
		dst := framebuf[(py+yy)*fbW+px : (py+yy)*fbW+px+w]
		for i := range dst {
			dst[i] = argb
		}
	}
	fbDirty = true
}

// term.Hooks implementation

func (t *Terminal) Bell() {}
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

// FinishDraw is a no-op: the single paint worker owns the framebuffer->X11
// blit (see paint.go), so the term core's rasterize step needs no flush here.
func (t *Terminal) FinishDraw() {}
