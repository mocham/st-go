package stterminal

import (
	"testing"
	"time"
)

func TestSelectionBlankRectangle(t *testing.T) {
	trm, _ := newTestTerm(t, 10, 3)
	trm.Twrite([]byte("   text\r\n   more"), false)
	trm.SelStart(0, 0, 0)
	trm.SelExtend(1, 1, SelRectangular, 0)
	trm.SelExtend(1, 1, SelRectangular, 1)
	if got := trm.GetSel(); got != "\n" {
		t.Fatalf("blank rectangle = %q, want newline", got)
	}
}

func TestSelectionAfterShrink(t *testing.T) {
	for _, snap := range []int{0, snapWord, snapLine} {
		t.Run(string(rune('0'+snap)), func(t *testing.T) {
			trm, _ := newTestTerm(t, 10, 8)
			trm.SelStart(8, 6, snap)
			trm.SelExtend(9, 7, SelRegular, 0)
			trm.Tresize(5, 3)
			trm.SelExtend(4, 2, SelRegular, 1)
			if got := trm.GetSel(); got != "" {
				t.Fatalf("selection survived resize: %q", got)
			}
		})
	}
}

func TestDeleteWideCharacterAtLeftEdge(t *testing.T) {
	for _, redraw := range []bool{false, true} {
		name := "overwrite"
		if redraw {
			name = "redraw"
		}
		t.Run(name, func(t *testing.T) {
			trm, _ := newTestTerm(t, 10, 3)
			trm.Twrite([]byte("中\x1b[H\x1b[P"), false)
			if redraw {
				trm.Redraw()
			}
			trm.Twrite([]byte("x"), false)
			trm.Redraw()
			if got := trm.LineAt(0, 0).U; got != 'x' {
				t.Fatalf("first cell = %q, want x", got)
			}
		})
	}
}

func TestAnimationAfterShrink(t *testing.T) {
	trm, hooks := newTestTerm(t, 10, 8)
	hooks.imageCols, hooks.imageRows = 1, 1
	hooks.animFrameGlyphs = [][]Glyph{{{U: 'A'}}, {{U: 'B'}}}
	trm.SetAnim([]byte("animation"), ImageDecodeOptions{}, []int{100, 100}, 1, 1, 6, 4, 4, 3)
	start := trm.anim.startAt
	trm.Tresize(5, 3)
	trm.TickAnim(start.Add(150 * time.Millisecond))
	if trm.HasAnim() {
		t.Fatal("animation retained obsolete screen coordinates")
	}
}

// Exercise edits and resize/selection interleavings through the same public
// entry points used by PTY and X events, without opening files or an X server.
func FuzzTerminalEdits(f *testing.F) {
	f.Add([]byte{0, 1, 2, 3, 4, 5, 6, 7, 8, 9})
	f.Add([]byte{0, 1, 2, 14, 15, 16, 17, 18})
	edits := []string{
		"中", "\x1b[H", "\x1b[P", "x", "\x1b[@", "\x1b[2J",
		"\r\n", "\x1b[?1049h", "\x1b[?1049l", "\x1b[4h",
		"\x1b[4l", "\x1b[2;3r", "\x1b[?6h", "\x1b[?6l",
		"\x1b[999999999999999999999999999999C", "\t", "\b",
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		if len(data) > 512 {
			return
		}
		trm, _ := newTestTerm(t, 10, 8)
		for _, b := range data {
			switch int(b) % (len(edits) + 3) {
			case len(edits):
				trm.Tresize(1+int(b)%12, 1+int(b)%9)
			case len(edits) + 1:
				trm.SelStart(int(b)%trm.col, int(b)%trm.row, int(b)%3)
			case len(edits) + 2:
				trm.SelExtend(int(b)%trm.col, int(b)%trm.row, 1+int(b)%2, int(b)%2)
				trm.GetSel()
			default:
				trm.Twrite([]byte(edits[int(b)%(len(edits)+3)]), false)
			}
			trm.Redraw()
		}
	})
}
