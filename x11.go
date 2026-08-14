package main

import (
	"fmt"
	"log"
	"os"
	"strings"
	"sync"
	"sync/atomic"
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
	x, y       int
	geomMask   XGeometryMask
	isFixed    bool

	colors []Color

	cfg             *config.Config
	webpCache       *webPCache
	webpAnimDecoder *webPAnimDecoder

	title       string
	iconTitle   string
	lockTitle   bool
	cursorShape int
	cursorThick int

	atoms map[string]xproto.Atom

	winMode   uint
	ignoreMod uint

	forceMouseMod uint16
	doubleClick   uint
	tripleClick   uint

	pasteTarget  xproto.Atom
	incrActive   bool
	incrData     []byte
	incrProperty xproto.Atom

	blinkMs uint

	termCore *term.Term
	// inPTYWrite lets the synchronous paint callback distinguish terminal
	// output from X/input/timer redraws. PTY paints are scheduled by main's
	// latency loop; all other redraws paint immediately.
	inPTYWrite        bool
	ptyPaintRequested bool
	done              chan struct{}

	// ttyResize is set by main to send TIOCSWINSZ to the pty master.
	ttyResize func(rows, cols int)

	keys       []keyDef
	shortcuts  []shortcut
	mshortcuts []mShortcut

	selectionText         string
	geometryTags          map[string]windowRect
	restoreGeometryTag    string
	suppressRestoreButton bool

	mu       sync.Mutex
	isClosed int32
}

// closed reports whether the terminal connection has been closed (e.g. the
// WM_DELETE_WINDOW handler), so the event loop can stop instead of spinning
// on the dead connection.
func (t *Terminal) closed() bool {
	return atomic.LoadInt32(&t.isClosed) != 0
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
	return NewTerminalOpts(cfg, 1.0, 0, 0, 0, "st", "", "", false)
}

