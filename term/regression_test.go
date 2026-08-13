package term

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

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

// TestDslOpenAnim verifies the open DSL `anim` option: an animated image is
// decoded into frames and played by TickAnim (play once, holding the last
// frame), with a terminal-drawn progress line in the rect's bottom row.
func TestDslOpenAnim(t *testing.T) {
	cfg := config.Default()
	cfg.Cols = 40
	cfg.Rows = 20
	trm, m := newTermHooks(cfg)
	// three 3x3 frames (distinct runes) at 100ms each
	makeFrame := func(u rune) []Glyph {
		g := make([]Glyph, 9)
		for i := range g {
			g[i] = Glyph{U: u}
		}
		return g
	}
	m.imageCols = 3
	m.imageRows = 3
	m.animFrameGlyphs = [][]Glyph{makeFrame('A'), makeFrame('B'), makeFrame('C')}
	m.animDurations = []int{100, 100, 100}
	m.animFrameCount = 3
	m.animOK = true

	now := time.Now()
	// dslOpen reads the file bytes first; the mock decodes them as animated.
	animPath := filepath.Join(t.TempDir(), "anim.webp")
	if err := os.WriteFile(animPath, []byte("RIFF....WEBPVP8Xanim"), 0644); err != nil {
		t.Fatal(err)
	}
	trm.Twrite([]byte("\x1bPopen '"+animPath+"' rect 2 2 10 5 fit-contain anim\x1b\\"), false)
	if !trm.HasAnim() {
		t.Fatal("anim option did not start an animation")
	}
	// frame 0 drawn immediately
	trm.TickAnim(now)
	if !strings.Contains(trm.LineText(1), "A") {
		t.Fatalf("frame 0 not drawn: %q", trm.LineText(1))
	}
	if !strings.Contains(trm.LineText(5), "WEBP") {
		t.Fatalf("progress line missing: %q", trm.LineText(5))
	}
	// advance past 100ms -> frame 1
	if !trm.TickAnim(now.Add(120 * time.Millisecond)) {
		t.Fatal("expected a change at 120ms")
	}
	if !strings.Contains(trm.LineText(1), "B") {
		t.Fatalf("frame 1 not drawn: %q", trm.LineText(1))
	}
	if !strings.Contains(trm.LineText(5), "66%") {
		t.Fatalf("progress %q", trm.LineText(5))
	}
	// past 200ms -> frame 2 (last)
	if !trm.TickAnim(now.Add(240 * time.Millisecond)) {
		t.Fatal("expected a change at 240ms")
	}
	if !strings.Contains(trm.LineText(1), "C") {
		t.Fatalf("frame 2 not drawn: %q", trm.LineText(1))
	}
	if !strings.Contains(trm.LineText(5), "100%") {
		t.Fatalf("progress %q", trm.LineText(5))
	}
	// past 300ms -> done, holds the last frame
	if trm.TickAnim(now.Add(400 * time.Millisecond)) {
		t.Fatal("animation should be done")
	}
	if trm.HasAnim() {
		t.Fatal("animation still active after play-once finished")
	}
	if !strings.Contains(trm.LineText(1), "C") {
		t.Fatalf("last frame not held: %q", trm.LineText(1))
	}
}

// TestDslOpenAnimCancel verifies writing into an animation rect cancels it.
func TestDslOpenAnimCancel(t *testing.T) {
	cfg := config.Default()
	cfg.Cols = 40
	cfg.Rows = 20
	trm, m := newTermHooks(cfg)
	m.imageCols = 2
	m.imageRows = 2
	m.animFrameGlyphs = [][]Glyph{
		{Glyph{U: 'A'}, Glyph{U: 'A'}, Glyph{U: 'A'}, Glyph{U: 'A'}},
		{Glyph{U: 'B'}, Glyph{U: 'B'}, Glyph{U: 'B'}, Glyph{U: 'B'}},
	}
	m.animDurations = []int{100, 100}
	m.animFrameCount = 2
	m.animOK = true

	animPath := filepath.Join(t.TempDir(), "anim.webp")
	if err := os.WriteFile(animPath, []byte("RIFF....WEBPVP8Xanim"), 0644); err != nil {
		t.Fatal(err)
	}
	trm.Twrite([]byte("\x1bPopen '"+animPath+"' rect 2 2 10 5 fit-contain anim\x1b\\"), false)
	if !trm.HasAnim() {
		t.Fatal("animation did not start")
	}
	// write a character inside the rect (cell 5,5)
	trm.Twrite([]byte("\x1b[5;5HZ"), false)
	if trm.HasAnim() {
		t.Fatal("writing into the animation rect did not cancel it")
	}
}

