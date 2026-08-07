package main

import (
	"unsafe"

	"st-go/term"
)

func between(x int, a, b int) bool { return a <= x && x <= b }

func clampInt(x, a, b int) int {
	if x < a {
		return a
	}
	if x > b {
		return b
	}
	return x
}

func uint32ToBytes(v []uint32) []byte {
	return (*[1 << 30]byte)(unsafe.Pointer(&v[0]))[:len(v)*4 : len(v)*4]
}

func (t *Terminal) defaultBgARGB() uint32 { return t.colorAt(ColDefaultBg) }
func (t *Terminal) defaultFgARGB() uint32 { return t.colorAt(ColDefaultFg) }

// glyphToARGB resolves a Glyph fg/bg index (or truecolor) to an ARGB value.
func (t *Terminal) glyphToARGB(v uint32) uint32 {
	if term.IsTrueColor(v) {
		return 0xFF000000 | (v & 0xFFFFFF)
	}
	return t.colorAt(v)
}

// xglyphcolors port from x.c: resolve fg and bg ARGB values for a glyph.
func (t *Terminal) xglyphcolors(g term.Glyph) (fg, bg uint32) {
	fg = t.glyphToARGB(g.Fg)
	bg = t.glyphToARGB(g.Bg)

	// Change basic system colors [0-7] to bright system colors [8-15]
	if g.Mode&term.ATTRBold != 0 && g.Mode&term.ATTRFaint == 0 &&
		!term.IsTrueColor(g.Fg) && between(int(g.Fg), 0, 7) {
		fg = t.colorAt(g.Fg + 8)
	}

	if t.winModeIs(term.ModeReverse) {
		if fg == t.colorAt(ColDefaultFg) {
			fg = t.colorAt(ColDefaultBg)
		} else {
			fg = 0xFF000000 | (^fg & 0xFFFFFF)
		}
		if bg == t.colorAt(ColDefaultBg) {
			bg = t.colorAt(ColDefaultFg)
		} else {
			bg = 0xFF000000 | (^bg & 0xFFFFFF)
		}
	}

	if g.Mode&term.ATTRFaint != 0 && g.Mode&term.ATTRBold == 0 {
		fg = 0xFF000000 | ((fg & 0xFFFFFF) / 2 & 0xFFFFFF)
	}

	if g.Mode&term.ATTRReverse != 0 {
		fg, bg = bg, fg
	}

	if g.Mode&term.ATTRBlink != 0 && t.winModeIs(term.ModeBlink) {
		fg = bg
	}

	if g.Mode&term.ATTRInvisible != 0 {
		fg = bg
	}
	return
}

func (t *Terminal) resolveFG(g term.Glyph) uint32 {
	fg, _ := t.xglyphcolors(g)
	return fg
}

func (t *Terminal) resolveBG(g term.Glyph) uint32 {
	_, bg := t.xglyphcolors(g)
	return bg
}