// NewTerminalOpts creates a terminal with the given ratio, window position
// (x, y and geometry mask for negative offsets), title, instance/class names
// (st: -n res_name / -c res_class), and fixed-size flag (st: -i).
func NewTerminalOpts(cfg *config.Config, ratio float64, x, y int, geomMask XGeometryMask, title, instance, className string, isFixed bool) (*Terminal, error) {
	if ratio <= 0 {
		ratio = 1.0
	}
	if title == "" {
		title = "st"
	}
	if instance == "" {
		instance = cfg.Termname
	}
	if className == "" {
		className = cfg.Termname
	}
	xc, err := xgb.NewConn()
	if err != nil {
		return nil, err
	}
	xu, err := xgbutil.NewConn()
	if err != nil {
		return nil, err
	}
	setup := xproto.Setup(xc)
	scr := setup.DefaultScreen(xc)

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
		conn:          xc,
		xu:            xu,
		scr:           scr,
		cfg:           cfg,
		cols:          int(cfg.Cols),
		rows:          int(cfg.Rows),
		cw:            cw,
		ch:            ch,
		baseline:      baseline,
		borderpx:      cfg.Borderpx,
		ratio:         ratio,
		title:         title,
		iconTitle:     title,
		x:             x,
		y:             y,
		geomMask:      geomMask,
		isFixed:       isFixed,
		cursorShape:   int(cfg.CursorShape),
		cursorThick:   int(cfg.CursorThick),
		atoms:         make(map[string]xproto.Atom),
		ignoreMod:     cfg.IgnoreMod,
		forceMouseMod: uint16(cfg.ForceMouseMod),
		doubleClick:   cfg.DoubleClickMs,
		tripleClick:   cfg.TripleClickMs,
		blinkMs:       cfg.BlinkTimeout,
		geometryTags:  make(map[string]windowRect),
		done:          make(chan struct{}),
	}

	for _, name := range []string{"WM_NAME", "WM_ICON_NAME", "WM_PROTOCOLS",
		"WM_DELETE_WINDOW", "UTF8_STRING", "CLIPBOARD", "PRIMARY", "TARGETS",
		"INCR", "TIMESTAMP", "ATOM", "STRING", "WM_STATE", "INTEGER", "WM_CLASS",
		"_NET_WM_NAME", "_NET_WM_ICON_NAME", "_NET_WM_PID", "CARDINAL",
		"WM_NORMAL_HINTS", "WM_SIZE_HINTS", "BAR_DATA"} {
		t.intern(name)
	}

	// window id
	wid, err := xproto.NewWindowId(xc)
	if err != nil {
		return nil, err
	}
	w := t.borderpx*2 + t.cols*t.cw
	h := t.borderpx*2 + t.rows*t.ch

	// Negative geometry offsets are relative to the screen's right/bottom
	// edge (st: xw.l += DisplayWidth - win.w - 2).
	winX, winY := t.x, t.y
	if t.geomMask&XNegative != 0 {
		winX += int(scr.WidthInPixels) - w - 2
	}
	if t.geomMask&YNegative != 0 {
		winY += int(scr.HeightInPixels) - h - 2
	}

	mask := uint32(xproto.CwBackPixel | xproto.CwEventMask)
	values := []uint32{
		uint32(scr.BlackPixel),
		uint32(xproto.EventMaskExposure | xproto.EventMaskKeyPress |
			xproto.EventMaskKeyRelease | xproto.EventMaskStructureNotify |
			xproto.EventMaskButtonPress | xproto.EventMaskButtonRelease |
			xproto.EventMaskButtonMotion | xproto.EventMaskPointerMotion |
			xproto.EventMaskFocusChange | xproto.EventMaskVisibilityChange |
			xproto.EventMaskPropertyChange),
	}
	// Unchecked CreateWindow + all identity properties in ONE request batch,
	// before any round-trip. The tiling WM (gowinmgr) receives CreateNotify
	// immediately on window creation and reads WM_NAME/WM_CLASS in its
	// handler; if we round-trip (CreateWindowChecked) between CreateWindow and
	// the property writes, the WM reads empty titles. st's XCreateWindow +
	// XStoreName + XFlush have the same no-round-trip semantics.
	xproto.CreateWindow(xc, scr.RootDepth, wid, scr.Root,
		int16(winX), int16(winY), uint16(w), uint16(h), 0,
		xproto.WindowClassInputOutput, scr.RootVisual, mask, values)
	t.win = wid

	// XStoreName: WM_NAME as STRING (tiling WMs read this).
	xproto.ChangeProperty(xc, xproto.PropModeReplace, wid,
		t.atoms["WM_NAME"], t.atoms["STRING"], 8,
		uint32(len(t.title)), []byte(t.title))
	if t.atoms["_NET_WM_NAME"] != 0 {
		xproto.ChangeProperty(xc, xproto.PropModeReplace, wid,
			t.atoms["_NET_WM_NAME"], t.atoms["UTF8_STRING"], 8,
			uint32(len(t.title)), []byte(t.title))
	}
	// WM_CLASS: instance (res_name) + class (res_class), like st's xhints
	xproto.ChangeProperty(xc, xproto.PropModeReplace, wid,
		t.atoms["WM_CLASS"], t.atoms["STRING"], 8,
		uint32(len(instance)+1+len(className)+1),
		append([]byte(instance+"\x00"), []byte(className+"\x00")...))
	xc.Sync()

	wmDelete := t.atoms["WM_DELETE_WINDOW"]
	wmProtocols := t.atoms["WM_PROTOCOLS"]
	_ = xproto.ChangePropertyChecked(xc, xproto.PropModeReplace, wid,
		wmProtocols, t.atoms["ATOM"], 32, 1, uint32Bytes(uint32(wmDelete))).Check()

	// _NET_WM_PID (st sets this before mapping)
	pidv := uint32(os.Getpid())
	_ = xproto.ChangePropertyChecked(xc, xproto.PropModeReplace, wid,
		t.atoms["_NET_WM_PID"], t.atoms["CARDINAL"], 32, 1,
		uint32Bytes(pidv)).Check()

	// WM_NORMAL_HINTS (st's xhints): size + resize increment + base/min,
	// and USPosition + gravity when geometry x/y are given.
	t.setWMHints(w, h, winX, winY)

	// create backing pixmap
	pid, err := xproto.NewPixmapId(xc)
	if err != nil {
		return nil, err
	}
	if err := xproto.CreatePixmapChecked(xc, scr.RootDepth, pid,
		xproto.Drawable(wid), uint16(w), uint16(h)).Check(); err != nil {
		return nil, err
	}
	t.pixmap = pid
	// GC
	gc, err := xproto.NewGcontextId(xc)
	if err != nil {
		return nil, err
	}
	if err := xproto.CreateGCChecked(xc, gc, xproto.Drawable(pid),
		0, nil).Check(); err != nil {
		return nil, err
	}
	t.gc = gc

	// colors
	t.loadColors(cfg)

	ensureFramebuffer(w, h)
	t.clearFramebuffer()

	// Before mapping, ensure the tiling WM (gowinmgr) has processed our title
	// into its BAR_DATA property in the "prefix@title@class@instance@desk"
	// format. gowinmgr reads the title on CreateNotify; if it raced our
	// WM_NAME write, the title segment is empty and the window is never tiled.
	// Write the expected format ourselves so WindowMap picks it up.
	t.verifyBarData(instance, className)

	xproto.MapWindow(xc, wid)
	xc.Sync() // st: XMapWindow then XSync

	// Post-map re-check: gowinmgr's WindowMap may have re-read the title and
	// rewritten BAR_DATA with a desktop suffix; confirm it is still correct.
	t.verifyBarData(instance, className)

	if cfg.WebPCachePath != "" {
		cachePath := strings.ReplaceAll(cfg.WebPCachePath, "{uid}", fmt.Sprint(os.Getuid()))
		cache, err := openWebPCache(expandHome(cachePath))
		if err != nil {
			log.Printf("webp cache: %v", err)
		} else {
			t.webpCache = cache
		}
	}
	return t, nil
}