// TestDscSync2026 verifies the synchronized-output protocol: \033[?2026h stops
// painting (regions queue), \033[?2026l resumes and flushes them as one batch.
func TestDscSync2026(t *testing.T) {
	cfg := config.Default()
	cfg.Cols = 40
	cfg.Rows = 20
	trm, m := newTermHooks(cfg)
	trm.Twrite([]byte("\x1b[?2026h"), false)
	if !trm.IsPaintPaused() {
		t.Fatal("2026h did not stop painting")
	}
	trm.Twrite([]byte("hello"), false)
	if !trm.HasPendingPaint() {
		t.Fatal("regions did not queue while paused")
	}
	trm.Twrite([]byte("\x1b[?2026l"), false)
	if trm.IsPaintPaused() {
		t.Fatal("2026l did not resume painting")
	}
	// the resume flushed the queued batch (drawn synchronously with no worker)
	if trm.HasPendingPaint() {
		t.Fatal("resume did not flush the queued regions")
	}
	trm.Redraw()
	if !strings.Contains(m.screen[0], "hello") {
		t.Fatalf("content not painted after resume: %q", m.screen[0])
	}
}

// TestPaintStopResumeBatches verifies PaintStop/PaintResume batch regions and
// that Draw drains them into one bounding region.
func TestPaintStopResumeBatches(t *testing.T) {
	cfg := config.Default()
	cfg.Cols = 40
	cfg.Rows = 20
	trm, _ := newTermHooks(cfg)
	trm.Draw() // discard initialization damage
	var commits []Region
	trm.SetPaintFn(func() { commits = append(commits, trm.Draw()) })
	trm.PaintStop()
	trm.Twrite([]byte("abc"), false)        // marks row 0
	trm.Twrite([]byte("\x1b[2;5Hx"), false) // marks row 1
	if !trm.HasPendingPaint() {
		t.Fatal("no regions queued while paused")
	}
	trm.PaintResume()
	if len(commits) != 1 {
		t.Fatalf("resume committed %d paints, want 1", len(commits))
	}
	r := commits[0]
	if r.Empty() {
		t.Fatal("commit returned no region")
	}
	if r.Y1 != 0 || r.Y2 < 1 {
		t.Fatalf("bounding region rows = %d..%d, want 0..1", r.Y1, r.Y2)
	}
	if trm.HasPendingPaint() {
		t.Fatal("regions not drained by Draw")
	}
}

func TestDscSync2026IsIdempotent(t *testing.T) {
	trm, _ := newTermHooks(config.Default())
	trm.Draw() // discard initialization damage
	commits := 0
	trm.SetPaintFn(func() {
		commits++
		trm.Draw()
	})

	trm.Twrite([]byte("\x1b[?2026h\x1b[?2026hhello"), false)
	trm.PaintDirty()
	if commits != 0 {
		t.Fatalf("paint committed while synchronized: %d", commits)
	}
	trm.Twrite([]byte("\x1b[?2026l"), false)
	if commits != 1 || trm.IsPaintPaused() {
		t.Fatalf("one reset committed %d paints, paused=%v", commits, trm.IsPaintPaused())
	}
}

func TestSetCharQueuesExactCell(t *testing.T) {
	trm, hooks := newTermHooks(config.Default())
	trm.Draw() // discard initialization damage
	hooks.drawRegions = nil
	// cursor moves queue the cursor cell, and a written char queues exactly
	// its own cell (plus the post-write cursor advance)
	trm.Twrite([]byte("\x1b[3;6HX"), false)
	want := Region{X1: 5, Y1: 2, X2: 5, Y2: 2}
	regions := trm.TakeRegions()
	found := false
	for _, r := range regions {
		if r == want {
			found = true
		}
	}
	if !found {
		t.Fatalf("character cell not damaged: %+v", regions)
	}
	trm.markDirty(want.X1, want.Y1, want.X2, want.Y2)
	trm.Draw()
	if len(hooks.drawRegions) != 1 || hooks.drawRegions[0] != want {
		t.Fatalf("rasterized damage = %+v, want [%+v]", hooks.drawRegions, want)
	}
}
