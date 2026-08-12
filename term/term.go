package term

import (
	"fmt"
	"strings"
	"time"

	"st-go/config"
)

// Glyph attributes
const (
	ATTRNull      uint16 = 0
	ATTRBold      uint16 = 1 << 0
	ATTRFaint     uint16 = 1 << 1
	ATTRItalic    uint16 = 1 << 2
	ATTRUnderline uint16 = 1 << 3
	ATTRBlink     uint16 = 1 << 4
	ATTRReverse   uint16 = 1 << 5
	ATTRInvisible uint16 = 1 << 6
	ATTRStruck    uint16 = 1 << 7
	ATTRWrap      uint16 = 1 << 8
	ATTRWide      uint16 = 1 << 9
	ATTRWdummy    uint16 = 1 << 10
)

// Window modes (win.h)
const (
	ModeVisible     uint = 1 << 0
	ModeFocused     uint = 1 << 1
	ModeAppKeypad   uint = 1 << 2
	ModeMouseBtn    uint = 1 << 3
	ModeMouseMotion uint = 1 << 4
	ModeReverse     uint = 1 << 5
	ModeKbdLock     uint = 1 << 6
	ModeHide        uint = 1 << 7
	ModeAppCursor   uint = 1 << 8
	ModeMouseSgr    uint = 1 << 9
	Mode8bit        uint = 1 << 10
	ModeBlink       uint = 1 << 11
	ModeFBlink      uint = 1 << 12
	ModeFocus       uint = 1 << 13
	ModeMouseX10    uint = 1 << 14
	ModeMouseMany   uint = 1 << 15
	ModeBrcktPaste  uint = 1 << 16
	ModeNumLock     uint = 1 << 17

	ModeMouse = ModeMouseBtn | ModeMouseMotion | ModeMouseX10 | ModeMouseMany
)

// Term modes
const (
	termModeWrap      uint = 1 << 0
	termModeInsert    uint = 1 << 1
	termModeAltScreen uint = 1 << 2
	termModeCRLF      uint = 1 << 3
	termModeEcho      uint = 1 << 4
	termModePrint     uint = 1 << 5
	termModeUTF8      uint = 1 << 6
)

// Cursor states
const (
	cursorDefault = iota
	cursorWrapNext
	cursorOrigin
)

// Cursor movement
const (
	cursorSave = iota
	cursorLoad
)

// Charsets
const (
	csGraphic0 = iota
	csGraphic1
	csUK
	csUSA
	csMulti
	csGer
	csFin
)

// Escape states
const (
	escStart      = 1 << 0
	escCSI        = 1 << 1
	escSTR        = 1 << 2
	escAltCharset = 1 << 3
	escStrEnd     = 1 << 4
	escTest       = 1 << 5
	escUTF8       = 1 << 6
)

// Selection modes
const (
	selIdle = iota
	selEmpty
	selReady
)

// Selection types
const (
	SelRegular     = 1
	SelRectangular = 2

	selRegular     = SelRegular
	selRectangular = SelRectangular
)

// Selection snaps
const (
	snapWord = 1
	snapLine = 2
)

const (
	utfInvalid = 0xFFFD
	utfSiz     = 4
	escBufSiz  = 128 * utfSiz
	escArgSiz  = 16
	strBufSiz  = escBufSiz
	strArgSiz  = escArgSiz
)

// MagicColorTag marks a Glyph fg/bg value as a final, already-resolved ARGB
// color that must NOT be recolored (bold/faint/reverse/blink). Used by image
// glyphs, whose cell color is a raw pixel and should be drawn verbatim.
// ImageRune is the magic rune placed in Glyph.U to mark an image cell. The
// image is broken into one glyph per cell right after decoding; the Fg/Bg
// fields of the glyph pack the address of that cell's pixel block in the
// frontend's image atlas. Glyphs stay plain hashable values.
const ImageRune rune = 0xF0000