// verifyBarData checks the BAR_DATA property written by gowinmgr. If it is
// missing or its title segment is empty, writes the expected "?@?@..." format
// so the window manager tiles the terminal correctly.
func (t *Terminal) verifyBarData(instance, className string) {
	barAtom := t.atoms["BAR_DATA"]
	if barAtom == 0 {
		return
	}
	rep, err := xproto.GetProperty(t.conn, false, t.win, barAtom,
		0, 0, 1024).Reply()
	if err != nil {
		return
	}
	bar := string(rep.Value)
	// Expected format: prefix@title@class@instance@desktop. The title segment
	// is index 1 when split on '@'. If empty, gowinmgr missed it.
	parts := strings.Split(bar, "@")
	if len(parts) < 4 || parts[1] == "" {
		// Reconstruct: keep any existing prefix (tile-/auto-) or default to
		// tile- so the window is tiled; title = our title.
		prefix := "tile-"
		if len(parts) > 0 && (parts[0] == "tile-" || parts[0] == "auto-" || parts[0] == "auto-sticky") {
			prefix = parts[0]
		}
		desk := ""
		if len(parts) > 3 {
			desk = parts[len(parts)-1]
		}
		newBar := prefix + "@" + t.title + "@" + className + "@" + instance + "@" + desk
		xproto.ChangeProperty(t.conn, xproto.PropModeReplace, t.win,
			barAtom, t.atoms["STRING"], 8,
			uint32(len(newBar)), []byte(newBar))
	}
}

func (t *Terminal) intern(name string) {
	rep, err := xproto.InternAtom(t.conn, true, uint16(len(name)), name).Reply()
	if err != nil {
		log.Printf("intern %s: %v", name, err)
		return
	}
	t.atoms[name] = rep.Atom
}

func (t *Terminal) setWMHints(winW, winH, x, y int) {
	// XSizeHints as an array of 32-bit values (mirrors st's xhints + Xlib).
	// flags: USPosition=1, USSize=2, PPosition=4, PSize=8, PMinSize=16,
	// PMaxSize=32, PResizeInc=64, PBaseSize=256, PWinGravity=512.
	var h [18]uint32
	flags := uint32(8) // PSize
	flags |= 64        // PResizeInc
	flags |= 256       // PBaseSize
	flags |= 16        // PMinSize
	if t.isFixed {
		flags |= 32 // PMaxSize
	}
	h[0] = flags
	h[1] = uint32(x) // x
	h[2] = uint32(y) // y
	h[3] = uint32(winW)
	h[4] = uint32(winH)
	// min width/height
	h[5] = uint32(t.cw + 2*t.borderpx)
	h[6] = uint32(t.ch + 2*t.borderpx)
	// max width/height (fixed only)
	if t.isFixed {
		h[7] = uint32(winW)
		h[8] = uint32(winH)
	}
	// width_inc / height_inc
	h[9] = uint32(t.cw)
	h[10] = uint32(t.ch)
	// base width/height
	h[15] = uint32(2 * t.borderpx)
	h[16] = uint32(2 * t.borderpx)
	// win_gravity
	h[17] = uint32(gravityForMask(t.geomMask))

	// x/y only used when geometry provides them (USPosition + PWinGravity)
	if t.geomMask&(XValue|YValue) != 0 {
		h[0] |= 1   // USPosition
		h[0] |= 512 // PWinGravity
	} else {
		h[1], h[2] = 0, 0
	}

	_ = xproto.ChangePropertyChecked(t.conn, xproto.PropModeReplace, t.win,
		t.atoms["WM_NORMAL_HINTS"], t.atoms["WM_SIZE_HINTS"], 32,
		uint32(len(h)), uint32Bytes32(h[:])).Check()
}

