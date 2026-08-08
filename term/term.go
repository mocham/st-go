package term

import (
	"strings"

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
	dirty    []bool
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

	// writer receives bytes destined for the pty (set by the frontend).
	writer func([]byte)

	// printerFn writes the print-mode output stream (set by frontend).
	printerFn func([]byte)
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
	ImageDecode(encoded []byte, fitW, fitH bool, page int) (cols, rows int, glyphs []Glyph, ok bool)
	// ImageClearAll clears the image glyph atlas (terminal reset / clear).
	ImageClearAll()
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
func (t *Term) xbell()             { t.hooks.Bell() }
func (t *Term) xclipcopy()         { t.hooks.ClipCopy() }
func (t *Term) xdrawcursor(a, b int, g Glyph, c, d int, og Glyph) {
	t.hooks.DrawCursor(a, b, g, c, d, og)
}
func (t *Term) xdrawline(l []Glyph, a, b, c int) { t.hooks.DrawLine(l, a, b, c) }
func (t *Term) xfinishdraw()                      { t.hooks.FinishDraw() }
func (t *Term) xloadcols()                        { t.hooks.LoadCols() }
func (t *Term) xsetcolorname(i int, s string) bool {
	return t.hooks.SetColorName(i, s)
}
func (t *Term) xgetcolor(i int) (byte, byte, byte, bool) {
	return t.hooks.GetColor(i)
}
func (t *Term) xseticontitle(s string)       { t.hooks.SetIconTitle(s) }
func (t *Term) xsettitle(s string)           { t.hooks.SetTitle(s) }
func (t *Term) xsetcursor(i int) bool        { return t.hooks.SetCursor(i) }
func (t *Term) xsetmode(set bool, m uint)    { t.hooks.SetMode(set, m) }
func (t *Term) xsetpointermotion(on bool)    { t.hooks.SetPointerMotion(on) }
func (t *Term) xsetsel(s string)             { t.hooks.SetSel(s) }
func (t *Term) xstartdraw() bool             { return t.hooks.StartDraw() }

func (t *Term) setWinMode(set bool, mode uint) {
	if set {
		t.winMode |= mode
	} else {
		t.winMode &^= mode
	}
	t.hooks.SetMode(set, mode)
}

func (t *Term) winModeIs(flag uint) bool { return t.winMode&flag != 0 }
