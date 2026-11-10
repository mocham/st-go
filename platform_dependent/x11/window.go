package x11

import (
	"fmt"
	"log"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"unsafe"

	"github.com/BurntSushi/xgb"
	"github.com/BurntSushi/xgb/xfixes"
	"github.com/BurntSushi/xgb/xproto"
	"github.com/BurntSushi/xgbutil"

	"st-go/config"
	"st-go/limits"
	"st-go/platform_neutral/api"
	"st-go/platform_neutral/sharedclipboard"
)

type XGeometryMask = api.XGeometryMask

const (
	XValue    = api.XValue
	YValue    = api.YValue
	XNegative = api.XNegative
	YNegative = api.YNegative
)

type Driver struct {
	textCursor api.TextCursor
	conn       *xgb.Conn
	xu         *xgbutil.XUtil
	scr        *xproto.ScreenInfo

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

	title               string
	iconTitle           string
	instance, className string

	atoms map[string]xproto.Atom

	pasteTarget  xproto.Atom
	incrActive   bool
	incrDiscard  bool
	incrData     []byte
	incrProperty xproto.Atom

	selectionText      string
	clipboardText      string
	clipboardSet       bool
	primarySet         bool
	clipboardQuery     *clipboardQuery
	clipboardTransfers map[clipboardTransferID]*clipboardTransfer
	clipboardSync      *sharedclipboard.Sync
	sharedClipboard    map[byte]sharedclipboard.Entry
	clipboardOwners    map[byte]xproto.Window
	externalPending    map[byte]xproto.Window
	externalImport     map[byte]xproto.Window
	clipboardManager   xproto.Atom
	clipboardBridge    bool
	xfixesAvailable    bool
	displayID          string
	callbacks          api.Callbacks
	surfaces           map[xproto.Window]*Surface
	surfaceMu          sync.RWMutex
	keyOnce            sync.Once
	keymapReply        *xproto.GetKeyboardMappingReply
	firstKey           xproto.Keycode
	keysymsPer         byte
	isClosed           int32
}

// closed reports whether the terminal connection has been closed (e.g. the
// WM_DELETE_WINDOW handler), so the event loop can stop instead of spinning
// on the dead connection.
func (t *Driver) closed() bool {
	return atomic.LoadInt32(&t.isClosed) != 0
}

// Open creates native resources without mapping; the host initializes its
// existing framebuffer before calling Show, preserving startup ordering.
func Open(o api.WindowOptions) (api.Host, error) {
	cfg := &config.Config{Cols: uint(o.Cols), Rows: uint(o.Rows), GlyphWidth: uint(o.CellWidth), GlyphHeight: uint(o.CellHeight), Borderpx: o.Border, Termname: o.Class}
	return newDriver(cfg, 1, o.X, o.Y, XGeometryMask(o.GeometryMask), o.Title, o.Instance, o.Class, o.Fixed, o.CachePath)
}