// TrueColor tag: 1<<24 | r<<16 | g<<8 | b
func TrueColor(r, g, b uint) uint32 { return 1<<24 | uint32(r)<<16 | uint32(g)<<8 | uint32(b) }
func IsTrueColor(x uint32) bool     { return 1<<24&x != 0 }

// Glyph is one terminal cell.
type Glyph struct {
	U    rune
	Mode uint16
	Fg   uint32
	Bg   uint32
}
type Line []Glyph

// TCursor
type TCursor struct {
	attr  Glyph
	x, y  int
	state int
}

type Selection struct {
	mode, typ, snap int
	nb, ne, ob, oe  struct{ x, y int }
	alt             bool
}

type CSIEscape struct {
	buf  []byte
	len  int
	priv bool
	arg  [escArgSiz]int
	narg int
	mode [2]byte
}

type STREscape struct {
	typ  byte
	buf  []byte
	siz  int
	len  int
	args []string
	narg int
}

type Term struct {
	row, col int
	line     []Line
	alt      []Line
	// queue-based painting: changed cell regions awaiting repaint, plus the
	// stop/resume (synchronized update, DECSET 2026) pause counter and the
	// frontend's paint dispatcher.
	regions      []Region
	paintPaused  int
	paintFn      func(flushNow bool)
	c        TCursor
	ocx, ocy int
	top, bot int
	mode     uint
	esc      uint
	trantbl  [4]int
	charset  int
	icharset int
	tabs     []bool
	lastc    rune

	cfg        *config.Config
	hooks      Hooks
	winMode    uint
	worddelims string
	iofd       int

	saveCursor    TCursor
	saveCursorAlt TCursor

	sel       Selection
	csiescseq CSIEscape
	strescseq STREscape

	// image display (DCS DSL)
	pwd string // base directory for relative image paths (setpwd)

	// animated image playback (open DSL `anim` option)
	anim        *animation
	animDrawing bool // set while the animation places its own cells

	// writer receives bytes destined for the pty (set by the frontend).
	writer func([]byte)

	// printerFn writes the print-mode output stream (set by frontend).
	printerFn func([]byte)
}

// ImageDecodeOptions describes how a DCS open command should size an image.
// ViewCols/ViewRows are zero for legacy whole-terminal behavior.
type ImageDecodeOptions struct {
	FitWidth   bool
	FitHeight  bool
	FitContain bool
	Page       int
	ViewCols   int
	ViewRows   int
	Animate    bool // play an animated WebP (open DSL `anim` option)
}

type GeometryUnit uint8

const (
	GeometryPixels GeometryUnit = iota
	GeometryRatio
)

type GeometryValue struct {
	Unit  GeometryUnit
	Value float64
}

type WindowGeometryAction uint8

const (
	GeometryRemember WindowGeometryAction = iota
	GeometryRestore
	GeometryForget
	GeometryPlace
)

// WindowGeometryRequest is emitted by the DCS window command. Anchor is one
// of absolute, top-left, top, top-right, right, bottom-right, bottom,
// bottom-left, or left. X/Y are positions for absolute and offsets otherwise.
type WindowGeometryRequest struct {
	Action     WindowGeometryAction
	Tag        string
	Anchor     string
	X, Y, W, H GeometryValue
	RestoreTag string
}

