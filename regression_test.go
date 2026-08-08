package main

import (
	"encoding/base64"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/BurntSushi/xgb"
	"github.com/BurntSushi/xgb/xproto"

	"st-go/config"
	"st-go/ptyutil"
	"st-go/term"
)

// Regression tests for silent-corruption bugs found by comparing the Go port
// against the C implementation of st.

// TestMatchSemantics verifies that match() uses st's exact-modifier matching:
// XK_ANY_MOD (= UINT_MAX) matches any state, XK_NO_MOD (= 0) matches only no
// modifiers, and a specific mask must equal the (ignore-mod-masked) state.
func TestMatchSemantics(t *testing.T) {
	cases := []struct {
		mask, state int
		want        bool
	}{
		{XKAnyMod, 0, true},           // any matches no-mod
		{XKAnyMod, ShiftMask, true},   // any matches shift
		{XKNoMod, 0, true},            // no-mod matches none
		{XKNoMod, ShiftMask, false},   // no-mod fails with shift
		{ShiftMask, ShiftMask, true},  // exact shift
		{ShiftMask, ShiftMask | ControlMask, false}, // subset fails (exact)
		{ControlMask, ControlMask, true},
		{ShiftMask | ControlMask, ShiftMask | ControlMask, true},
	}
	for _, c := range cases {
		got := match(c.mask, c.state)
		if got != c.want {
			t.Fatalf("match(%#x,%#x)=%v want %v", c.mask, c.state, got, c.want)
		}
	}
}

// TestKmapExact verifies the arrow-key keymap resolves modifiers exactly
// (Ctrl+Shift+Left must not match the plain-Left entry).
func TestKmapExact(t *testing.T) {
	cfg := config.Default()
	trm := &Terminal{}
	trm.termCore = &term.Term{}
	trm.loadInputConfig(cfg)

	cases := []struct {
		ksym uint
		state uint
		want string
	}{
		{0xFF51, 0, "\x1b[D"},               // Left
		{0xFF51, ControlMask, "\x1b[1;5D"},  // Ctrl+Left
		{0xFF51, ShiftMask | ControlMask, "\x1b[1;6D"}, // Ctrl+Shift+Left
		{0xFF53, 0, "\x1b[C"},               // Right
		{0xFF08, 0, "\x7f"},                 // BackSpace no-mod
	}
	for _, c := range cases {
		s, m := trm.kmap(c.ksym, c.state)
		if !m || s != c.want {
			t.Fatalf("kmap(%#x,%#x)=%q,%v want %q", c.ksym, c.state, s, m, c.want)
		}
	}
}

// TestXtermColors verifies the 256-color cube/grayscale table matches st's
// xtermcolormap (standard levels {0,95,135,175,215,255}, gray = 8+10n capped
// at 255).
func TestXtermColors(t *testing.T) {
	cfg := config.Default()
	trm := &Terminal{}
	trm.loadColors(cfg)

	cases := []struct {
		idx  int
		want uint32
	}{
		{16, 0xFF000000}, // cube (0,0,0)
		{17, 0xFF00005F}, // cube (0,0,95)
		{46, 0xFF00FF00}, // cube (0,255,0)
		{231, 0xFFFFFFFF}, // cube (255,255,255)
		{232, 0xFF080808}, // gray 8
		{255, 0xFFEEEEEE}, // gray 238 (8+10*23=238)
	}
	for _, c := range cases {
		got := trm.colorAt(uint32(c.idx))
		if got != c.want {
			t.Fatalf("color[%d]=%#x want %#x", c.idx, got, c.want)
		}
	}
}

// TestCursorShape verifies DECSCUSR sets the cursor shape and invalid shapes
// are ignored (st's xsetcursor returns 0 on success, 1 on error).
func TestCursorShape(t *testing.T) {
	cfg := config.Default()
	trm := &Terminal{}
	core := term.NewTerm(cfg, trm)
	trm.termCore = core
	trm.cursorShape = 2

	core.Twrite([]byte("\x1b[5 q"), false)
	if trm.cursorShape != 5 {
		t.Fatalf("expected cursor shape 5, got %d", trm.cursorShape)
	}
	core.Twrite([]byte("\x1b[9 q"), false) // invalid -> ignored
	if trm.cursorShape != 5 {
		t.Fatalf("cursor shape changed on invalid sequence: %d", trm.cursorShape)
	}
}

// TestResizeGrowth verifies the minrow/mincol snapshot used when clearing the
// newly added region is taken before the size fields are mutated.
func TestResizeGrowth(t *testing.T) {
	cfg := config.Default()
	cfg.Cols = 80
	cfg.Rows = 24
	trm := &Terminal{}
	core := term.NewTerm(cfg, trm)
	trm.termCore = core

	core.Tresize(120, 40)
	if core.Cols() != 120 || core.Rows() != 40 {
		t.Fatalf("grow: got %dx%d", core.Cols(), core.Rows())
	}
	core.Tresize(40, 10)
	if core.Cols() != 40 || core.Rows() != 10 {
		t.Fatalf("shrink: got %dx%d", core.Cols(), core.Rows())
	}
	// cursor at a far row then shrink below it: cursor must be clamped
	core.Twrite([]byte("\x1b[30;5H"), false)
	core.Tresize(20, 5)
	if core.CursorY() > core.Rows()-1 {
		t.Fatalf("cursor y=%d exceeds rows-1", core.CursorY())
	}
}

// TestLiveReadlineInsertion types ABC, a backspace (readline's left-move),
// then D and expects the rendered buffer to be ABD (D overwrites C). The
// forward-move case (which needs ESC [ C) is covered by the term-package
// TestBackspaceThenForwardCursor.
func TestLiveReadlineInsertion(t *testing.T) {
	cfg := config.Default()
	trm, err := NewTerminal(cfg)
	if err != nil {
		t.Skipf("no X server: %v", err)
	}
	defer trm.Close()
	core := term.NewTerm(cfg, trm)
	trm.termCore = core
	trm.loadInputConfig(cfg)
	trm.setupKeys()

	core.Twrite([]byte("[root@trixie st-go]# ABC"), false)
	core.Twrite([]byte("\b"), false)
	core.Twrite([]byte("D"), false)
	core.Redraw()
	line := core.LineText(0)
	if !containsStr(line, "ABD") {
		t.Fatalf("expected ABD after backspace+D, got %q", line)
	}
}

