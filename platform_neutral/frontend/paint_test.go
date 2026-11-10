package frontend

import "testing"

func TestTerminalPaintLeavesInterveningRowsUntouched(t *testing.T) {
	trm := regionTestTerminal(t)
	const sentinel uint32 = 0xff123abc
	// Poison the framebuffer on the three clean rows. An unnecessary repaint
	// would replace these pixels from the terminal model, even if the glyphs
	// themselves looked unchanged in an ordinary before/after screenshot.
	firstY := trm.borderpx + trm.ch
	lastY := trm.borderpx + 4*trm.ch
	for y := firstY; y < lastY; y++ {
		for x := trm.borderpx; x < trm.borderpx+trm.cols*trm.cw; x++ {
			trm.framebuf[y*trm.fbW+x] = sentinel
		}
	}
	trm.writeTerminalOutput(trm.termCore, []byte("\x1b[?2026h\x1b[1;2H\x1b[48;2;17;34;51m \x1b[5;2H \x1b[1;2H\x1b[?2026l"))
	trm.paintTerminalOutput(trm.termCore)
	for y := firstY; y < lastY; y++ {
		for x := trm.borderpx; x < trm.borderpx+trm.cols*trm.cw; x++ {
			if got := trm.framebuf[y*trm.fbW+x]; got != sentinel {
				t.Fatalf("clean row %d repainted at pixel (%d,%d): %#x", (y-trm.borderpx)/trm.ch+1, x, y, got)
			}
		}
	}
	// The status update really was painted; this is not a no-op frontend test.
	x, y := trm.borderpx+trm.cw, trm.borderpx+4*trm.ch
	if got := trm.framebuf[y*trm.fbW+x]; got != 0xff112233 {
		t.Fatalf("changed status cell was not painted: %#x", got)
	}
}