// Hooks is the frontend interface (mirrors win.h).
type Hooks interface {
	Bell()
	ClipCopy()
	DrawCursor(cx, cy int, g Glyph, ox, oy int, og Glyph)
	DrawLine(line []Glyph, x1, y1, x2 int)
	FinishDraw()
	LoadCols()
	SetColorName(idx int, name string) bool
	GetColor(idx int) (r, g, b byte, ok bool)
	SetIconTitle(s string)
	SetTitle(s string)
	SetCursor(shape int) bool
	SetMode(set bool, mode uint)
	SetPointerMotion(on bool)
	SetSel(s string)
	StartDraw() bool

	// Image support (DCS display DSL).
	// ImageDecode decodes encoded image bytes and breaks it into one glyph
	// per terminal cell (U=ImageRune, Fg/Bg packing the cell's pixel-block
	// address in the frontend atlas). The original image data is freed.
	// Returns the grid size and the glyphs in row-major order.
	ImageDecode(encoded []byte, opts ImageDecodeOptions) (cols, rows int, glyphs []Glyph, ok bool)
	// ImageDecodeAnim decodes an animated image's metadata for the open DSL
	// `anim` option: per-frame durations (ms), frame count and canvas grid.
	ImageDecodeAnim(encoded []byte, opts ImageDecodeOptions) (durations []int, frameCount, cols, rows int, ok bool)
	// ImageDecodeAnimFrame decodes one frame (0-based index) of an animated
	// image into glyphs, on demand as the animation plays.
	ImageDecodeAnimFrame(encoded []byte, frameIdx int, opts ImageDecodeOptions) (cols, rows int, glyphs []Glyph, ok bool)
	// ImageClearAll clears the image glyph atlas (terminal reset / clear).
	ImageClearAll()
	WindowGeometry(req WindowGeometryRequest)
}

// utfbyte/mask/min/max tables (port of st.c globals)
var (
	utfbyte = [...]byte{0x80, 0, 0xC0, 0xE0, 0xF0}
	utfmask = [...]byte{0xC0, 0x80, 0xE0, 0xF0, 0xF8}
	utfmin  = [...]rune{0, 0, 0x80, 0x800, 0x10000}
	utfmax  = [...]rune{0x10FFFF, 0x7F, 0x7FF, 0xFFFF, 0x10FFFF}
)

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}
func between(x, a, b int) bool { return a <= x && x <= b }
func divceil(n, d int) int     { return (n + d - 1) / d }

func modbit(x *int, set bool, bit int) {
	if set {
		*x |= bit
	} else {
		*x &^= bit
	}
}

func clamp(x, a, b int) int {
	if x < a {
		return a
	}
	if x > b {
		return b
	}
	return x
}

func utf8decodeByte(c byte, i *int) rune {
	for *i = 0; *i < len(utfmask); *i++ {
		if c&utfmask[*i] == utfbyte[*i] {
			return rune(c) &^ rune(utfmask[*i])
		}
	}
	return 0
}

// utf8decode decodes up to clen bytes from s. Returns (rune, bytes consumed).
// Consumed == 0 means incomplete sequence (needs more data).
func utf8decode(s []byte, clen int) (rune, int) {
	if clen == 0 {
		return utfInvalid, 0
	}
	var len_ int
	udecoded := utf8decodeByte(s[0], &len_)
	if len_ < 1 || len_ > utfSiz {
		return utfInvalid, 1
	}
	j := 1
	for i := 1; i < clen && j < len_; i, j = i+1, j+1 {
		var typ int
		udecoded = udecoded<<6 | utf8decodeByte(s[i], &typ)
		if typ != 0 {
			return utfInvalid, j
		}
	}
	if j < len_ {
		return utfInvalid, 0
	}
	utf8validate(&udecoded, len_)
	return udecoded, len_
}

func utf8encodeByte(u rune, i int) byte {
	return utfbyte[i] | byte(u)&^utfmask[i]
}

func utf8validate(u *rune, i int) int {
	if !(*u >= utfmin[i] && *u <= utfmax[i]) || (*u >= 0xD800 && *u <= 0xDFFF) {
		*u = utfInvalid
	}
	for i = 1; *u > utfmax[i]; i++ {
	}
	return i
}

// utf8encode encodes a rune. Returns the UTF-8 bytes.
func utf8encode(u rune) []byte {
	len_ := utf8validate(&u, 0)
	if len_ > utfSiz {
		return nil
	}
	c := make([]byte, len_)
	for i := len_ - 1; i != 0; i-- {
		c[i] = utf8encodeByte(u, 0)
		u >>= 6
	}
	c[0] = utf8encodeByte(u, len_)
	return c
}

// runeWidth returns the display width of a rune (wcwidth). -1 if unprintable.
func runeWidth(u rune) int {
	switch {
	case u == 0 || u == '\x00':
		return 0
	case u < 32 || (u >= 0x7f && u < 0xa0):
		return -1
	}
	// Use unicode wide tables (East Asian Wide / Fullwidth = 2).
	return runeWidthTbl(u)
}