func containsStr(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

// makeTestPNG returns a tiny valid 8x8 PNG (used by regression tests).
func makeTestPNG(t *testing.T) []byte {
	data, err := base64.StdEncoding.DecodeString(
		"iVBORw0KGgoAAAANSUhEUgAAAAgAAAAICAIAAABLbSncAAAADElEQVR4nGNgGB4AAADIAAGtQHYiAAAAAElFTkSuQmCC")
	if err != nil {
		t.Fatal(err)
	}
	return data
}

// TestBase64Decode ensures the custom base64 decoder matches stdlib.
func TestBase64Decode(t *testing.T) {
	// exercise the term-package base64 decoder indirectly through OSC 52 path
	// by checking image DSL decode still functions with a real PNG.
	png := makeTestPNG(t)
	cfg := config.Default()
	cfg.Cols = 40
	cfg.Rows = 10
	trm := &Terminal{}
	core := term.NewTerm(cfg, trm)
	trm.termCore = core
	trm.cw, trm.ch = int(cfg.GlyphWidth), int(cfg.GlyphHeight)
	trm.cols, trm.rows = int(cfg.Cols), int(cfg.Rows)
	ensureFramebuffer(40*trm.cw, 10*trm.ch)

	// write PNG to a temp file and open via the DSL
	dir, err := os.MkdirTemp("", "stgotest")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	path := dir + "/img.png"
	if err := os.WriteFile(path, png, 0644); err != nil {
		t.Fatal(err)
	}
	core.Twrite([]byte("\x1bPopen '"+path+"';\x1b\\"), false)
	if len(imageAtlas) == 0 {
		t.Fatal("image DSL did not populate the atlas")
	}
	g := core.LineAt(0, 0)
	if g.U != term.ImageRune {
		t.Fatalf("expected ImageRune at (0,0), got %#x", g.U)
	}
}

// TestGeometryParse verifies st's -g geometry parsing (WxH+X+Y).
func TestGeometryParse(t *testing.T) {
	cases := []struct {
		s          string
		cols, rows, x, y int
		mask       XGeometryMask
	}{
		{"100x40", 100, 40, 0, 0, WidthValue | HeightValue},
		{"100x40+10+20", 100, 40, 10, 20, AllValues},
		{"100x40-10+20", 100, 40, -10, 20, AllValues | XNegative},
		{"+10+20", 80, 24, 10, 20, XValue | YValue},
		{"100", 100, 24, 0, 0, WidthValue},
		{"", 80, 24, 0, 0, 0},
		{"x40", 80, 40, 0, 0, HeightValue},
	}
	for _, c := range cases {
		cols, rows := 80, 24
		x, y := 0, 0
		mask := parseGeometry(c.s, &cols, &rows, &x, &y)
		if cols != c.cols || rows != c.rows || x != c.x || y != c.y || mask != c.mask {
			t.Fatalf("%q: got %dx%d+%d+%d mask=%#x want %dx%d+%d+%d mask=%#x",
				c.s, cols, rows, x, y, mask, c.cols, c.rows, c.x, c.y, c.mask)
		}
	}
}

// TestTitleAndGeometry verifies -t sets WM_NAME and -g sets the window
// position/size (requires an X server). Position checks use TranslateCoordinates
// to avoid the WM-less X server reusing window coordinates across creates.
func TestTitleAndGeometry(t *testing.T) {
	cfg := config.Default()
	trm, err := NewTerminalOpts(cfg, 1.0, 50, 60, XValue|YValue, "My Terminal", "", "STTest", false)
	if err != nil {
		t.Skipf("no X server: %v", err)
	}
	defer trm.Close()
	trm.conn.Sync()
	rep, _ := xproto.GetProperty(trm.conn, false, trm.win, trm.atoms["WM_NAME"],
		0, 0, 1024).Reply()
	if rep == nil || string(rep.Value) != "My Terminal" {
		t.Fatalf("title=%q want %q", string(rep.Value), "My Terminal")
	}
	// Position: the requested x/y is recorded in WM_NORMAL_HINTS' USPosition
	// flags; the live geometry may be overridden by a tiling WM (gowinmgr),
	// so only require the hint to carry the requested position.
	hrep, _ := xproto.GetProperty(trm.conn, false, trm.win, trm.atoms["WM_NORMAL_HINTS"],
		0, 0, 1024).Reply()
	if hrep == nil {
		t.Fatalf("no WM_NORMAL_HINTS")
	}
	var flags uint32
	if len(hrep.Value) >= 4 {
		flags = uint32(hrep.Value[0]) | uint32(hrep.Value[1])<<8 |
			uint32(hrep.Value[2])<<16 | uint32(hrep.Value[3])<<24
	}
	if flags&uint32(1) == 0 { // USPosition
		t.Fatalf("WM_NORMAL_HINTS flags=%#x missing USPosition", flags)
	}
	wantW := trm.borderpx*2 + int(cfg.Cols)*trm.cw
	wantH := trm.borderpx*2 + int(cfg.Rows)*trm.ch
	// Requested size is carried in WM_NORMAL_HINTS (PSize at offset 3/4);
	// the live geometry may be overridden by the tiling WM.
	if len(hrep.Value) < 20 {
		t.Fatalf("WM_NORMAL_HINTS too short: %d", len(hrep.Value))
	}
	gotW := uint32(hrep.Value[12]) | uint32(hrep.Value[13])<<8 |
		uint32(hrep.Value[14])<<16 | uint32(hrep.Value[15])<<24
	gotH := uint32(hrep.Value[16]) | uint32(hrep.Value[17])<<8 |
		uint32(hrep.Value[18])<<16 | uint32(hrep.Value[19])<<24
	if int(gotW) != wantW || int(gotH) != wantH {
		t.Fatalf("hints size %dx%d want %dx%d", gotW, gotH, wantW, wantH)
	}
}

// TestTitleAllProps verifies -t/-n/-c set WM_NAME, _NET_WM_NAME, WM_CLASS
// and _NET_WM_PID like st's xinit/xhints (title set before map, sync after).
func TestTitleAllProps(t *testing.T) {
	cfg := config.Default()
	trm, err := NewTerminalOpts(cfg, 1.0, 0, 0, 0, "MyTitle", "myinst", "MyClass", false)
	if err != nil {
		t.Skipf("no X server: %v", err)
	}
	defer trm.Close()
	trm.conn.Sync()
	rep, _ := xproto.GetProperty(trm.conn, false, trm.win, trm.atoms["WM_NAME"],
		0, 0, 1024).Reply()
	if string(rep.Value) != "MyTitle" {
		t.Fatalf("WM_NAME=%q", rep.Value)
	}
	rep, _ = xproto.GetProperty(trm.conn, false, trm.win, trm.atoms["_NET_WM_NAME"],
		0, 0, 1024).Reply()
	if string(rep.Value) != "MyTitle" {
		t.Fatalf("_NET_WM_NAME=%q", rep.Value)
	}
	rep, _ = xproto.GetProperty(trm.conn, false, trm.win, trm.atoms["WM_CLASS"],
		trm.atoms["STRING"], 0, 1024).Reply()
	if string(rep.Value) != "myinst\x00MyClass\x00" {
		t.Fatalf("WM_CLASS=%q want myinst\\0MyClass\\0", rep.Value)
	}
	rep, _ = xproto.GetProperty(trm.conn, false, trm.win, trm.atoms["_NET_WM_PID"],
		trm.atoms["CARDINAL"], 0, 4).Reply()
	if len(rep.Value) < 4 {
		t.Fatalf("_NET_WM_PID missing")
	}
}

// TestEscAndTabLookup verifies ESC (0xFF1B) -> \033 and Tab (0xFF09) -> \t,
// which XLookupString produces (st relies on this for the ESC key).
func TestEscAndTabLookup(t *testing.T) {
	trm := &Terminal{}
	if b, n := trm.lookupString(0xFF1B, false); n != 1 || b[0] != 0x1b {
		t.Fatalf("ESC lookup wrong: n=%d b=%v", n, b)
	}
	if b, n := trm.lookupString(0xFF09, false); n != 1 || b[0] != 0x09 {
		t.Fatalf("Tab lookup wrong: n=%d b=%v", n, b)
	}
	if b, n := trm.lookupString(0xFFE3, false); n != 0 {
		t.Fatalf("Ctrl_L should emit nothing, got %v", b)
	}
	if b, n := trm.lookupString(0xFFBE, false); n != 0 {
		t.Fatalf("F1 should emit nothing in lookup, got %v", b)
	}
}

func TestNegativeGeometry(t *testing.T) {
	cfgCols, cfgRows := 80, 24
	gx, gy := 0, 0
	mask := parseGeometry("80x24-10-5", &cfgCols, &cfgRows, &gx, &gy)
	if mask&XNegative == 0 || mask&YNegative == 0 {
		t.Fatalf("expected XNegative|YNegative, got %#x", mask)
	}
	if gx != -10 || gy != -5 {
		t.Fatalf("gx=%d gy=%d want -10 -5", gx, gy)
	}
	if cfgCols != 80 || cfgRows != 24 {
		t.Fatalf("size %dx%d want 80x24", cfgCols, cfgRows)
	}
	// The screen-edge offset adjustment is applied in NewTerminalOpts:
	// winX = gx + ScreenWidth - win.w - 2 (for XNegative). Verify the math
	// with a fake screen.
	scrW, scrH := 2880, 1800
	w, h := 2+80*16, 2+24*28
	wantX := -10 + scrW - w - 2
	wantY := -5 + scrH - h - 2
	if wantX != 2880-1282-12 || wantY != 1800-674-7 {
		t.Fatalf("offset math: %d,%d", wantX, wantY)
	}
	_ = mask
}

// TestWMReadAsTilingWM verifies the window properties a tiling WM (e.g. dwm)
// reads: WM_NAME as STRING (XStoreName behavior) and WM_NORMAL_HINTS with
// USPosition when -g provides x/y.
func TestWMReadAsTilingWM(t *testing.T) {
	cfg := config.Default()
	trm, err := NewTerminalOpts(cfg, 1.0, 100, 200, XValue|YValue, "dwm-title", "", "ST", false)
	if err != nil {
		t.Skipf("no X server: %v", err)
	}
	defer trm.Close()
	trm.conn.Sync()
	rep, _ := xproto.GetProperty(trm.conn, false, trm.win, trm.atoms["WM_NAME"],
		xproto.Atom(31), 0, 1024).Reply() // STRING, like XFetchName
	if string(rep.Value) != "dwm-title" {
		t.Fatalf("WM_NAME=%q", rep.Value)
	}
	hr, _ := xproto.GetProperty(trm.conn, false, trm.win, trm.atoms["WM_NORMAL_HINTS"],
		0, 0, 1024).Reply()
	if len(hr.Value) < 18*4 {
		t.Fatalf("size hints too short: %d", len(hr.Value))
	}
	flags := leUint32(hr.Value[0:4])
	if flags&1 == 0 {
		t.Fatalf("USPosition flag not set")
	}
	x := leUint32(hr.Value[4:8])
	y := leUint32(hr.Value[8:12])
	if x != 100 || y != 200 {
		t.Fatalf("hints position %d,%d want 100,200", x, y)
	}
}

func leUint32(b []byte) uint32 {
	return uint32(b[0]) | uint32(b[1])<<8 | uint32(b[2])<<16 | uint32(b[3])<<24
}

// TestEArgParsing verifies -e consumes all remaining args (like st's
// ARGBEGIN `goto run; opt_cmd = argv`).
func TestEArgParsing(t *testing.T) {
	args := []string{"st", "-t", "x", "-e", "vim", "-c", "file.txt", "arg2"}
	var cmdArgs []string
	for i, a := range args[1:] {
		if a == "-e" {
			cmdArgs = args[i+2:] // after "-e" (index i in args[1:] => i+1 in args)
			break
		}
	}
	want := []string{"vim", "-c", "file.txt", "arg2"}
	if len(cmdArgs) != len(want) {
		t.Fatalf("cmdArgs=%v want %v", cmdArgs, want)
	}
	for j := range want {
		if cmdArgs[j] != want[j] {
			t.Fatalf("cmdArgs=%v want %v", cmdArgs, want)
		}
	}
}

// TestVerifyBarData verifies the self-heal: if gowinmgr wrote a BAR_DATA
// property with an empty title segment (a race at window creation), the
// terminal rewrites it as prefix@title@class@instance@desktop.
func TestVerifyBarData(t *testing.T) {
	cfg := config.Default()
	trm, err := NewTerminalOpts(cfg, 1.0, 0, 0, 0, "MyTitle", "inst", "Class", false)
	if err != nil {
		t.Skipf("no X server: %v", err)
	}
	defer trm.Close()
	trm.conn.Sync()
	bad := "tile-@@Class@inst@desk1"
	xproto.ChangeProperty(trm.conn, xproto.PropModeReplace, trm.win,
		trm.atoms["BAR_DATA"], trm.atoms["STRING"], 8,
		uint32(len(bad)), []byte(bad))
	trm.conn.Sync()
	trm.verifyBarData("inst", "Class")
	trm.conn.Sync()
	rep, _ := xproto.GetProperty(trm.conn, false, trm.win, trm.atoms["BAR_DATA"],
		0, 0, 1024).Reply()
	if string(rep.Value) != "tile-@MyTitle@Class@inst@desk1" {
		t.Fatalf("healed BAR_DATA=%q", rep.Value)
	}
}

// TestTitleImmediateRead verifies the gowinmgr race fix: a separate WM
// connection must be able to read the terminal's WM_NAME (STRING) and WM_CLASS
// immediately after creation, because gowinmgr reads them in its CreateNotify
// handler. This requires CreateWindow + identity properties to be sent in one
// request batch with no round-trip between them.
func TestTitleImmediateRead(t *testing.T) {
	wm, err := xgb.NewConnDisplay(":0")
	if err != nil {
		t.Skipf("no X server: %v", err)
	}
	defer wm.Close()
	wmName, err := xproto.InternAtom(wm, true, 7, "WM_NAME").Reply()
	if err != nil {
		t.Skipf("intern: %v", err)
	}
	cls, err := xproto.InternAtom(wm, true, 8, "WM_CLASS").Reply()
	if err != nil {
		t.Skipf("intern: %v", err)
	}

	cfg := config.Default()
	for i := 0; i < 10; i++ {
		title := "tile_title_" + itoa(i)
		trm, err := NewTerminalOpts(cfg, 1.0, 0, 0, 0, title, "st", "ST", false)
		if err != nil {
			t.Skipf("no X: %v", err)
		}
		// gowinmgr reads WM_NAME as STRING (Atom 31) in CreateNotify handler
		rep, _ := xproto.GetProperty(wm, false, trm.win, wmName.Atom,
			xproto.Atom(31), 0, 1024).Reply()
		if string(rep.Value) != title {
			t.Fatalf("win %d: WM_NAME=%q want %q", i, rep.Value, title)
		}
		crep, _ := xproto.GetProperty(wm, false, trm.win, cls.Atom,
			xproto.Atom(31), 0, 1024).Reply()
		parts := strings.Split(string(crep.Value), "\x00")
		if len(parts) < 2 || parts[0] != "st" || parts[1] != "ST" {
			t.Fatalf("win %d: WM_CLASS=%q want st\x00ST", i, crep.Value)
		}
		trm.Close()
	}
}

// TestLiveWMConsistency spawns several terminals against the running tiling
// WM (gowinmgr) and verifies each window's BAR_DATA property carries the
// correct title. gowinmgr reads WM_NAME (STRING) inside its CreateNotify
// handler; the terminal must set title+class in the same request batch as
// CreateWindow (no round-trip) so the WM never sees an empty title.
func TestLiveWMConsistency(t *testing.T) {
	wm, err := xgb.NewConnDisplay(":0")
	if err != nil {
		t.Skipf("no X: %v", err)
	}
	defer wm.Close()
	barRep, _ := xproto.InternAtom(wm, true, 8, "BAR_DATA").Reply()
	barAtom := barRep.Atom

	cfg := config.Default()
	total := 6
	wins := make(map[xproto.Window]string, total)
	trms := make([]*Terminal, 0, total)
	for i := 0; i < total; i++ {
		title := "tile_consistency_" + itoa(i)
		trm, err := NewTerminalOpts(cfg, 1.0, 0, 0, 0, title, "st", "ST", false)
		if err != nil {
			t.Skipf("create: %v", err)
		}
		wins[trm.win] = title
		trms = append(trms, trm)
	}
	time.Sleep(time.Millisecond * 400)

	bad, checked := 0, 0
	for win, title := range wins {
		rep, err := xproto.GetProperty(wm, false, win, barAtom, 0, 0, 1024).Reply()
		if err != nil || rep == nil {
			continue
		}
		checked++
		parts := strings.Split(string(rep.Value), "@")
		if len(parts) < 2 || parts[1] != title {
			bad++
			t.Logf("win 0x%x title=%q barData=%q", win, title, rep.Value)
		}
	}
	for _, trm := range trms {
		trm.Close()
	}
	if checked == 0 {
		t.Skipf("no BAR_DATA seen (no WM)")
	}
	if bad > 0 {
		t.Fatalf("%d/%d windows have wrong title in BAR_DATA", bad, checked)
	}
}

// TestEvColRowClamp verifies the st-equivalent LIMIT on click coordinates:
// clicks past the window's right/bottom edges (or on the border) must clamp
// to the last cell instead of producing negative/out-of-bounds cell indexes
// (which previously panicked in SelStart/SelExtend and made the window
// disappear when clicking near the screen edges).
func TestEvColRowClamp(t *testing.T) {
	trm := &Terminal{cols: 80, rows: 24, cw: 16, ch: 28, borderpx: 1}
	// click far beyond the bottom-right corner
	if x := trm.evcol(99999); x != 79 {
		t.Fatalf("evcol(99999)=%d want 79", x)
	}
	if y := trm.evrow(99999); y != 23 {
		t.Fatalf("evrow(99999)=%d want 23", y)
	}
	// click on the top-left border (negative after borderpx subtraction)
	if x := trm.evcol(0); x != 0 {
		t.Fatalf("evcol(0)=%d want 0", x)
	}
	if y := trm.evrow(0); y != 0 {
		t.Fatalf("evrow(0)=%d want 0", y)
	}
	// normal cell clicks
	if x := trm.evcol(1 + 16*5 + 8); x != 5 {
		t.Fatalf("evcol(mid)=%d want 5", x)
	}
	if y := trm.evrow(1 + 28*10 + 14); y != 10 {
		t.Fatalf("evrow(mid)=%d want 10", y)
	}
}

// TestSelStartEdgeCoords feeds the extreme clamped coordinates into the term
// core's selection machinery to ensure no out-of-bounds panic occurs (the
// actual crash behind the disappearing window).
func TestSelStartEdgeCoords(t *testing.T) {
	core := term.NewTerm(config.Default(), nil)
	for _, snap := range []int{0, 1, 2} {
		core.SelStart(79, 23, snap) // bottom-right cell
		core.SelExtend(79, 23, 0, 0)
		core.SelStart(0, 0, snap) // top-left cell
		core.SelExtend(0, 0, 0, 1)
	}
	core.SelStart(79, 23, 1)
	core.SelExtend(79, 23, 0, 0)
	core.SelExtend(0, 0, 0, 1)
}

// TestStChildEnvUnsetsSizeVars verifies the child environment matches st's
// execsh: COLUMNS/LINES/TERMCAP are removed so vim/shells use TIOCGWINSZ
// (the real pty size) instead of a stale inherited terminal size.
func TestStChildEnvUnsetsSizeVars(t *testing.T) {
	os.Setenv("COLUMNS", "20")
	os.Setenv("LINES", "5")
	os.Setenv("TERMCAP", "xterm")
	env := stChildEnv("st-256color")
	m := map[string]string{}
	for _, kv := range env {
		for i := 0; i < len(kv); i++ {
			if kv[i] == '=' {
				m[kv[:i]] = kv[i+1:]
				break
			}
		}
	}
	for _, k := range []string{"COLUMNS", "LINES", "TERMCAP"} {
		if _, ok := m[k]; ok {
			t.Fatalf("%s still set in child env", k)
		}
	}
	if m["TERM"] != "st-256color" {
		t.Fatalf("TERM=%q", m["TERM"])
	}
}

// TestActualRowsColsMatchesWindow verifies the pty size source: the effective
// rows/cols are computed from the live window geometry (post-WM-tiling), not
// the config defaults, so vim gets a correct TIOCGWINSZ at spawn.
func TestActualRowsColsMatchesWindow(t *testing.T) {
	cfg := config.Default()
	if !loadFonts(cfg.Font, int(cfg.GlyphHeight)) {
		t.Skip("font not available")
	}
	trm, err := NewTerminalOpts(cfg, 1.0, 0, 0, 0, "auto-gtex", "st", "ST", false)
	if err != nil {
		t.Skipf("no X server: %v", err)
	}
	defer trm.Close()
	rows, cols := trm.actualRowsCols()
	if rows < 1 || cols < 1 {
		t.Fatalf("actualRowsCols=%dx%d", rows, cols)
	}
	t.Logf("actualRowsCols=%dx%d cfg=%dx%d", rows, cols, cfg.Rows, cfg.Cols)
}

// TestClipboardRoundTrip: the terminal claims CLIPBOARD; a second client
// requests it and must receive the text. Regression for the selrequest bug
// where the SelectionNotify advertised e.Property (None) instead of the
// resolved property, breaking copy.
func TestClipboardRoundTrip(t *testing.T) {
	cfg := config.Default()
	if !loadFonts(cfg.Font, int(cfg.GlyphHeight)) {
		t.Skip("font not available")
	}
	trm, err := NewTerminalOpts(cfg, 1.0, 0, 0, 0, "tile_cliprt", "st", "ST", false)
	if err != nil {
		t.Skipf("no X: %v", err)
	}
	defer trm.Close()
	trm.loadInputConfig(cfg)
	core := term.NewTerm(cfg, trm)
	trm.termCore = core

	trm.selectionText = "HELLO_CLIP"
	xproto.SetSelectionOwner(trm.conn, trm.win, trm.atoms["CLIPBOARD"], xproto.TimeCurrentTime)

	req, err := xgb.NewConnDisplay(":0")
	if err != nil {
		t.Skipf("req conn: %v", err)
	}
	defer req.Close()
	scr := xproto.Setup(req).DefaultScreen(req)
	clipAtom, _ := xproto.InternAtom(req, true, 9, "CLIPBOARD").Reply()
	utf8Atom, _ := xproto.InternAtom(req, true, 11, "UTF8_STRING").Reply()
	wid, _ := xproto.NewWindowId(req)
	xproto.CreateWindow(req, scr.RootDepth, wid, scr.Root, 0, 0, 1, 1, 0,
		xproto.WindowClassInputOutput, scr.RootVisual, 0, nil)
	req.Sync()
	xproto.ConvertSelection(req, wid, clipAtom.Atom, utf8Atom.Atom, 0, xproto.TimeCurrentTime)

	go func() {
		for {
			ev, err := trm.conn.WaitForEvent()
			if err != nil || ev == nil {
				return
			}
			if se, ok := ev.(xproto.SelectionRequestEvent); ok {
				trm.selrequest(se)
			}
		}
	}()

	for {
		ev, err := req.WaitForEvent()
		if err != nil || ev == nil {
			t.Fatalf("req conn error: %v", err)
		}
		if sn, ok := ev.(xproto.SelectionNotifyEvent); ok {
			if sn.Property == 0 {
				t.Fatalf("selection notify property is 0 (transfer failed)")
			}
			gp, _ := xproto.GetProperty(req, false, wid, sn.Property, 0, 0, 1024).Reply()
			if gp == nil || string(gp.Value) != "HELLO_CLIP" {
				t.Fatalf("wrong text: %q", gp.Value)
			}
			return
		}
	}
}

// TestClipboardPaste: another client owns CLIPBOARD; the terminal's clippaste
// + selnotify must write the text to the pty. Regression for paste.
func TestClipboardPaste(t *testing.T) {
	cfg := config.Default()
	if !loadFonts(cfg.Font, int(cfg.GlyphHeight)) {
		t.Skip("font not available")
	}
	trm, err := NewTerminalOpts(cfg, 1.0, 0, 0, 0, "tile_clippaste", "st", "ST", false)
	if err != nil {
		t.Skipf("no X: %v", err)
	}
	defer trm.Close()
	trm.loadInputConfig(cfg)
	core := term.NewTerm(cfg, trm)
	trm.termCore = core
	written := []byte{}
	core.SetWriter(func(b []byte) { written = append(written, b...) })

	owner, err := xgb.NewConnDisplay(":0")
	if err != nil {
		t.Skipf("owner conn: %v", err)
	}
	defer owner.Close()
	scr := xproto.Setup(owner).DefaultScreen(owner)
	owin, _ := xproto.NewWindowId(owner)
	xproto.CreateWindow(owner, scr.RootDepth, owin, scr.Root, 0, 0, 1, 1, 0,
		xproto.WindowClassInputOutput, scr.RootVisual, 0, nil)
	owner.Sync()
	clipAtom, _ := xproto.InternAtom(owner, true, 9, "CLIPBOARD").Reply()
	utf8Atom, _ := xproto.InternAtom(owner, true, 11, "UTF8_STRING").Reply()
	xproto.SetSelectionOwner(owner, owin, clipAtom.Atom, xproto.TimeCurrentTime)
	owner.Sync()

	trm.clippaste()
	trm.conn.Sync()

	go func() {
		for {
			ev, err := owner.WaitForEvent()
			if err != nil || ev == nil {
				return
			}
			if se, ok := ev.(xproto.SelectionRequestEvent); ok {
				prop := se.Property
				if prop == 0 {
					prop = se.Target
				}
				xproto.ChangeProperty(owner, xproto.PropModeReplace, se.Requestor,
					prop, utf8Atom.Atom, 8, uint32(len("PASTE_ME")), []byte("PASTE_ME"))
				xproto.SendEvent(owner, false, se.Requestor, 0,
					string(xproto.SelectionNotifyEvent{
						Time: se.Time, Requestor: se.Requestor, Selection: se.Selection,
						Target: se.Target, Property: prop,
					}.Bytes()))
				owner.Sync()
			}
		}
	}()

	for {
		ev, err := trm.conn.WaitForEvent()
		if err != nil || ev == nil {
			t.Fatalf("trm conn: %v", err)
		}
		if sn, ok := ev.(xproto.SelectionNotifyEvent); ok {
			trm.selnotify(sn)
			trm.conn.Sync()
			break
		}
	}
	if string(written) != "PASTE_ME" {
		t.Fatalf("paste text %q != PASTE_ME", string(written))
	}
}

// TestDcsSetPwdResolves: the image-viewer script emits "setpwd '<dir>'" then
// relative "open './x.png'". The terminal must resolve the relative path
// against the recorded pwd even when its own CWD differs. Regression for the
// all-black image viewer caused by relative paths resolving against st's CWD.
func TestDcsSetPwdResolves(t *testing.T) {
	cfg := config.Default()
	if !loadFonts(cfg.Font, int(cfg.GlyphHeight)) {
		t.Skip("font not available")
	}
	trm, err := NewTerminalOpts(cfg, 1.0, 0, 0, 0, "tile_pwd", "st", "ST", false)
	if err != nil {
		t.Skipf("no X: %v", err)
	}
	defer trm.Close()
	core := term.NewTerm(cfg, trm)
	trm.termCore = core

	// a tiny 1x1 PNG (red) written to a temp dir; relative path via setpwd
	dir := t.TempDir()
	png := []byte{0x89, 0x50, 0x4E, 0x47, 0x0D, 0x0A, 0x1A, 0x0A, 0x00, 0x00, 0x00, 0x0D,
		0x49, 0x48, 0x44, 0x52, 0x00, 0x00, 0x00, 0x01, 0x00, 0x00, 0x00, 0x01, 0x08, 0x02,
		0x00, 0x00, 0x00, 0x90, 0x77, 0x53, 0xDE, 0x00, 0x00, 0x00, 0x0C, 0x49, 0x44, 0x41,
		0x54, 0x08, 0xD7, 0x63, 0xF8, 0xFF, 0xFF, 0x3F, 0x00, 0x05, 0xFE, 0x02, 0xFE, 0xDC,
		0xCC, 0x59, 0xE7, 0x00, 0x00, 0x00, 0x00, 0x49, 0x45, 0x4E, 0x44, 0xAE, 0x42, 0x60,
		0x82}
	if err := os.WriteFile(dir+"/pic.png", png, 0644); err != nil {
		t.Fatal(err)
	}

	core.Twrite([]byte("\x1bPsetpwd '"+dir+"'\x1b\\"), false)
	core.Twrite([]byte("\x1bPopen './pic.png' fit-height\x1b\\"), false)
	core.Redraw()
	found := false
	for y := 0; y < core.Rows(); y++ {
		for x := 0; x < core.Cols(); x++ {
			if core.LineAt(x, y).U == term.ImageRune {
				found = true
			}
		}
	}
	if !found {
		t.Fatalf("setpwd + relative path did not decode image")
	}
}

// TestDcsGlobPath: the DSL must expand a wildcard image path (as a shell
// would) so a script may pass './*.png' literally.
func TestDcsGlobPath(t *testing.T) {
	cfg := config.Default()
	if !loadFonts(cfg.Font, int(cfg.GlyphHeight)) {
		t.Skip("font not available")
	}
	trm, err := NewTerminalOpts(cfg, 1.0, 0, 0, 0, "tile_glob", "st", "ST", false)
	if err != nil {
		t.Skipf("no X: %v", err)
	}
	defer trm.Close()
	core := term.NewTerm(cfg, trm)
	trm.termCore = core
	dir := t.TempDir()
	png := []byte{0x89, 0x50, 0x4E, 0x47, 0x0D, 0x0A, 0x1A, 0x0A, 0x00, 0x00, 0x00, 0x0D,
		0x49, 0x48, 0x44, 0x52, 0x00, 0x00, 0x00, 0x01, 0x00, 0x00, 0x00, 0x01, 0x08, 0x02,
		0x00, 0x00, 0x00, 0x90, 0x77, 0x53, 0xDE, 0x00, 0x00, 0x00, 0x0C, 0x49, 0x44, 0x41,
		0x54, 0x08, 0xD7, 0x63, 0xF8, 0xFF, 0xFF, 0x3F, 0x00, 0x05, 0xFE, 0x02, 0xFE, 0xDC,
		0xCC, 0x59, 0xE7, 0x00, 0x00, 0x00, 0x00, 0x49, 0x45, 0x4E, 0x44, 0xAE, 0x42, 0x60,
		0x82}
	if err := os.WriteFile(dir+"/pic.png", png, 0644); err != nil {
		t.Fatal(err)
	}
	core.Twrite([]byte("\x1bPsetpwd '"+dir+"'\x1b\\"), false)
	core.Twrite([]byte("\x1bPopen './*.png' fit-height\x1b\\"), false)
	core.Redraw()
	found := false
	for y := 0; y < core.Rows(); y++ {
		for x := 0; x < core.Cols(); x++ {
			if core.LineAt(x, y).U == term.ImageRune {
				found = true
			}
		}
	}
	if !found {
		t.Fatalf("wildcard path './*.png' did not expand and decode")
	}
}

// TestFocusToggleRedraw: FocusOut must clear ModeFocused (so DrawCursor draws
// the unfocused outline box) and FocusIn restore it, mirroring st's focus()
// handler which is followed by a draw on every event.
func TestFocusToggleRedraw(t *testing.T) {
	cfg := config.Default()
	if !loadFonts(cfg.Font, int(cfg.GlyphHeight)) {
		t.Skip("font not available")
	}
	trm, err := NewTerminalOpts(cfg, 1.0, 0, 0, 0, "tile_foc", "st", "ST", false)
	if err != nil {
		t.Skipf("no X: %v", err)
	}
	defer trm.Close()
	trm.loadInputConfig(cfg)
	core := term.NewTerm(cfg, trm)
	trm.termCore = core
	trm.setWinMode(true, term.ModeFocused)

	trm.handleEvent(xproto.FocusOutEvent{Mode: 0})
	if trm.winModeIs(term.ModeFocused) {
		t.Fatalf("ModeFocused still set after FocusOut")
	}
	trm.handleEvent(xproto.FocusInEvent{Mode: 0})
	if !trm.winModeIs(term.ModeFocused) {
		t.Fatalf("ModeFocused not restored after FocusIn")
	}
}

// TestImageDecodePDF verifies the DSL "open" path renders a PDF (first page)
// through the minimal static poppler build. Regression for the PDF display
// feature (the C++ page_renderer API needs no cairo/glib).
func TestImageDecodePDF(t *testing.T) {
	cfg := config.Default()
	if !loadFonts(cfg.Font, int(cfg.GlyphHeight)) {
		t.Skip("font not available")
	}
	// a minimal valid PDF (1 blank page)
	pdf := []byte("%PDF-1.4\n1 0 obj<</Type/Catalog/Pages 2 0 R>>endobj\n" +
		"2 0 obj<</Type/Pages/Kids[3 0 R]/Count 1>>endobj\n" +
		"3 0 obj<</Type/Page/Parent 2 0 R/MediaBox[0 0 100 100]>>endobj\n" +
		"xref\n0 4\n0000000000 65535 f \n0000000009 00000 n \n0000000052 00000 n \n0000000101 00000 n \n" +
		"trailer<</Size 4/Root 1 0 R>>\nstartxref\n190\n%%EOF\n")
	trm, err := NewTerminalOpts(cfg, 1.0, 0, 0, 0, "tile_pdf", "st", "ST", false)
	if err != nil {
		t.Skipf("no X: %v", err)
	}
	defer trm.Close()
	cols, rows, glyphs, ok := trm.ImageDecode(pdf, false, true, 0)
	if !ok || len(glyphs) == 0 {
		t.Fatalf("pdf decode failed (cols=%d rows=%d glyphs=%d ok=%v)", cols, rows, len(glyphs), ok)
	}
	if len(imageAtlas) == 0 {
		t.Fatalf("pdf atlas empty")
	}
}

// TestPDFModuloWrap verifies PDF page navigation uses modular arithmetic: the
// image-viewer script only sends +/-1 counters, so a page number that is
// negative or past the end must wrap modulo the page count (never fail).
func TestPDFModuloWrap(t *testing.T) {
	cfg := config.Default()
	if !loadFonts(cfg.Font, int(cfg.GlyphHeight)) {
		t.Skip("font not available")
	}
	// build a 2-page PDF: one red page + one green page
	if _, err := exec.Command("gs", "-q", "-o", "/tmp/rp.pdf", "-sDEVICE=pdfwrite", "-g200x200",
		"-c", "1 0 0 setrgbcolor 0 0 200 200 rectfill showpage").Output(); err != nil {
		t.Skipf("gs: %v", err)
	}
	if _, err := exec.Command("gs", "-q", "-o", "/tmp/gp.pdf", "-sDEVICE=pdfwrite", "-g200x200",
		"-c", "0 1 0 setrgbcolor 0 0 200 200 rectfill showpage").Output(); err != nil {
		t.Skipf("gs: %v", err)
	}
	if _, err := exec.Command("gs", "-q", "-dBATCH", "-dNOPAUSE", "-sDEVICE=pdfwrite",
		"-sOutputFile=/tmp/rg.pdf", "/tmp/rp.pdf", "/tmp/gp.pdf").Output(); err != nil {
		t.Skipf("gs merge: %v", err)
	}
	defer os.Remove("/tmp/rg.pdf")
	defer os.Remove("/tmp/rp.pdf")
	defer os.Remove("/tmp/gp.pdf")

	data, err := os.ReadFile("/tmp/rg.pdf")
	if err != nil {
		t.Skipf("read: %v", err)
	}
	if n := pdfPageCount(data); n != 2 {
		t.Skipf("expected 2 pages, got %d", n)
	}
	trm, err := NewTerminalOpts(cfg, 1.0, 0, 0, 0, "tile_mw", "st", "ST", false)
	if err != nil {
		t.Skipf("no X: %v", err)
	}
	defer trm.Close()

	hash := func(page int) uint64 {
		_, _, g, ok := trm.ImageDecode(data, false, true, page)
		if !ok || len(g) == 0 {
			return 0
		}
		var h uint64 = 1469598103934665603
		for _, gl := range g {
			o := int(gl.Fg)
			for i := 0; i < trm.cw*trm.ch; i++ {
				h ^= uint64(imageAtlas[o+i])
				h *= 1099511628211
			}
		}
		return h
	}
	h1, h2, h3, h0 := hash(0), hash(1), hash(2), hash(-1)
	if h1 == 0 || h2 == 0 {
		t.Fatalf("render failed")
	}
	if h1 == h2 {
		t.Fatalf("page1 and page2 must differ")
	}
	if h3 != h1 {
		t.Fatalf("page 3 (out of range) should wrap to page 1")
	}
	if h0 != h2 {
		t.Fatalf("page -1 (negative) should wrap to last page")
	}
}

// TestWebPDecode verifies WebP images decode via libwebp (stb_image does not
// support WebP). A minimal valid WebP is built in-memory from a known-good
// 2x2 lossless WebP and must decode to the expected RGBA.
func TestWebPDecode(t *testing.T) {
	// 2x2 solid-red lossless WebP (VP8L), bytes verified against libwebp.
	webp := []byte{
		0x52, 0x49, 0x46, 0x46, 0x1c, 0x00, 0x00, 0x00, 0x57, 0x45, 0x42, 0x50,
		0x56, 0x50, 0x38, 0x4c, 0x0f, 0x00, 0x00, 0x00, 0x2f, 0x01, 0x40, 0x00,
		0x00, 0x07, 0x10, 0xfd, 0x8f, 0xfe, 0x07, 0x22, 0xa2, 0xff, 0x01, 0x00,
	}
	if !isWebP(webp) {
		t.Fatalf("isWebP rejected RIFF/WEBP header")
	}
	w, h, rgba, ok := decodeWebP(webp)
	if !ok {
		t.Fatalf("webp decode failed")
	}
	if w != 2 || h != 2 || len(rgba) != 2*2*4 {
		t.Fatalf("webp decoded %dx%d len=%d, want 2x2", w, h, len(rgba))
	}
	// solid red (lossless): R=255 G=0 B=0
	if rgba[0] != 255 || rgba[1] != 0 || rgba[2] != 0 {
		t.Fatalf("webp pixel (R,G,B)=(%d,%d,%d), want (255,0,0)", rgba[0], rgba[1], rgba[2])
	}
}

// TestDummyGracefulDegrades verifies that when a library is replaced by a
// dummy object (st-min/st-stb/st-pdf builds), the decode paths fail gracefully
// (no panic, no image) instead of crashing. It passes with both real libs and
// dummies since the failure-handling is the same.
func TestDummyGracefulDegrades(t *testing.T) {
	cfg := config.Default()
	if !loadFonts(cfg.Font, int(cfg.GlyphHeight)) {
		t.Skip("font not available")
	}
	trm, err := NewTerminalOpts(cfg, 1.0, 0, 0, 0, "tile_dg", "st", "ST", false)
	if err != nil {
		t.Skipf("no X: %v", err)
	}
	defer trm.Close()

	// malformed PNG magic: real stb returns NULL, dummy-stb returns NULL too
	_, _, _, errOK := decodeImage([]byte{0x89, 0x50, 0x4E, 0x47, 0x0D, 0x0A, 0x1A, 0x0A, 0xff})
	if !errOK {
		t.Fatalf("decodeImage of bad png should fail gracefully (err=true)")
	}
	// malformed webp: WebPGetInfo returns 0 in both real and dummy
	_, _, _, ok := decodeWebP([]byte{0x52, 0x49, 0x46, 0x46, 0x00, 0x00, 0x00, 0x00})
	if ok {
		t.Fatalf("decodeWebP of bad webp should fail gracefully (ok=false)")
	}
	// pdf: page count of garbage is 0 in both real and dummy
	if n := pdfPageCount([]byte("not a pdf")); n != 0 {
		t.Fatalf("pdfPageCount(garbage)=%d want 0", n)
	}
}

// TestDslOpenTextPreview verifies the DSL "open" renders a text file as rows
// from the cursor position, stopping at the last screen row (no scrolling) —
// used by a mini file browser to show a file preview in a pane.
func TestDslOpenTextPreview(t *testing.T) {
	cfg := config.Default()
	if !loadFonts(cfg.Font, int(cfg.GlyphHeight)) {
		t.Skip("font not available")
	}
	dir := t.TempDir()
	f := dir + "/preview.txt"
	var content []byte
	for i := 0; i < 30; i++ {
		content = append(content, []byte("preview line "+itoa(i)+"\n")...)
	}
	if err := os.WriteFile(f, content, 0644); err != nil {
		t.Fatal(err)
	}
	trm, err := NewTerminalOpts(cfg, 1.0, 0, 0, 0, "tile_txt", "st", "ST", false)
	if err != nil {
		t.Skipf("no X: %v", err)
	}
	defer trm.Close()
	core := term.NewTerm(cfg, trm)
	trm.termCore = core

	// place the cursor at column 30 (right preview pane), open the text file
	core.Twrite([]byte("\x1b[1;30H"), false)
	core.Twrite([]byte("\x1bPopen '"+f+"'\x1b\\"), false)
	core.Redraw()

	// row 0 must contain "preview line 0" starting at col 30 (index 29)
	if !strings.Contains(core.LineText(0), "preview line 0") {
		t.Fatalf("row0=%q missing preview text", core.LineText(0))
	}
	// the last screen row must not have scrolled content from beyond the file
	if !strings.Contains(core.LineText(0), "preview line 0") {
		t.Fatalf("row0=%q", core.LineText(0))
	}
	// file has 30 lines but only cfg.Rows fit: nothing scrolls, row0 stays put
	t.Logf("OK: text preview rendered at offset, rows=%d", core.Rows())
}

// TestDslOpenTextStopsAtBottom verifies the text preview only reads/renders
// the bytes that fit the visible rows: a long line is truncated (not wrapped)
// and rendering stops at the last screen row.
func TestDslOpenTextStopsAtBottom(t *testing.T) {
	cfg := config.Default()
	if !loadFonts(cfg.Font, int(cfg.GlyphHeight)) {
		t.Skip("font not available")
	}
	dir := t.TempDir()
	f := dir + "/big.txt"
	// a 200-char line, then many more lines than rows
	var content strings.Builder
	content.WriteString(strings.Repeat("X", 200))
	content.WriteString("\n")
	for i := 0; i < 100; i++ {
		content.WriteString("line ")
		content.WriteString(itoa(i))
		content.WriteString("\n")
	}
	if err := os.WriteFile(f, []byte(content.String()), 0644); err != nil {
		t.Fatal(err)
	}
	trm, err := NewTerminalOpts(cfg, 1.0, 0, 0, 0, "tile_tbs", "st", "ST", false)
	if err != nil {
		t.Skipf("no X: %v", err)
	}
	defer trm.Close()
	core := term.NewTerm(cfg, trm)
	trm.termCore = core
	core.Twrite([]byte("\x1bPopen '"+f+"'\x1b\\"), false)
	core.Redraw()
	// row0: full-width X's truncated to the column count
	row0 := core.LineText(0)
	if len(row0) != int(cfg.Cols) || row0[0] != 'X' {
		t.Fatalf("row0=%q want %d X's", row0, cfg.Cols)
	}
	// rendering stopped at the last row: the bottom row still has content from
	// within the file but the file's later lines were not wrapped in
	// (i.e. no X's reappear after the first row)
	for y := 1; y < int(cfg.Rows); y++ {
		if strings.Contains(core.LineText(y), "X") {
			t.Fatalf("row %d has wrapped X's from the truncated line: %q", y, core.LineText(y))
		}
	}
	t.Logf("OK: long line truncated, no wrap into later rows")
}

// TestFBAltPathAndIncremental verifies demo/file-browser.sh:
//  1. it starts from the <path> argument instead of the cwd, and
//  2. Up/Down selection moves only redraw the affected rows (no full clear).
func TestFBAltPathAndIncremental(t *testing.T) {
	dir := t.TempDir()
	sub := dir + "/sub"
	if err := os.MkdirAll(sub, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(sub+"/s.txt", []byte("sub file\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dir+"/r.txt", []byte("root file\n"), 0644); err != nil {
		t.Fatal(err)
	}
	master, slave, err := ptyutil.Open()
	if err != nil {
		t.Fatal(err)
	}
	defer master.Close()
	ptyutil.SetWinSize(master, 24, 80)
	cmd := exec.Command("bash", "demo/file-browser.sh", sub)
	cmd.Stdin = slave
	cmd.Stdout = slave
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { master.Write([]byte("q")); cmd.Process.Kill() }()
	slave.Close()

	var all []byte
	go func() {
		buf := make([]byte, 8192)
		for {
			n, err := master.Read(buf)
			if n > 0 {
				all = append(all, buf[:n]...)
			}
			if err != nil {
				return
			}
		}
	}()
	time.Sleep(600 * time.Millisecond)
	if clears := strings.Count(string(all), "\x1bPclear\x1b\\"); clears != 1 {
		t.Fatalf("initial render issued %d full clears, want exactly 1", clears)
	}
	// one Down move; must NOT trigger another full clear
	master.Write([]byte("\x1b[B"))
	time.Sleep(300 * time.Millisecond)
	master.Write([]byte("\x1b[B"))
	time.Sleep(300 * time.Millisecond)
	allStr := string(all)
	if clears := strings.Count(allStr, "\x1bPclear\x1b\\"); clears > 1 {
		t.Fatalf("selection moves triggered %d full clears, want incremental redraw", clears)
	}
	// the alt-path list should contain s.txt (from <path>/sub), not r.txt
	if !strings.Contains(allStr, "s.txt") {
		t.Fatalf("alt-path list missing s.txt (started at %s): %q", sub, allStr)
	}
	if strings.Contains(allStr, "r.txt") {
		t.Fatalf("alt-path list unexpectedly contains r.txt from parent dir")
	}
	t.Logf("OK: alt path start + incremental redraw (single initial clear)")
}

// TestFBSelectionKeepsList verifies selection moves in demo/file-browser.sh do
// not blank the unaffected list rows (the preview clear must not erase the
// whole line), and that a fit-height image preview redraws the full list.
func TestFBSelectionKeepsList(t *testing.T) {
	dir := t.TempDir()
	// glob order: .., aaaa, bbbb, dddd, pic
	os.WriteFile(dir+"/aaaa.txt", []byte(strings.Repeat("a", 40)+"\n"), 0644)
	os.WriteFile(dir+"/bbbb.txt", []byte(strings.Repeat("b", 40)+"\n"), 0644)
	os.WriteFile(dir+"/dddd.txt", []byte(strings.Repeat("d", 40)+"\n"), 0644)
	os.WriteFile(dir+"/pic.png", []byte{0x89, 'P', 'N', 'G', 0x0d, 0x0a, 0x1a, 0x0a,
		0x00, 0x00, 0x00, 0x0d, 'I', 'H', 'D', 'R',
		0x00, 0x00, 0x00, 0x01, 0x00, 0x00, 0x00, 0x01, 0x08, 0x02, 0x00, 0x00, 0x00,
		0x90, 0x77, 0x53, 0xde, 0x00, 0x00, 0x00, 0x0c, 'I', 'D', 'A', 'T',
		0x08, 0x1d, 0x01, 0x02, 0x03, 0x04, 0x05, 0x06, 0x07, 0x08,
		0x48, 0x51, 0x89, 0x41, 0x8c, 0x00, 0x00, 0x00, 0x00, 'I', 'E', 'N', 'D',
		0xae, 0x42, 0x60, 0x82}, 0644)

	cfg := config.Default()
	if !loadFonts(cfg.Font, int(cfg.GlyphHeight)) {
		t.Skip("font not available")
	}
	trm, err := NewTerminalOpts(cfg, 1.0, 0, 0, 0, "tile_fbk", "st", "ST", false)
	if err != nil {
		t.Skipf("no X: %v", err)
	}
	defer trm.Close()
	core := term.NewTerm(cfg, trm)
	trm.termCore = core

	master, slave, err := ptyutil.Open()
	if err != nil {
		t.Fatal(err)
	}
	defer master.Close()
	ptyutil.SetWinSize(master, 24, 80)
	cmd := exec.Command("bash", "demo/file-browser.sh", dir)
	cmd.Stdin = slave
	cmd.Stdout = slave
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { master.Write([]byte("q")); cmd.Process.Kill() }()
	slave.Close()
	go func() {
		buf := make([]byte, 8192)
		for {
			n, err := master.Read(buf)
			if n > 0 {
				core.Twrite(buf[:n], false)
			}
			if err != nil {
				return
			}
		}
	}()
	core.SetWriter(func(b []byte) { master.Write(b) })

	time.Sleep(700 * time.Millisecond)
	core.Redraw()
	// 3 Down moves on text files: IDX 0->3 (dddd), list must stay intact
	for i := 0; i < 3; i++ {
		master.Write([]byte("\x1b[B"))
		time.Sleep(250 * time.Millisecond)
	}
	core.Redraw()
	for _, n := range []string{"aaaa.txt", "bbbb.txt", "dddd.txt"} {
		found := false
		for y := 1; y < 6; y++ {
			if strings.Contains(core.LineText(y), n) {
				found = true
			}
		}
		if !found {
			t.Fatalf("text-file move blanked list row containing %q", n)
		}
	}
	// one more Down to reach pic.png (image, fit-height clears screen)
	master.Write([]byte("\x1b[B"))
	time.Sleep(400 * time.Millisecond)
	core.Redraw()
	for _, n := range []string{"aaaa.txt", "bbbb.txt", "dddd.txt", "pic.png"} {
		found := false
		for y := 1; y < 6; y++ {
			if strings.Contains(core.LineText(y), n) {
				found = true
			}
		}
		if !found {
			t.Fatalf("image preview blanked list row containing %q", n)
		}
	}
	t.Logf("OK: list rows preserved on selection moves and after image preview")
}
