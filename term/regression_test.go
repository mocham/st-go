package term

import (
	"strings"
	"testing"

	"st-go/config"
)

// Regression tests for the term core, comparing against the C st semantics.

func newTermHooks(cfg *config.Config) (*Term, *mockHooks) {
	m := &mockHooks{}
	trm := NewTerm(cfg, m)
	return trm, m
}

// TestCSIArgsDefault verifies that absent/zero CSI arguments fall back to the
// default for cursor movements (st's DEFAULT macro), so ESC [ C moves 1.
func TestCSIArgsDefault(t *testing.T) {
	cfg := config.Default()
	cfg.Cols = 80
	cfg.Rows = 24
	trm, _ := newTermHooks(cfg)
	trm.Twrite([]byte("A"), false)
	// ESC [ C (no argument) must move right by 1
	trm.Twrite([]byte("\x1b[C"), false)
	if trm.CursorX() != 2 {
		t.Fatalf("ESC [ C: cursor x=%d want 2", trm.CursorX())
	}
	// ESC [ 0 C (explicit 0) also moves by 1 per st's DEFAULT
	trm.Twrite([]byte("\x1b[0C"), false)
	if trm.CursorX() != 3 {
		t.Fatalf("ESC [ 0 C: cursor x=%d want 3", trm.CursorX())
	}
}

// TestBackspaceThenForwardCursor reproduces the readline corruption: ABC, \b,
// ESC [ C, then D must produce ABCD (the forward move must advance by 1).
func TestBackspaceThenForwardCursor(t *testing.T) {
	cfg := config.Default()
	cfg.Cols = 80
	cfg.Rows = 24
	trm, _ := newTermHooks(cfg)
	trm.Twrite([]byte("[root@trixie st-go]# ABC"), false)
	trm.Twrite([]byte("\b"), false)
	trm.Twrite([]byte("\x1b[C"), false)
	trm.Twrite([]byte("D"), false)
	trm.Redraw()
	line := trm.LineText(0)
	if !strings.Contains(line, "ABCD") {
		t.Fatalf("expected ABCD, got %q", line)
	}
}

// TestResizeClampsCursor verifies tresize keeps the cursor inside the new
// bounds and clears newly added region correctly.
func TestResizeClampsCursor(t *testing.T) {
	cfg := config.Default()
	cfg.Cols = 80
	cfg.Rows = 24
	trm, _ := newTermHooks(cfg)
	trm.Twrite([]byte("\x1b[30;5H"), false)
	trm.Tresize(20, 5)
	if trm.CursorX() > 19 || trm.CursorY() > 4 {
		t.Fatalf("cursor out of bounds after shrink: (%d,%d)", trm.CursorX(), trm.CursorY())
	}
	// grow and ensure no panic / bounds
	trm.Tresize(120, 40)
	if trm.Cols() != 120 || trm.Rows() != 40 {
		t.Fatalf("grow: got %dx%d", trm.Cols(), trm.Rows())
	}
}

// TestWideCharGlyph verifies a wide (CJK) rune sets ATTR_WIDE on its cell and
// ATTR_WDUMMY on the following cell.
func TestWideCharGlyph(t *testing.T) {
	cfg := config.Default()
	cfg.Cols = 80
	cfg.Rows = 24
	trm, _ := newTermHooks(cfg)
	trm.Twrite([]byte("中"), false) // U+4E2D, width 2
	g0 := trm.LineAt(0, 0)
	if g0.Mode&ATTRWide == 0 {
		t.Fatalf("expected ATTR_WIDE at (0,0), mode=%#x", g0.Mode)
	}
	g1 := trm.LineAt(1, 0)
	if g1.Mode&ATTRWdummy == 0 {
		t.Fatalf("expected ATTR_WDUMMY at (1,0), mode=%#x", g1.Mode)
	}
}

// TestSGRTruecolor verifies 38;2;r;g;b and 38;5;n set the fg as expected.
func TestSGRTruecolor(t *testing.T) {
	cfg := config.Default()
	cfg.Cols = 80
	cfg.Rows = 24
	trm, _ := newTermHooks(cfg)
	trm.Twrite([]byte("\x1b[38;5;196mX"), false)
	if trm.c.attr.Fg != 196 {
		t.Fatalf("38;5;196: fg=%d want 196", trm.c.attr.Fg)
	}
	trm.Twrite([]byte("\x1b[38;2;1;2;3mY"), false)
	if trm.c.attr.Fg != TrueColor(1, 2, 3) {
		t.Fatalf("38;2;1;2;3: fg=%#x want truecolor", trm.c.attr.Fg)
	}
}

// TestSelectionGetSel verifies getsel produces correct text across lines.
func TestSelectionGetSel(t *testing.T) {
	cfg := config.Default()
	cfg.Cols = 80
	cfg.Rows = 24
	trm, _ := newTermHooks(cfg)
	trm.Twrite([]byte("hello world\nsecond"), false)
	trm.SelStart(0, 0, 0)
	trm.SelExtend(9, 0, SelRegular, 0) // drag
	trm.SelExtend(9, 0, SelRegular, 1) // release
	if got := trm.GetSel(); got != "hello worl" {
		t.Fatalf("getsel=%q want %q", got, "hello worl")
	}
}

// TestImageRunePlacement verifies image-cell glyphs (U=ImageRune) can be
// placed via the DSL without panicking.
func TestImageRunePlacement(t *testing.T) {
	cfg := config.Default()
	cfg.Cols = 40
	cfg.Rows = 10
	trm, _ := newTermHooks(cfg)
	// mock ImageDecode returns ok=false; a missing file should no-op safely
	trm.Twrite([]byte("\x1bPopen '/nonexistent/image.png';\x1b\\"), false)
	// no panic is the success criterion
}