func (t *Term) isSet(flag uint) bool { return t.mode&flag != 0 }

func (t *Term) isControlC0(c rune) bool { return between(int(c), 0, 0x1f) || c == 0x7f }
func (t *Term) isControlC1(c rune) bool { return between(int(c), 0x80, 0x9f) }
func (t *Term) isControl(c rune) bool   { return t.isControlC0(c) || t.isControlC1(c) }
func (t *Term) isDelim(u rune) bool {
	return u != 0 && strings.ContainsRune(t.worddelims, u)
}

// NewTerm creates a terminal with the given config and hooks.
func NewTerm(cfg *config.Config, hooks Hooks) *Term {
	t := &Term{
		cfg:        cfg,
		hooks:      hooks,
		worddelims: cfg.WordDelimiters,
		iofd:       1,
	}
	t.SelInit()
	t.New(int(cfg.Cols), int(cfg.Rows))
	return t
}

// win.h wrapper functions forwarded to hooks (called from core).
func (t *Term) xbell()     { t.hooks.Bell() }
func (t *Term) xclipcopy() { t.hooks.ClipCopy() }
func (t *Term) xdrawcursor(a, b int, g Glyph, c, d int, og Glyph) {
	t.hooks.DrawCursor(a, b, g, c, d, og)
}
func (t *Term) xdrawline(l []Glyph, a, b, c int) { t.hooks.DrawLine(l, a, b, c) }
func (t *Term) xfinishdraw()                     { t.hooks.FinishDraw() }
func (t *Term) xloadcols()                       { t.hooks.LoadCols() }
func (t *Term) xsetcolorname(i int, s string) bool {
	return t.hooks.SetColorName(i, s)
}
func (t *Term) xgetcolor(i int) (byte, byte, byte, bool) {
	return t.hooks.GetColor(i)
}
func (t *Term) xseticontitle(s string)    { t.hooks.SetIconTitle(s) }
func (t *Term) xsettitle(s string)        { t.hooks.SetTitle(s) }
func (t *Term) xsetcursor(i int) bool     { return t.hooks.SetCursor(i) }
func (t *Term) xsetmode(set bool, m uint) { t.hooks.SetMode(set, m) }
func (t *Term) xsetpointermotion(on bool) { t.hooks.SetPointerMotion(on) }
func (t *Term) xsetsel(s string)          { t.hooks.SetSel(s) }
func (t *Term) xstartdraw() bool          { return t.hooks.StartDraw() }

func (t *Term) setWinMode(set bool, mode uint) {
	if set {
		t.winMode |= mode
	} else {
		t.winMode &^= mode
	}
	t.hooks.SetMode(set, mode)
}

func (t *Term) winModeIs(flag uint) bool { return t.winMode&flag != 0 }

// --- animated image playback (open DSL `anim` option) ----------------------
// A decoded animated WebP is played in a fixed rect: the frontend calls
// TickAnim on a timer, and each tick swaps the frame glyphs and redraws. The
// animation plays once and then holds the last frame; a progress line is drawn
// in the bottom row of the rect. Any write/erase into the rect cancels it.

// animation holds the playback state of an animated image (open DSL `anim`
// option). It keeps the encoded bytes and timing metadata, and decodes each
// frame on demand (via ImageDecodeAnimFrame) as it plays, so a playing
// animation's memory stays bounded. Plays once, then holds the last frame; a
// progress line is drawn in the bottom row of the rect. Any write/erase into
// the rect cancels it.
type animation struct {
	data      []byte // encoded animated image
	opts      ImageDecodeOptions
	durations []int // ms per frame
	frameCount int
	cols, rows int
	rectX, rectY, rectW, rectH int
	startAt time.Time
	idx     int
	done    bool
}