func uint32Bytes32(v []uint32) []byte {
	b := make([]byte, len(v)*4)
	for i, x := range v {
		b[i*4+0] = byte(x)
		b[i*4+1] = byte(x >> 8)
		b[i*4+2] = byte(x >> 16)
		b[i*4+3] = byte(x >> 24)
	}
	return b
}

// gravityForMask maps XGeometryMask negatives to a win gravity like st's
// xgeommasktogravity (NorthWest=1, NorthEast=2, SouthWest=3, SouthEast=4).
func gravityForMask(mask XGeometryMask) int {
	switch mask & (XNegative | YNegative) {
	case 0:
		return 1 // NorthWest
	case XNegative:
		return 2 // NorthEast
	case YNegative:
		return 3 // SouthWest
	default:
		return 4 // SouthEast
	}
}

// actualRowsCols queries the live window geometry and computes the effective
// rows/cols like st's cresize. This lets main() size the pty to the window's
// real dimensions (after any WM tiling) so the child (vim, shells) sees the
// correct TIOCGWINSZ instead of a stale config default.
func (t *Terminal) actualRowsCols() (rows, cols int) {
	rep, err := xproto.GetGeometry(t.conn, xproto.Drawable(t.win)).Reply()
	if err != nil {
		return t.rows, t.cols
	}
	cols = (int(rep.Width) - 2*t.borderpx) / t.cw
	rows = (int(rep.Height) - 2*t.borderpx) / t.ch
	if cols < 1 {
		cols = 1
	}
	if rows < 1 {
		rows = 1
	}
	return rows, cols
}

func (t *Terminal) setTitle(s string) {
	if s == "" || t.lockTitle {
		return
	}
	t.title = s
	// st's xsettitle: both WM_NAME and _NET_WM_NAME as UTF8.
	_ = xproto.ChangePropertyChecked(t.conn, xproto.PropModeReplace, t.win,
		t.atoms["WM_NAME"], t.atoms["UTF8_STRING"], 8,
		uint32(len(s)), []byte(s)).Check()
	if t.atoms["_NET_WM_NAME"] != 0 {
		_ = xproto.ChangePropertyChecked(t.conn, xproto.PropModeReplace, t.win,
			t.atoms["_NET_WM_NAME"], t.atoms["UTF8_STRING"], 8,
			uint32(len(s)), []byte(s)).Check()
	}
}

func (t *Terminal) SetMode(set bool, mode uint) { t.setWinMode(set, mode) }
func (t *Terminal) SetPointerMotion(on bool)    {}

// LoadCols mirrors st's xloadcols(): reset the palette to config defaults.
func (t *Terminal) LoadCols() {
	if t.cfg == nil {
		return
	}
	t.loadColors(t.cfg)
}

func (t *Terminal) SetCursor(shape int) bool {
	// st's xsetcursor returns 0 on success, 1 on error. The Hooks bool
	// mirrors that: false = success, true = error (invalid cursor shape).
	if shape < 0 || shape > 7 {
		return true // error -> csihandle reports unknown sequence
	}
	t.cursorShape = shape
	return false // success
}

func (t *Terminal) setWinMode(set bool, mode uint) {
	old := t.winMode
	if set {
		t.winMode |= mode
	} else {
		t.winMode &^= mode
	}
	// st's xsetmode redraws when the reverse-video mode changes.
	if (t.winMode&term.ModeReverse) != (old&term.ModeReverse) &&
		t.termCore != nil {
		t.termCore.Redraw()
	}
}

func (t *Terminal) winModeIs(flag uint) bool { return t.winMode&flag != 0 }

// StartDraw returns whether window is visible.
func (t *Terminal) StartDraw() bool { return true }

func uint32Bytes(v uint32) []byte {
	return (*[4]byte)(unsafe.Pointer(&v))[:]
}

func (t *Terminal) Close() {
	if !atomic.CompareAndSwapInt32(&t.isClosed, 0, 1) {
		return
	}
	if t.done != nil {
		close(t.done)
	}
	if t.webpCache != nil {
		t.webpCache.close()
	}
	if t.webpAnimDecoder != nil {
		t.webpAnimDecoder.close()
	}
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
