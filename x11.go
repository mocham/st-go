package main

import (
	"log"
	"os"
	"strings"
	"sync"
	"unsafe"

	"github.com/BurntSushi/xgb"
	"github.com/BurntSushi/xgb/xproto"
	"github.com/BurntSushi/xgbutil"

	"st-go/config"
	"st-go/term"
)

const (
	ColDefaultFg  = 258
	ColDefaultBg  = 259
	ColDefaultCs  = 256
	ColDefaultRcs = 257
)

// Color: ARGB value + X pixel
type Color struct {
	argb  uint32
	pixel uint32
}

type Terminal struct {
	conn *xgb.Conn
	xu   *xgbutil.XUtil
	scr  *xproto.ScreenInfo

	win    xproto.Window
	pixmap xproto.Pixmap
	gc     xproto.Gcontext

	cols, rows int
	cw, ch     int
	baseline   int
	borderpx   int
	ratio      float64

	colors []Color

	title       string
	iconTitle   string
	cursorShape int
	cursorThick int

	atoms map[string]xproto.Atom

	winMode   uint
	ignoreMod uint

	forceMouseMod uint16
	doubleClick   uint
	tripleClick   uint

	pasteTarget xproto.Atom
	incrActive  bool
	incrData    []byte
	incrProperty xproto.Atom

	blinkMs uint

	termCore *term.Term

	keys      []keyDef
	shortcuts []shortcut
	mshortcuts []mShortcut

	selectionText string

	mu sync.Mutex
}

// shortcut from config
type shortcut struct {
	keysym uint
	mask   int
	action string
	arg    string
}

// mShortcut from config
type mShortcut struct {
	mask    int
	button  byte
	action  string
	arg     string
	release bool
}

func NewTerminal(cfg *config.Config) (*Terminal, error) {
	return NewTerminalRatio(cfg, 1.0)
}

// NewTerminalRatio creates a terminal; ratio scales the glyph geometry
// (cell width/height and baseline) like st's --ratio flag.
func NewTerminalRatio(cfg *config.Config, ratio float64) (*Terminal, error) {
	if ratio <= 0 {
		ratio = 1.0
	}
	x, err := xgb.NewConn()
	if err != nil {
		return nil, err
	}
	xu, err := xgbutil.NewConn()
	if err != nil {
		return nil, err
	}
	setup := xproto.Setup(x)
	scr := setup.DefaultScreen(x)

	// Effective glyph geometry: config default scaled by ratio.
	cw := int(float64(cfg.GlyphWidth) * ratio)
	ch := int(float64(cfg.GlyphHeight) * ratio)
	baseline := int(float64(cfg.GlyphBaseline) * ratio)
	if cw < 1 {
		cw = 1
	}
	if ch < 1 {
		ch = 1
	}

	t := &Terminal{
		conn:        x,
		xu:          xu,
		scr:         scr,
		cols:        int(cfg.Cols),
		rows:        int(cfg.Rows),
		cw:          cw,
		ch:          ch,
		baseline:    baseline,
		borderpx:    cfg.Borderpx,
		ratio:       ratio,
		title:        "st",
		iconTitle:    "st",
		cursorShape:  int(cfg.CursorShape),
		cursorThick:  int(cfg.CursorThick),
		atoms:       make(map[string]xproto.Atom),
		ignoreMod:   cfg.IgnoreMod,
		forceMouseMod: uint16(cfg.ForceMouseMod),
		doubleClick:   cfg.DoubleClickMs,
		tripleClick:   cfg.TripleClickMs,
		blinkMs:       cfg.BlinkTimeout,
	}

	for _, name := range []string{"WM_NAME", "WM_ICON_NAME", "WM_PROTOCOLS",
		"WM_DELETE_WINDOW", "UTF8_STRING", "CLIPBOARD", "PRIMARY", "TARGETS",
		"INCR", "TIMESTAMP", "ATOM", "STRING", "WM_STATE", "INTEGER"} {
		t.intern(name)
	}

	// window id
	wid, err := xproto.NewWindowId(x)
	if err != nil {
		return nil, err
	}
	w := t.borderpx*2 + t.cols*t.cw
	h := t.borderpx*2 + t.rows*t.ch

	mask := uint32(xproto.CwBackPixel | xproto.CwEventMask)
	values := []uint32{
		uint32(scr.BlackPixel),
		uint32(xproto.EventMaskExposure | xproto.EventMaskKeyPress |
			xproto.EventMaskKeyRelease | xproto.EventMaskStructureNotify |
			xproto.EventMaskButtonPress | xproto.EventMaskButtonRelease |
			xproto.EventMaskButtonMotion | xproto.EventMaskPointerMotion |
			xproto.EventMaskFocusChange | xproto.EventMaskVisibilityChange),
	}
	if err := xproto.CreateWindowChecked(x, scr.RootDepth, wid, scr.Root,
		int16(0), int16(0), uint16(w), uint16(h), 0,
		xproto.WindowClassInputOutput, scr.RootVisual, mask, values).Check(); err != nil {
		return nil, err
	}
	t.win = wid

	t.setTitle(t.title)

	wmDelete := t.atoms["WM_DELETE_WINDOW"]
	wmProtocols := t.atoms["WM_PROTOCOLS"]
	_ = xproto.ChangePropertyChecked(x, xproto.PropModeReplace, wid,
		wmProtocols, t.atoms["ATOM"], 32, 1, uint32Bytes(uint32(wmDelete))).Check()

	// create backing pixmap
	pid, err := xproto.NewPixmapId(x)
	if err != nil {
		return nil, err
	}
	if err := xproto.CreatePixmapChecked(x, scr.RootDepth, pid,
		xproto.Drawable(wid), uint16(w), uint16(h)).Check(); err != nil {
		return nil, err
	}
	t.pixmap = pid
	// GC
	gc, err := xproto.NewGcontextId(x)
	if err != nil {
		return nil, err
	}
	if err := xproto.CreateGCChecked(x, gc, xproto.Drawable(pid),
		0, nil).Check(); err != nil {
		return nil, err
	}
	t.gc = gc

	// colors
	t.loadColors(cfg)

	ensureFramebuffer(w, h)
	t.clearFramebuffer()

	xproto.MapWindow(x, wid)
	return t, nil
}