// SetAnim starts playing an animated image in the rect (rx,ry,rw,rh),
// replacing any existing animation. The first frame is drawn immediately.
func (t *Term) SetAnim(data []byte, opts ImageDecodeOptions, durations []int, cols, rows, rx, ry, rw, rh int) {
	t.anim = &animation{
		data: data, opts: opts, durations: durations,
		frameCount: len(durations),
		cols: cols, rows: rows,
		rectX: rx, rectY: ry, rectW: rw, rectH: rh,
		startAt: time.Now(), idx: -1,
	}
	t.TickAnim(time.Now())
}

// HasAnim reports whether an animation is currently running (not yet done).
func (t *Term) HasAnim() bool { return t.anim != nil && !t.anim.done }

// CancelAnims removes all running animations (e.g. terminal clear/reset).
func (t *Term) CancelAnims() { t.anim = nil }

// cancelAnimCell cancels the animation if the cell lies inside its rect.
func (t *Term) cancelAnimCell(x, y int) {
	if t.anim == nil || t.animDrawing {
		return
	}
	a := t.anim
	if x >= a.rectX && x < a.rectX+a.rectW && y >= a.rectY && y < a.rectY+a.rectH {
		t.anim = nil
	}
}

// cancelAnimRegion cancels the animation if the region overlaps its rect.
func (t *Term) cancelAnimRegion(x1, y1, x2, y2 int) {
	if t.anim == nil || t.animDrawing {
		return
	}
	a := t.anim
	if x1 < a.rectX+a.rectW && x2 >= a.rectX && y1 < a.rectY+a.rectH && y2 >= a.rectY {
		t.anim = nil
	}
}

// TickAnim advances the running animation to the frame due at now, decoding
// that frame on demand (recycling the previous frame's bitmap). It returns
// true when the screen changed and the frontend should redraw.
func (t *Term) TickAnim(now time.Time) bool {
	a := t.anim
	if a == nil || a.done || a.frameCount == 0 {
		return false
	}
	elapsed := now.Sub(a.startAt)
	target, cum := 0, 0
	for _, d := range a.durations {
		if elapsed < time.Duration(cum+d)*time.Millisecond {
			break
		}
		cum += d
		target++
	}
	if target >= a.frameCount {
		// play once: done, hold the last frame
		a.done = true
		if a.idx == a.frameCount-1 {
			return false
		}
		target = a.frameCount - 1
	}
	if target == a.idx {
		return false
	}
	cols, rows, glyphs, ok := t.hooks.ImageDecodeAnimFrame(a.data, target, a.opts)
	if !ok {
		a.done = true
		return false
	}
	a.cols, a.rows = cols, rows
	t.drawAnimFrame(a, target, glyphs)
	a.idx = target
	return true
}

// drawAnimFrame places a frame's glyphs in the animation rect, reserving the
// bottom row for a progress line, and marks the rows dirty.
func (t *Term) drawAnimFrame(a *animation, idx int, glyphs []Glyph) {
	t.animDrawing = true
	defer func() { t.animDrawing = false }()

	// one animation frame = one atomic paint batch = one flush (stop/resume).
	t.PaintStop()
	defer t.PaintResume()

	imgH := a.rectH - 1 // bottom row holds the progress line
	if imgH < 1 {
		imgH = 1
	}
	t.tclearregion(a.rectX, a.rectY, a.rectX+a.rectW-1, a.rectY+a.rectH-1)
	startX := a.rectX + max(0, (a.rectW-min(a.cols, a.rectW))/2)
	startY := a.rectY + max(0, (imgH-min(a.rows, imgH))/2)
	for gy := 0; gy < a.rows; gy++ {
		y := startY + gy
		if y >= a.rectY+imgH {
			break
		}
		for gx := 0; gx < a.cols; gx++ {
			x := startX + gx
			if x >= a.rectX+a.rectW {
				break
			}
			gi := gy*a.cols + gx
			if gi >= len(glyphs) {
				break
			}
			t.tsetchar(glyphs[gi].U, &glyphs[gi], x, y)
		}
	}
	// progress line in the bottom row of the rect
	pct := (idx + 1) * 100 / a.frameCount
	label := fmt.Sprintf("WEBP %3d%%", pct)
	attr := Glyph{Mode: ATTRBold, Fg: uint32(t.cfg.DefaultFg), Bg: uint32(t.cfg.DefaultBg)}
	for i := 0; i < len(label) && a.rectX+i < a.rectX+a.rectW; i++ {
		attr.U = rune(label[i])
		t.tsetchar(attr.U, &attr, a.rectX+i, a.rectY+a.rectH-1)
	}
}

