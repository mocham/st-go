package term

import (
	"os"
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

// TestDslOpenText verifies the DSL "open" renders a text file from the
// cursor row down, stopping at the last screen row without scrolling.
func TestDslOpenText(t *testing.T) {
	core, _ := newTestTerm(t, 20, 6)
	// 10 lines of "abc": only rows 0..5 (6 rows) may show content; the rest
	// must be clipped (no scroll).
	var payload []byte
	for i := 0; i < 10; i++ {
		payload = append(payload, []byte("abc\n")...)
	}
	core.Twrite([]byte("\x1bPclear\x1b\\"), false)
	// feed the text via a direct dslOpenText call is not possible (path read);
	// instead write to a temp file and open it.
	dir := t.TempDir()
	f := dir + "/preview.txt"
	if err := os.WriteFile(f, payload, 0644); err != nil {
		t.Fatal(err)
	}
	core.Twrite([]byte("\x1bPopen '"+f+"'\x1b\\"), false)
	core.Redraw()
	// rows 0..5 should each start with "abc"
	for y := 0; y < 6; y++ {
		lt := core.LineText(y)
		if !strings.HasPrefix(lt, "abc") {
			t.Fatalf("row %d = %q, want abc prefix", y, lt)
		}
	}
	// there must be no scroll: row 0 is still "abc" (not line 4+)
	if got := core.LineText(0); !strings.HasPrefix(got, "abc") {
		t.Fatalf("row0 scrolled: %q", got)
	}
	t.Logf("OK: text preview rendered rows 0..5, stopped at bottom")
}

// TestLooksLikeText confirms binary formats are not treated as text.
func TestLooksLikeText(t *testing.T) {
	core, _ := newTestTerm(t, 20, 6)
	png := []byte{0x89, 0x50, 0x4E, 0x47, 0x0D, 0x0A, 0x1A, 0x0A}
	if core.looksLikeText(png) {
		t.Fatalf("PNG sniffed as text")
	}
	if !core.looksLikeText([]byte("hello world\n")) {
		t.Fatalf("plain text not recognized")
	}
	if core.looksLikeText([]byte{0x00, 0x01, 0x02, 0x00, 0x00, 0x00, 0x01}) {
		t.Fatalf("NUL-heavy binary sniffed as text")
	}
}

// TestDslOpenTextTruncate verifies long lines are truncated at the screen's
// right edge (not wrapped) and rendering stops at the last row.
func TestDslOpenTextTruncate(t *testing.T) {
	core, _ := newTestTerm(t, 10, 3)
	dir := t.TempDir()
	f := dir + "/long.txt"
	// a very long line (100 chars) then another short line
	content := strings.Repeat("x", 100) + "\n" + "short\n"
	if err := os.WriteFile(f, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
	core.Twrite([]byte("\x1bPopen '"+f+"'\x1b\\"), false)
	core.Redraw()
	// row0: 10 x's (full width), NOT wrapped
	if got := core.LineText(0); got != strings.Repeat("x", 10) {
		t.Fatalf("row0=%q want 10 x's", got)
	}
	// row1: the truncated line was skipped, so "short" lands on row1
	if got := core.LineText(1); !strings.HasPrefix(got, "short") {
		t.Fatalf("row1=%q want short", got)
	}
	// row2 (last row): nothing from beyond
	if got := core.LineText(2); got != strings.Repeat(" ", 10) {
		t.Fatalf("row2=%q want blank (stopped at last row)", got)
	}
	t.Logf("OK: long line truncated, stopped at last row")
}

func TestDslOpenTextRectangle(t *testing.T) {
	core, _ := newTestTerm(t, 10, 5)
	for y := 0; y < core.row; y++ {
		for x := 0; x < core.col; x++ {
			core.line[y][x].U = 'x'
		}
	}
	core.c.x, core.c.y = 8, 4
	dir := t.TempDir()
	f := dir + "/rect.txt"
	if err := os.WriteFile(f, []byte("abcdef\nz\n"), 0644); err != nil {
		t.Fatal(err)
	}
	core.Twrite([]byte("\x1bPopen '"+f+"' rect 3 2 4 2\x1b\\"), false)
	if got := core.LineText(1); got != "xxabcdxxxx" {
		t.Fatalf("row 2 = %q, want rectangle-clipped text", got)
	}
	if got := core.LineText(2); got != "xxz   xxxx" {
		t.Fatalf("row 3 = %q, want cleared rectangle remainder", got)
	}
	if got := core.LineText(0); got != "xxxxxxxxxx" {
		t.Fatalf("outside rectangle changed: %q", got)
	}
	if core.c.x != 8 || core.c.y != 4 {
		t.Fatalf("rectangle open moved cursor to %d,%d", core.c.x, core.c.y)
	}
}

func TestDslOpenImageRectangleOptions(t *testing.T) {
	core, hooks := newTestTerm(t, 10, 5)
	hooks.imageCols, hooks.imageRows, hooks.imageOK = 2, 1, true
	hooks.imageGlyphs = []Glyph{{U: ImageRune}, {U: ImageRune}}
	dir := t.TempDir()
	f := dir + "/rect.png"
	if err := os.WriteFile(f, []byte{0x89, 'P', 'N', 'G', 0x0d, 0x0a, 0x1a, 0x0a}, 0644); err != nil {
		t.Fatal(err)
	}
	core.c.x, core.c.y = 9, 4
	core.Twrite([]byte("\x1bPopen '"+f+"' rect 2 2 4 3 fit-contain page 2\x1b\\"), false)
	if !hooks.imageOptions.FitContain || hooks.imageOptions.ViewCols != 4 ||
		hooks.imageOptions.ViewRows != 3 || hooks.imageOptions.Page != 1 {
		t.Fatalf("image options = %#v", hooks.imageOptions)
	}
	if core.line[2][2].U != ImageRune || core.line[2][3].U != ImageRune {
		t.Fatalf("image was not centered in rectangle")
	}
	if core.line[2][1].U == ImageRune || core.line[2][4].U == ImageRune {
		t.Fatalf("image escaped centered placement")
	}
	if core.c.x != 9 || core.c.y != 4 {
		t.Fatalf("rectangle image moved cursor to %d,%d", core.c.x, core.c.y)
	}
}

func TestDslWindowGeometry(t *testing.T) {
	core, hooks := newTestTerm(t, 10, 5)
	core.cfg.AllowGeometryOps = true
	core.Twrite([]byte("\x1bPwindow remember browser;"+
		"window place bottom-left 8px 2% 25% 56px restore browser;"+
		"window restore browser\x1b\\"), false)
	if len(hooks.geometry) != 3 {
		t.Fatalf("got %d geometry requests, want 3", len(hooks.geometry))
	}
	place := hooks.geometry[1]
	if place.Action != GeometryPlace || place.Anchor != "bottom-left" ||
		place.X.Unit != GeometryPixels || place.X.Value != 8 ||
		place.Y.Unit != GeometryRatio || place.Y.Value != 0.02 ||
		place.W.Unit != GeometryRatio || place.W.Value != 0.25 ||
		place.H.Unit != GeometryPixels || place.H.Value != 56 ||
		place.RestoreTag != "browser" {
		t.Fatalf("place request = %#v", place)
	}
	core.cfg.AllowGeometryOps = false
	core.Twrite([]byte("\x1bPwindow forget browser\x1b\\"), false)
	if len(hooks.geometry) != 3 {
		t.Fatal("disabled window operation reached frontend")
	}
}

func TestDslWindowGeometryRequiresToken(t *testing.T) {
	core, hooks := newTestTerm(t, 10, 5)
	core.cfg.AllowGeometryOps = true
	core.cfg.GeometryToken = "secret"
	core.Twrite([]byte("\x1bPwindow remember browser\x1b\\"), false)
	if len(hooks.geometry) != 0 {
		t.Fatal("unauthenticated geometry request reached frontend")
	}
	core.Twrite([]byte("\x1bPwindow auth secret remember browser\x1b\\"), false)
	if len(hooks.geometry) != 1 || hooks.geometry[0].Action != GeometryRemember {
		t.Fatalf("authenticated geometry requests = %#v", hooks.geometry)
	}
}