func (t *Terminal) intern(name string) {
	rep, err := xproto.InternAtom(t.conn, true, uint16(len(name)), name).Reply()
	if err != nil {
		log.Printf("intern %s: %v", name, err)
		return
	}
	t.atoms[name] = rep.Atom
}

func (t *Terminal) setTitle(s string) {
	if s == "" {
		return
	}
	t.title = s
	_ = xproto.ChangePropertyChecked(t.conn, xproto.PropModeReplace, t.win,
		t.atoms["WM_NAME"], t.atoms["UTF8_STRING"], 8,
		uint32(len(s)), []byte(s)).Check()
}

func (t *Terminal) SetMode(set bool, mode uint) { t.setWinMode(set, mode) }
func (t *Terminal) SetPointerMotion(on bool)      {}
func (t *Terminal) LoadCols()                     {}

func (t *Terminal) SetCursor(shape int) bool {
	t.cursorShape = shape
	return true
}

func (t *Terminal) setWinMode(set bool, mode uint) {
	// mirror into a field; called from term core via hook
	if set {
		t.winMode |= mode
	} else {
		t.winMode &^= mode
	}
}

func (t *Terminal) winModeIs(flag uint) bool { return t.winMode&flag != 0 }

// StartDraw returns whether window is visible.
func (t *Terminal) StartDraw() bool { return true }

func uint32Bytes(v uint32) []byte {
	return (*[4]byte)(unsafe.Pointer(&v))[:]
}

func (t *Terminal) Close() {
	if t.pixmap != 0 {
		xproto.FreePixmap(t.conn, t.pixmap)
	}
	if t.win != 0 {
		xproto.DestroyWindow(t.conn, t.win)
	}
	t.conn.Close()
}

func xerror(err error, fatal bool) {
	if err == nil {
		return
	}
	log.Printf("x11: %v", err)
	if fatal {
		os.Exit(1)
	}
}

func expandHome(p string) string {
	if strings.HasPrefix(p, "~") {
		home, _ := os.UserHomeDir()
		return home + p[1:]
	}
	return p
}