// --- queue-based painting -------------------------------------------------
// Virtual painting (Twrite, image placement, animation frames, selection)
// marks changed cell regions; a single actual-paint worker drains them, draws
// the union into the framebuffer and sends one X11 blit per batch. DECSET 2026
// (PaintStop/PaintResume) lets applications batch output and flush once.

// Region is an inclusive cell rectangle pending repaint.
type Region struct {
	X1, Y1, X2, Y2 int
}

// Empty reports whether the region is invalid (nothing to paint).
func (r Region) Empty() bool { return r.X1 > r.X2 || r.Y1 > r.Y2 }

func regionUnion(a, b Region) Region {
	if b.X1 < a.X1 {
		a.X1 = b.X1
	}
	if b.Y1 < a.Y1 {
		a.Y1 = b.Y1
	}
	if b.X2 > a.X2 {
		a.X2 = b.X2
	}
	if b.Y2 > a.Y2 {
		a.Y2 = b.Y2
	}
	return a
}

// markDirty queues a changed cell region for repaint, clamped to the screen.
// The queue is bounded: once it grows large, entries coalesce into their
// bounding region.
func (t *Term) markDirty(x1, y1, x2, y2 int) {
	if x1 > x2 {
		x1, x2 = x2, x1
	}
	if y1 > y2 {
		y1, y2 = y2, y1
	}
	x1 = clamp(x1, 0, t.col-1)
	x2 = clamp(x2, 0, t.col-1)
	y1 = clamp(y1, 0, t.row-1)
	y2 = clamp(y2, 0, t.row-1)
	if x1 > x2 || y1 > y2 {
		return
	}
	if len(t.regions) >= 64 {
		r := t.regions[0]
		for _, q := range t.regions[1:] {
			r = regionUnion(r, q)
		}
		t.regions = []Region{regionUnion(r, Region{x1, y1, x2, y2})}
		return
	}
	t.regions = append(t.regions, Region{x1, y1, x2, y2})
}

// TakeRegions drains the pending repaint regions.
func (t *Term) TakeRegions() []Region {
	regs := t.regions
	t.regions = nil
	return regs
}

// HasPendingPaint reports whether regions await repaint.
func (t *Term) HasPendingPaint() bool { return len(t.regions) > 0 }

// IsPaintPaused reports whether repainting is currently stopped (2026h).
func (t *Term) IsPaintPaused() bool { return t.paintPaused > 0 }

// SetPaintFn registers the frontend's paint dispatcher. When nil, Draw runs
// synchronously on a paint request (tests without a paint worker).
func (t *Term) SetPaintFn(fn func(flushNow bool)) { t.paintFn = fn }

// PaintStop defers repainting until the matching PaintResume (DECSET 2026h).
// Regions keep queueing while paused; the resume flushes them as one batch.
func (t *Term) PaintStop() {
	t.paintPaused++
	if t.paintPaused > 64 {
		t.paintPaused = 64
	}
}

// PaintResume restarts painting and requests one repaint of the queued batch.
func (t *Term) PaintResume() {
	if t.paintPaused > 0 {
		t.paintPaused--
	}
	if t.paintPaused == 0 {
		t.requestPaint(true)
	}
}

func (t *Term) requestPaint(flushNow bool) {
	if t.paintPaused > 0 {
		return
	}
	if t.paintFn != nil {
		t.paintFn(flushNow)
	} else {
		t.Draw()
	}
}

// PaintDirty requests a repaint of the currently queued regions without
// enqueuing a full-screen region. This is the per-output paint trigger.
func (t *Term) PaintDirty() { t.requestPaint(false) }