// NewTerminalOpts creates a terminal with the given ratio, window position
// (x, y and geometry mask for negative offsets), title, instance/class names
// (st: -n res_name / -c res_class), and fixed-size flag (st: -i).
func newDriver(cfg *config.Config, ratio float64, x, y int, geomMask XGeometryMask, title, instance, className string, isFixed bool, cachePath string) (*Driver, error) {
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

	t := &Driver{
		conn:      xc,
		xu:        xu,
		scr:       scr,
		cols:      int(cfg.Cols),
		rows:      int(cfg.Rows),
		cw:        cw,
		ch:        ch,
		baseline:  baseline,
		borderpx:  cfg.Borderpx,
		ratio:     ratio,
		title:     title,
		iconTitle: title,
		instance:  instance, className: className,
		x:               x,
		y:               y,
		geomMask:        geomMask,
		isFixed:         isFixed,
		atoms:           make(map[string]xproto.Atom),
		surfaces:        make(map[xproto.Window]*Surface),
		sharedClipboard: make(map[byte]sharedclipboard.Entry), clipboardOwners: make(map[byte]xproto.Window),
		externalPending: make(map[byte]xproto.Window), externalImport: make(map[byte]xproto.Window),
		displayID: os.Getenv("DISPLAY"),
	}
	if xfixes.Init(xc) == nil {
		if _, versionErr := xfixes.QueryVersion(xc, 5, 0).Reply(); versionErr == nil {
			t.xfixesAvailable = true
		}
	}

	for _, name := range []string{"WM_NAME", "WM_ICON_NAME", "WM_PROTOCOLS",
		"WM_DELETE_WINDOW", "UTF8_STRING", "CLIPBOARD", "PRIMARY", "TARGETS",
		"INCR", "TIMESTAMP", "ATOM", "STRING", "WM_STATE", "INTEGER", "WM_CLASS",
		"_NET_WM_NAME", "_NET_WM_ICON_NAME", "_NET_WM_PID", "CARDINAL",
		"WM_NORMAL_HINTS", "WM_SIZE_HINTS", "BAR_DATA"} {
		t.intern(name)
	}
	managerName := "_ST_GO_CLIPBOARD_SYNC_MANAGER"
	if manager, managerErr := xproto.InternAtom(xc, false, uint16(len(managerName)), managerName).Reply(); managerErr == nil {
		t.clipboardManager = manager.Atom
	}

	// window id
	wid, err := xproto.NewWindowId(xc)
	if err != nil {
		return nil, err
	}
	if !limits.Check("window border", int64(t.borderpx), 65535) {
		return nil, fmt.Errorf("invalid window border")
	}
	w := t.borderpx*2 + t.cols*t.cw
	h := t.borderpx*2 + t.rows*t.ch
	if !limits.Grid(t.cols, t.rows) {
		return nil, fmt.Errorf("terminal grid exceeds resource limit")
	}
	if _, ok := limits.Product("window cells", limits.PixelBytes, t.cols, t.rows, t.cw, t.ch, 4); !ok {
		return nil, fmt.Errorf("window cells exceed resource limit")
	}
	if _, ok := limits.RGBA("window", w, h); !ok {
		return nil, fmt.Errorf("window exceeds resource limit")
	}
	if !limits.Check("X window width", int64(w), 65535) || !limits.Check("X window height", int64(h), 65535) {
		return nil, fmt.Errorf("window exceeds X dimensions")
	}

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
	t.clipboardSync = sharedclipboard.New(cachePath, sharedclipboard.Hooks{Changed: t.sharedClipboardChanged})

	return t, nil
}

func (t *Driver) Show() {
	instance, className := t.instance, t.className
	// Before mapping, ensure the tiling WM (gowinmgr) has processed our title
	// into its BAR_DATA property in the "prefix@title@class@instance@desk"
	// format. gowinmgr reads the title on CreateNotify; if it raced our
	// WM_NAME write, the title segment is empty and the window is never tiled.
	// Write the expected format ourselves so WindowMap picks it up.
	t.VerifyIdentity(instance, className)

	xproto.MapWindow(t.conn, t.win)
	t.conn.Sync() // st: XMapWindow then XSync

	// Post-map re-check: gowinmgr's WindowMap may have re-read the title and
	// rewritten BAR_DATA with a desktop suffix; confirm it is still correct.
	t.VerifyIdentity(instance, className)

}

// verifyBarData checks the BAR_DATA property written by gowinmgr. If it is
// missing or its title segment is empty, writes the expected "?@?@..." format
// so the window manager tiles the terminal correctly.
func (t *Driver) VerifyIdentity(instance, className string) {
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

func (t *Driver) intern(name string) {
	rep, err := xproto.InternAtom(t.conn, true, uint16(len(name)), name).Reply()
	if err != nil {
		log.Printf("intern %s: %v", name, err)
		return
	}
	t.atoms[name] = rep.Atom
}

func (t *Driver) setWMHints(winW, winH, x, y int) {
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
func (t *Driver) ClientSize() (width, height int, err error) {
	rep, err := xproto.GetGeometry(t.conn, xproto.Drawable(t.win)).Reply()
	if err != nil {
		return 0, 0, err
	}
	return int(rep.Width), int(rep.Height), nil
}

func (t *Driver) BackendName() string { return "x11" }

func (t *Driver) SetTitle(s string) {
	if s == "" {
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

func uint32Bytes(v uint32) []byte {
	return (*[4]byte)(unsafe.Pointer(&v))[:]
}

func (t *Driver) Close() {
	if !atomic.CompareAndSwapInt32(&t.isClosed, 0, 1) {
		return
	}
	if t.clipboardSync != nil {
		t.clipboardSync.Close()
	}
	if t.pixmap != 0 {
		xproto.FreePixmap(t.conn, t.pixmap)
	}
	if t.win != 0 {
		xproto.DestroyWindow(t.conn, t.win)
	}
	if t.conn != nil {
		t.conn.Close()
	}
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

func (t *Driver) ScreenSize() (int, int) {
	return int(t.scr.WidthInPixels), int(t.scr.HeightInPixels)
}
