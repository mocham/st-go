package frontend

import (
	"image"

	"st-go/platform_neutral/api"
	term "st-go/platform_neutral/stterminal"
)

func (*Terminal) CursorIsOverlay() bool { return true }

// DrawCursor prepares only a cell-sized overlay. Moving, hiding, or changing
// the caret never restores/rasterizes glyphs or mutates the content framebuffer.
func (t *Terminal) DrawCursor(cx, cy int, g term.Glyph, _, _ int, _ term.Glyph) {
	if t.winModeIs(term.ModeHide) || t.fbW < 1 || t.fbH < 1 {
		t.textCursor.Bounds = image.Rectangle{}
		t.textCursor.Pixels = t.textCursor.Pixels[:0]
		t.textCursor.Base = t.textCursor.Base[:0]
		return
	}
	x, y := t.borderpx+cx*t.cw, t.borderpx+cy*t.ch
	w := t.cw
	if g.Mode&term.ATTRWide != 0 {
		w *= 2
	}
	bounds := image.Rect(x, y, x+w, y+t.ch).Intersect(t.terminalClip())
	size := bounds.Dx() * bounds.Dy()
	if bounds.Empty() {
		t.textCursor = api.TextCursor{}
		return
	}
	pixels := t.textCursor.Pixels
	if cap(pixels) < size {
		pixels = make([]uint32, size)
	} else {
		pixels = pixels[:size]
	}
	for row := bounds.Min.Y; row < bounds.Max.Y; row++ {
		copy(pixels[(row-bounds.Min.Y)*bounds.Dx():], t.framebuf[row*t.fbW+bounds.Min.X:row*t.fbW+bounds.Max.X])
	}
	base := t.textCursor.Base
	if cap(base) < size {
		base = make([]uint32, size)
	} else {
		base = base[:size]
	}
	copy(base, pixels)
	t.textCursor = api.TextCursor{Bounds: bounds, Pixels: pixels, Base: base}
	fill := func(r image.Rectangle, color uint32) {
		r = r.Intersect(bounds)
		for row := r.Min.Y; row < r.Max.Y; row++ {
			at := (row-bounds.Min.Y)*bounds.Dx() + r.Min.X - bounds.Min.X
			for col := 0; col < r.Dx(); col++ {
				pixels[at+col] = color
			}
		}
	}
	g.Mode &= term.ATTRBold | term.ATTRItalic | term.ATTRUnderline | term.ATTRStruck | term.ATTRWide
	selected := t.termCore.Selected(cx, cy)
	var color uint32
	if t.winModeIs(term.ModeReverse) {
		g.Mode |= term.ATTRReverse
		g.Bg = ColDefaultFg
		if selected {
			color, g.Fg = t.colorAt(ColDefaultCs), ColDefaultRcs
		} else {
			color, g.Fg = t.colorAt(ColDefaultRcs), ColDefaultCs
		}
	} else {
		if selected {
			g.Fg, g.Bg = ColDefaultFg, ColDefaultRcs
		} else {
			g.Fg, g.Bg = ColDefaultBg, ColDefaultCs
		}
		color = t.colorAt(g.Bg)
	}
	if !t.winModeIs(term.ModeFocused) {
		fill(image.Rect(x, y, x+t.cw-1, y+1), color)
		fill(image.Rect(x, y, x+1, y+t.ch-1), color)
		fill(image.Rect(x+t.cw-1, y, x+t.cw, y+t.ch-1), color)
		fill(image.Rect(x, y+t.ch-1, x+t.cw, y+t.ch), color)
		return
	}
	switch t.cursorShape {
	case 7:
		g.U = 0x2603
		fallthrough
	case 0, 1, 2:
		if g.U != term.ImageRune {
			stride, glyph := t.glyphPixels(g.U, t.resolveFG(g), t.resolveBG(g), g.Mode&term.ATTRWide != 0)
			if stride > 0 {
				for row := bounds.Min.Y; row < bounds.Max.Y; row++ {
					at := (row-y)*stride + bounds.Min.X - x
					copy(pixels[(row-bounds.Min.Y)*bounds.Dx():], glyph[at:at+bounds.Dx()])
				}
			}
		}
	case 3, 4:
		fill(image.Rect(x, y+t.ch-t.cursorThick, x+t.cw, y+t.ch), color)
	case 5, 6:
		fill(image.Rect(x, y, x+t.cursorThick, y+t.ch), color)
	}
}

func (t *Terminal) presentTextCursor() {
	if host, ok := t.host.(api.TextCursorPainter); ok {
		host.SetTextCursor(t.textCursor)
	}
}
