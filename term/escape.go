package term

import (
	"fmt"
	"log"
	"os"
	"strconv"
	"strings"
)

func (t *Term) tdefcolor(attr []int, npar int, l int) (int32, int) {
	idx := int32(-1)
	if npar+1 >= l {
		log.Printf("erresc(38): Incorrect number of parameters (%d)\n", npar)
		return idx, npar
	}
	switch attr[npar+1] {
	case 2:
		if npar+4 >= l {
			log.Printf("erresc(38): Incorrect number of parameters (%d)\n", npar)
			break
		}
		r, g, b := attr[npar+2], attr[npar+3], attr[npar+4]
		npar += 4
		if !between(r, 0, 255) || !between(g, 0, 255) || !between(b, 0, 255) {
			log.Printf("erresc: bad rgb color (%d,%d,%d)\n", r, g, b)
		} else {
			idx = int32(TrueColor(uint(r), uint(g), uint(b)))
		}
	case 5:
		if npar+2 >= l {
			log.Printf("erresc(38): Incorrect number of parameters (%d)\n", npar)
			break
		}
		npar += 2
		if !between(attr[npar], 0, 255) {
			log.Printf("erresc: bad fgcolor %d\n", attr[npar])
		} else {
			idx = int32(attr[npar])
		}
	default:
		log.Printf("erresc(38): gfx attr %d unknown\n", attr[npar])
	}
	return idx, npar
}

func (t *Term) tsetattr(attr []int, l int) {
	for i := 0; i < l; i++ {
		switch attr[i] {
		case 0:
			t.c.attr.Mode &^= ATTRBold | ATTRFaint | ATTRItalic | ATTRUnderline |
				ATTRBlink | ATTRReverse | ATTRInvisible | ATTRStruck
			t.c.attr.Fg = uint32(t.cfg.DefaultFg)
			t.c.attr.Bg = uint32(t.cfg.DefaultBg)
		case 1:
			t.c.attr.Mode |= ATTRBold
		case 2:
			t.c.attr.Mode |= ATTRFaint
		case 3:
			t.c.attr.Mode |= ATTRItalic
		case 4:
			t.c.attr.Mode |= ATTRUnderline
		case 5, 6:
			t.c.attr.Mode |= ATTRBlink
		case 7:
			t.c.attr.Mode |= ATTRReverse
		case 8:
			t.c.attr.Mode |= ATTRInvisible
		case 9:
			t.c.attr.Mode |= ATTRStruck
		case 22:
			t.c.attr.Mode &^= ATTRBold | ATTRFaint
		case 23:
			t.c.attr.Mode &^= ATTRItalic
		case 24:
			t.c.attr.Mode &^= ATTRUnderline
		case 25:
			t.c.attr.Mode &^= ATTRBlink
		case 27:
			t.c.attr.Mode &^= ATTRReverse
		case 28:
			t.c.attr.Mode &^= ATTRInvisible
		case 29:
			t.c.attr.Mode &^= ATTRStruck
		case 38:
			if idx, npar := t.tdefcolor(attr, i, l); idx >= 0 {
				t.c.attr.Fg = uint32(idx)
				i = npar
			}
		case 39:
			t.c.attr.Fg = uint32(t.cfg.DefaultFg)
		case 48:
			if idx, npar := t.tdefcolor(attr, i, l); idx >= 0 {
				t.c.attr.Bg = uint32(idx)
				i = npar
			}
		case 49:
			t.c.attr.Bg = uint32(t.cfg.DefaultBg)
		default:
			switch {
			case between(attr[i], 30, 37):
				t.c.attr.Fg = uint32(attr[i] - 30)
			case between(attr[i], 40, 47):
				t.c.attr.Bg = uint32(attr[i] - 40)
			case between(attr[i], 90, 97):
				t.c.attr.Fg = uint32(attr[i] - 90 + 8)
			case between(attr[i], 100, 107):
				t.c.attr.Bg = uint32(attr[i] - 100 + 8)
			default:
				log.Printf("erresc(default): gfx attr %d unknown\n", attr[i])
			}
		}
	}
}

func (t *Term) tsetmode(priv, set bool, args []int, narg int) {
	for i := 0; i < narg; i++ {
		a := args[i]
		if priv {
			switch a {
			case 1:
				t.setWinMode(set, ModeAppCursor)
			case 5:
				t.setWinMode(set, ModeReverse)
			case 6:
				modbit(&t.c.state, set, cursorOrigin)
				t.tmoveato(0, 0)
			case 7:
				if set {
					t.mode |= termModeWrap
				} else {
					t.mode &^= termModeWrap
				}
			case 25:
				t.setWinMode(!set, ModeHide)
			case 9:
				t.xsetpointermotion(false)
				t.setWinMode(false, ModeMouse)
				t.setWinMode(set, ModeMouseX10)
			case 1000:
				t.xsetpointermotion(false)
				t.setWinMode(false, ModeMouse)
				t.setWinMode(set, ModeMouseBtn)
			case 1002:
				t.xsetpointermotion(false)
				t.setWinMode(false, ModeMouse)
				t.setWinMode(set, ModeMouseMotion)
			case 1003:
				t.xsetpointermotion(set)
				t.setWinMode(false, ModeMouse)
				t.setWinMode(set, ModeMouseMany)
			case 1004:
				t.setWinMode(set, ModeFocus)
			case 1006:
				t.setWinMode(set, ModeMouseSgr)
			case 1034:
				t.setWinMode(set, Mode8bit)
			case 1049:
				if !t.cfg.AllowAltScreen {
					break
				}
				if set {
					t.tcursor(cursorSave)
				} else {
					t.tcursor(cursorLoad)
				}
				fallthrough
			case 47, 1047:
				if !t.cfg.AllowAltScreen {
					break
				}
				alt := t.isSet(termModeAltScreen)
				if alt {
					t.tclearregion(0, 0, t.col-1, t.row-1)
				}
				if set != alt {
					t.tswapscreen()
				}
				if a != 1049 {
					break
				}
				fallthrough
			case 1048:
				if set {
					t.tcursor(cursorSave)
				} else {
					t.tcursor(cursorLoad)
				}
			case 2004:
				t.setWinMode(set, ModeBrcktPaste)
			case 1001, 1005, 1015:
			default:
				log.Printf("erresc: unknown private set/reset mode %d\n", a)
			}
		} else {
			switch a {
			case 2:
				t.setWinMode(set, ModeKbdLock)
			case 4:
				if set {
					t.mode |= termModeInsert
				} else {
					t.mode &^= termModeInsert
				}
			case 12:
				if !set {
					t.mode |= termModeEcho
				} else {
					t.mode &^= termModeEcho
				}
			case 20:
				if set {
					t.mode |= termModeCRLF
				} else {
					t.mode &^= termModeCRLF
				}
			default:
				log.Printf("erresc: unknown set/reset mode %d\n", a)
			}
		}
	}
}

func (t *Term) csihandle() {
	arg := t.csiescseq.arg
	narg := t.csiescseq.narg
	get := func(i, d int) int {
		// st's DEFAULT(arg, 1) semantics: an absent or zero argument
		// falls back to the default (used for cursor movements etc.).
		if i < narg {
			if arg[i] != 0 || d == 0 {
				return arg[i]
			}
			return d
		}
		return d
	}

	unknown := func() {
		log.Printf("erresc: unknown csi ")
		t.csidump()
	}

	switch t.csiescseq.mode[0] {
	default:
		unknown()
	case '@':
		t.tinsertblank(get(0, 1))
	case 'A':
		t.tmoveto(t.c.x, t.c.y-get(0, 1))
	case 'B', 'e':
		t.tmoveto(t.c.x, t.c.y+get(0, 1))
	case 'i':
		switch get(0, 0) {
		case 0:
			t.dump()
		case 1:
			t.dumpLine(t.c.y)
		case 2:
			t.printer([]byte(t.GetSel()))
		case 4:
			t.mode &^= termModePrint
		case 5:
			t.mode |= termModePrint
		}
	case 'c':
		if get(0, 0) == 0 {
			t.ttywrite([]byte(t.cfg.Vtiden), false)
		}
	case 'b':
		rep := clamp(get(0, 1), 1, 65535)
		if t.lastc != 0 {
			for rep > 0 {
				t.tputc(t.lastc)
				rep--
			}
		}
	case 'C', 'a':
		t.tmoveto(t.c.x+get(0, 1), t.c.y)
	case 'D':
		t.tmoveto(t.c.x-get(0, 1), t.c.y)
	case 'E':
		t.tmoveto(0, t.c.y+get(0, 1))
	case 'F':
		t.tmoveto(0, t.c.y-get(0, 1))
	case 'g':
		switch get(0, 0) {
		case 0:
			t.tabs[t.c.x] = false
		case 3:
			for i := range t.tabs {
				t.tabs[i] = false
			}
		default:
			unknown()
		}
	case 'G', '`':
		t.tmoveto(get(0, 1)-1, t.c.y)
	case 'H', 'f':
		t.tmoveato(get(1, 1)-1, get(0, 1)-1)
	case 'I':
		t.tputtab(get(0, 1))
	case 'J':
		switch get(0, 0) {
		case 0:
			t.tclearregion(t.c.x, t.c.y, t.col-1, t.c.y)
			if t.c.y < t.row-1 {
				t.tclearregion(0, t.c.y+1, t.col-1, t.row-1)
			}
		case 1:
			if t.c.y > 1 {
				t.tclearregion(0, 0, t.col-1, t.c.y-1)
			}
			t.tclearregion(0, t.c.y, t.c.x, t.c.y)
		case 2:
			t.tclearregion(0, 0, t.col-1, t.row-1)
		default:
			unknown()
		}
	case 'K':
		switch get(0, 0) {
		case 0:
			t.tclearregion(t.c.x, t.c.y, t.col-1, t.c.y)
		case 1:
			t.tclearregion(0, t.c.y, t.c.x, t.c.y)
		case 2:
			t.tclearregion(0, t.c.y, t.col-1, t.c.y)
		}
	case 'S':
		if t.csiescseq.priv {
			break
		}
		t.tscrollup(t.top, get(0, 1))
	case 'T':
		t.tscrolldown(t.top, get(0, 1))
	case 'L':
		t.tinsertblankline(get(0, 1))
	case 'l':
		t.tsetmode(t.csiescseq.priv, false, arg[:narg], narg)
	case 'M':
		t.tdeleteline(get(0, 1))
	case 'X':
		t.tclearregion(t.c.x, t.c.y, t.c.x+get(0, 1)-1, t.c.y)
	case 'P':
		t.tdeletechar(get(0, 1))
	case 'Z':
		t.tputtab(-get(0, 1))
	case 'd':
		t.tmoveato(t.c.x, get(0, 1)-1)
	case 'h':
		t.tsetmode(t.csiescseq.priv, true, arg[:narg], narg)
	case 'm':
		t.tsetattr(arg[:narg], narg)
	case 'n':
		switch get(0, 0) {
		case 5:
			t.ttywrite([]byte("\x1b[0n"), false)
		case 6:
			s := fmt.Sprintf("\x1b[%d;%dR", t.c.y+1, t.c.x+1)
			t.ttywrite([]byte(s), false)
		default:
			unknown()
		}
	case 'r':
		if t.csiescseq.priv {
			unknown()
		} else {
			t.tsetscroll(get(0, 1)-1, get(1, t.row)-1)
			t.tmoveato(0, 0)
		}
	case 's':
		t.tcursor(cursorSave)
	case 'u':
		t.tcursor(cursorLoad)
	case ' ':
		if t.csiescseq.mode[1] == 'q' {
			if t.xsetcursor(get(0, 0)) {
				unknown()
			}
		} else {
			unknown()
		}
	}
}

func (t *Term) csidump() {
	log.Printf("ESC[%s\n", string(t.csiescseq.buf))
}

func (t *Term) oscColorResponse(num, index int, isOsc4 bool) {
	r, g, b, ok := t.xgetcolor(index)
	if !ok {
		name := "osc"
		if isOsc4 {
			name = "osc4"
		}
		n := index
		if isOsc4 {
			n = num
		}
		log.Printf("erresc: failed to fetch %s color %d\n", name, n)
		return
	}
	prefix := ""
	if isOsc4 {
		prefix = "4;"
	}
	s := fmt.Sprintf("\x1b]%s%d;rgb:%02x%02x/%02x%02x/%02x%02x\x07",
		prefix, num, r, r, g, g, b, b)
	t.ttywrite([]byte(s), true)
}

func (t *Term) strhandle() {
	t.esc &^= escStrEnd | escSTR
	t.strparse()
	par := 0
	if t.strescseq.narg > 0 {
		par, _ = strconv.Atoi(t.strescseq.args[0])
	}

	oscTable := []struct {
		idx uint
		str string
	}{
		{t.cfg.DefaultFg, "foreground"},
		{t.cfg.DefaultBg, "background"},
		{t.cfg.DefaultCs, "cursor"},
	}

	switch t.strescseq.typ {
	case ']':
		switch par {
		case 0:
			if t.strescseq.narg > 1 {
				t.xsettitle(t.strescseq.args[1])
				t.xseticontitle(t.strescseq.args[1])
			}
			return
		case 1:
			if t.strescseq.narg > 1 {
				t.xseticontitle(t.strescseq.args[1])
			}
			return
		case 2:
			if t.strescseq.narg > 1 {
				t.xsettitle(t.strescseq.args[1])
			}
			return
		case 52:
			if t.strescseq.narg > 2 && t.cfg.AllowWindowOps {
				dec := base64dec(t.strescseq.args[2])
				if len(dec) > 0 {
					t.xsetsel(dec)
					t.xclipcopy()
				} else {
					log.Printf("erresc: invalid base64\n")
				}
			}
			return
		case 10, 11, 12:
			if t.strescseq.narg < 2 {
				break
			}
			p := t.strescseq.args[1]
			j := par - 10
			if j < 0 || j >= len(oscTable) {
				break
			}
			if p == "?" {
				t.oscColorResponse(par, int(oscTable[j].idx), false)
			} else if !t.xsetcolorname(int(oscTable[j].idx), p) {
				t.tfulldirt()
			} else {
				log.Printf("erresc: invalid %s color: %s\n", oscTable[j].str, p)
			}
			return
		case 4:
			if t.strescseq.narg < 3 {
				break
			}
			p := t.strescseq.args[2]
			j := -1
			if t.strescseq.narg > 1 {
				j, _ = strconv.Atoi(t.strescseq.args[1])
			}
			if p == "?" {
				t.oscColorResponse(j, 0, true)
			} else if !t.xsetcolorname(j, p) {
				t.tfulldirt()
			} else {
				log.Printf("erresc: invalid color j=%d, p=%s\n", j, p)
			}
			return
		case 104:
			j := -1
			if t.strescseq.narg > 1 {
				j, _ = strconv.Atoi(t.strescseq.args[1])
			}
			if t.strescseq.narg <= 1 {
				t.xloadcols()
				return
			}
			if !t.xsetcolorname(j, "") {
				t.tfulldirt()
			} else {
				log.Printf("erresc: invalid color j=%d\n", j)
			}
			return
		}
	case 'k':
		if t.strescseq.narg > 0 {
			t.xsettitle(t.strescseq.args[0])
		}
		return
	case '_', '^':
		// APC / PM: ignored
		return
	case 'P':
		// DCS: image/display DSL (device control string)
		t.dcs()
		return
	}
	log.Printf("erresc: unknown str ")
	t.strdump()
}

func (t *Term) strparse() {
	t.strescseq.narg = 0
	t.strescseq.args = t.strescseq.args[:0]
	if len(t.strescseq.buf) == 0 {
		return
	}
	parts := splitStr(t.strescseq.buf, ';')
	for _, p := range parts {
		if t.strescseq.narg == strArgSiz {
			break
		}
		t.strescseq.args = append(t.strescseq.args, p)
		t.strescseq.narg++
	}
}

// dcs handles a DCS (device control string) payload as a small display DSL.
//
//   ESC P <statement>; <statement>; ... ESC \
//
// Each statement is `command args...`; a statement ends with ';'. Quoted
// strings ('...' or "...") keep spaces inside a single argument. Unknown
// commands are ignored, so the DSL is forward-extensible.
//
// Supported commands:
//   open '<path>' [col row]   load and display an image file at the cursor
//                             (or at an explicit cell position)
//   clear                     remove all images and placements
//   delete <id>               remove one transmitted image
func (t *Term) dcs() {
	stmt := string(t.strescseq.buf)
	// split into statements on ';'
	for len(stmt) > 0 {
		i := indexByteStr(stmt, ';')
		var one string
		if i < 0 {
			one = stmt
			stmt = ""
		} else {
			one = stmt[:i]
			stmt = stmt[i+1:]
		}
		one = strings.TrimSpace(one)
		if one == "" {
			continue
		}
		args := dslTokenize(one)
		if len(args) == 0 {
			continue
		}
		switch args[0] {
		case "open":
			t.dslOpen(args[1:])
		case "clear":
			t.dslClear()
		case "delete":
			t.dslDelete(args[1:])
		default:
			log.Printf("dsl: unknown command %q\n", args[0])
		}
	}
}

// dslOpen loads an image file and displays it. Options (in any order after
// the path):
//   fit-width     scale the image to the terminal width
//   fit-height    clear the screen, then scale to the terminal height
// Without a fit option the image is shown at its native cell size.
// The image is written row by row at the cursor, advancing the cursor (and
// scrolling at the bottom) exactly like text.
func (t *Term) dslOpen(args []string) {
	if len(args) == 0 {
		log.Printf("dsl: open requires a path\n")
		return
	}
	path := args[0]
	fitW, fitH := false, false
	for _, a := range args[1:] {
		switch a {
		case "fit-width":
			fitW = true
		case "fit-height":
			fitH = true
		}
	}

	data, err := os.ReadFile(path)
	if err != nil {
		log.Printf("dsl: open %q: %v\n", path, err)
		return
	}
	cols, rows, glyphs, ok := t.hooks.ImageDecode(data, fitW, fitH)
	if !ok {
		log.Printf("dsl: failed to decode %q\n", path)
		return
	}
	if cols < 1 {
		cols = 1
	}
	if rows < 1 {
		rows = 1
	}

	// fit-height clears the screen and starts at the top
	if fitH {
		t.tclearregion(0, 0, t.col-1, t.row-1)
		t.tmoveto(0, 0)
	}

	// write each row of the image at the cursor, advancing like text
	startX := t.c.x
	for gy := 0; gy < rows; gy++ {
		for gx := 0; gx < cols; gx++ {
			if t.c.x+gx >= t.col {
				break
			}
			// image-cell glyph: plain value with U=ImageRune and the cell's
			// pixel-block address packed in Fg
			idx := gy*cols + gx
			if idx >= len(glyphs) {
				break
			}
			t.tsetchar(glyphs[idx].U, &glyphs[idx], t.c.x+gx, t.c.y)
		}
		// advance to the next row, scrolling at the bottom like a newline;
		// skip the advance after the final row so the last row stays put
		if gy == rows-1 {
			break
		}
		if t.c.y == t.bot {
			t.tscrollup(t.top, 1)
		} else {
			t.tmoveto(startX, t.c.y+1)
		}
	}
	// leave the cursor at the start of the image's last row
	t.tmoveto(startX, t.c.y)
	t.tfulldirt()
}

func (t *Term) dslClear() {
	// clear the visible screen and drop all cached images
	t.tclearregion(0, 0, t.col-1, t.row-1)
	if t.hooks != nil {
		t.hooks.ImageClearAll()
	}
	t.tfulldirt()
}

func (t *Term) dslDelete(args []string) {
	// images are broken into glyphs and not retained by id; deleting clears
	// the screen and atlas
	t.dslClear()
}

// imageCellGlyph returns a plain image-cell glyph whose Fg packs the address
// of the cell's pixel block in the frontend atlas.

// dslTokenize splits a statement into arguments, honoring single- and
// double-quoted strings and skipping runs of whitespace.
func dslTokenize(s string) []string {
	var out []string
	var cur strings.Builder
	quote := byte(0)
	flush := func() {
		if cur.Len() > 0 {
			out = append(out, cur.String())
			cur.Reset()
		}
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case quote != 0:
			if c == quote {
				quote = 0
			} else {
				cur.WriteByte(c)
			}
		case c == '\'' || c == '"':
			quote = c
		case c == ' ' || c == '\t':
			flush()
		default:
			cur.WriteByte(c)
		}
	}
	flush()
	return out
}

func indexByteStr(s string, c byte) int {
	for i := 0; i < len(s); i++ {
		if s[i] == c {
			return i
		}
	}
	return -1
}

func splitStr(b []byte, sep byte) []string {
	var out []string
	start := 0
	for i, c := range b {
		if c == sep {
			out = append(out, string(b[start:i]))
			start = i + 1
		}
	}
	out = append(out, string(b[start:]))
	return out
}

func (t *Term) strdump() {
	log.Printf("ESC%c%s ESC\\\n", t.strescseq.typ, string(t.strescseq.buf))
}

func (t *Term) strreset() {
	if t.strescseq.buf == nil {
		t.strescseq.siz = strBufSiz
		t.strescseq.buf = make([]byte, 0, strBufSiz)
	} else {
		t.strescseq.buf = t.strescseq.buf[:0]
	}
	t.strescseq.len = 0
}

func (t *Term) tcontrolcode(ascii rune) {
	switch ascii {
	case '\t':
		t.tputtab(1)
		return
	case '\b':
		t.tmoveto(t.c.x-1, t.c.y)
		return
	case '\r':
		t.tmoveto(0, t.c.y)
		return
	case '\f', '\v', '\n':
		t.tnewline(t.isSet(termModeCRLF))
		return
	case '\a':
		if t.esc&escStrEnd != 0 {
			t.strhandle()
		} else {
			t.xbell()
		}
	case '\033':
		t.csiescseq = CSIEscape{}
		t.esc &^= escCSI | escAltCharset | escTest
		t.esc |= escStart
		return
	case '\016', '\017':
		t.charset = 1 - int(ascii-'\016')
		return
	case '\032':
		t.tsetchar('?', &t.c.attr, t.c.x, t.c.y)
		fallthrough
	case '\030':
		t.csiescseq = CSIEscape{}
	case '\005', '\000', '\021', '\023', 0177:
		return
	case 0x85:
		t.tnewline(true)
	case 0x88:
		t.tabs[t.c.x] = true
	case 0x9a:
		t.ttywrite([]byte(t.cfg.Vtiden), false)
	case 0x90, 0x9d, 0x9e, 0x9f:
		t.tstrsequence(byte(ascii))
		return
	}
	t.esc &^= escStrEnd | escSTR
}

func (t *Term) eschandle(ascii rune) bool {
	switch ascii {
	case '[':
		t.esc |= escCSI
		return false
	case '#':
		t.esc |= escTest
		return false
	case '%':
		t.esc |= escUTF8
		return false
	case 'P', '_', '^', ']', 'k':
		t.tstrsequence(byte(ascii))
		return false
	case 'n', 'o':
		t.charset = 2 + int(ascii-'n')
	case '(', ')', '*', '+':
		t.icharset = int(ascii) - '('
		t.esc |= escAltCharset
		return false
	case 'D':
		if t.c.y == t.bot {
			t.tscrollup(t.top, 1)
		} else {
			t.tmoveto(t.c.x, t.c.y+1)
		}
	case 'E':
		t.tnewline(true)
	case 'H':
		t.tabs[t.c.x] = true
	case 'M':
		if t.c.y == t.top {
			t.tscrolldown(t.top, 1)
		} else {
			t.tmoveto(t.c.x, t.c.y-1)
		}
	case 'Z':
		t.ttywrite([]byte(t.cfg.Vtiden), false)
	case 'c':
		t.treset()
		t.ResetTitle()
		t.xloadcols()
		t.setWinMode(false, ModeHide)
	case '=':
		t.setWinMode(true, ModeAppKeypad)
	case '>':
		t.setWinMode(false, ModeAppKeypad)
	case '7':
		t.tcursor(cursorSave)
	case '8':
		t.tcursor(cursorLoad)
	case '\\':
		if t.esc&escStrEnd != 0 {
			t.strhandle()
		}
	default:
		log.Printf("erresc: unknown sequence ESC 0x%02X '%c'\n", ascii, ascii)
	}
	return true
}

func (t *Term) tputc(u rune) {
	var c []byte
	control := t.isControl(u)
	var width, len_ int
	if u < 127 || !t.isSet(termModeUTF8) {
		c = []byte{byte(u)}
		width, len_ = 1, 1
	} else {
		c = utf8encode(u)
		len_ = len(c)
		width = runeWidth(u)
		if width == -1 {
			width = 1
		}
	}

	if t.isSet(termModePrint) {
		t.printer(c)
	}

	if t.esc&escSTR != 0 {
		if u == '\a' || u == 030 || u == 032 || u == 033 || t.isControlC1(u) {
			t.esc &^= escStart | escSTR
			t.esc |= escStrEnd
			goto checkControlCode
		}
		if t.strescseq.len+len_ >= t.strescseq.siz {
			if t.strescseq.siz > (1<<30)/2 {
				return
			}
			t.strescseq.siz *= 2
		}
		t.strescseq.buf = append(t.strescseq.buf, c...)
		t.strescseq.len += len_
		return
	}

checkControlCode:
	if control {
		if t.isSet(termModeUTF8) && t.isControlC1(u) {
			return
		}
		t.tcontrolcode(u)
		if t.esc == 0 {
			t.lastc = 0
		}
		return
	} else if t.esc&escStart != 0 {
		if t.esc&escCSI != 0 {
			t.csiescseq.buf = append(t.csiescseq.buf, byte(u))
			t.csiescseq.len++
			if between(int(u), 0x40, 0x7E) || t.csiescseq.len >= escBufSiz-1 {
				t.esc = 0
				t.csiparse()
				t.csihandle()
			}
			return
		} else if t.esc&escUTF8 != 0 {
			t.tdefutf8(u)
		} else if t.esc&escAltCharset != 0 {
			t.tdeftran(u)
		} else if t.esc&escTest != 0 {
			t.tdectest(u)
		} else {
			if !t.eschandle(u) {
				return
			}
		}
		t.esc = 0
		return
	}
	if t.selected(t.c.x, t.c.y) {
		t.selclear()
	}

	gp := &t.line[t.c.y][t.c.x]
	if t.isSet(termModeWrap) && t.c.state&cursorWrapNext != 0 {
		gp.Mode |= ATTRWrap
		t.tnewline(true)
		gp = &t.line[t.c.y][t.c.x]
	}

	if t.isSet(termModeInsert) && t.c.x+width < t.col {
		line := t.line[t.c.y]
		copy(line[t.c.x+width:], line[t.c.x:])
		line[t.c.x].Mode &^= ATTRWide
	}

	if t.c.x+width > t.col {
		if t.isSet(termModeWrap) {
			t.tnewline(true)
		} else {
			t.tmoveto(t.col-width, t.c.y)
		}
		gp = &t.line[t.c.y][t.c.x]
	}

	t.tsetchar(u, &t.c.attr, t.c.x, t.c.y)
	t.lastc = u

	if width == 2 {
		gp.Mode |= ATTRWide
		if t.c.x+1 < t.col {
			if t.line[t.c.y][t.c.x+1].Mode == ATTRWide && t.c.x+2 < t.col {
				t.line[t.c.y][t.c.x+2].U = ' '
				t.line[t.c.y][t.c.x+2].Mode &^= ATTRWdummy
			}
			t.line[t.c.y][t.c.x+1].U = '\x00'
			t.line[t.c.y][t.c.x+1].Mode = ATTRWdummy
		}
	}
	if t.c.x+width < t.col {
		t.tmoveto(t.c.x+width, t.c.y)
	} else {
		t.c.state |= cursorWrapNext
	}
}

// Twrite processes a buffer of bytes; returns count processed.
func (t *Term) Twrite(buf []byte, showCtrl bool) int {
	n := 0
	for n < len(buf) {
		var u rune
		var charsize int
		if t.isSet(termModeUTF8) {
			u, charsize = utf8decode(buf[n:], len(buf)-n)
			if charsize == 0 {
				break
			}
		} else {
			u = rune(buf[n] & 0xFF)
			charsize = 1
		}
		if showCtrl && t.isControl(u) {
			if u&0x80 != 0 {
				u &^= 0x80
				t.tputc('^')
				t.tputc('[')
			} else if u != '\n' && u != '\r' && u != '\t' {
				u ^= 0x40
				t.tputc('^')
			}
		}
		t.tputc(u)
		n += charsize
	}
	return n
}

func (t *Term) ResetTitle() { t.xsettitle("") }

// SetWriter sets the function used to send bytes to the pty.
func (t *Term) SetWriter(w func([]byte)) { t.writer = w }

func (t *Term) ttywrite(s []byte, mayEcho bool) {
	next := 0
	if mayEcho && t.isSet(termModeEcho) {
		t.Twrite(s, true)
	}
	if !t.isSet(termModeCRLF) {
		t.writeRaw(s)
		return
	}
	for len(s) > 0 {
		if s[0] == '\r' {
			t.writeRaw([]byte("\r\n"))
			next = 1
		} else {
			nx := len(s)
			for i, b := range s {
				if b == '\r' {
					nx = i
					break
				}
			}
			t.writeRaw(s[:nx])
			next = nx
		}
		s = s[next:]
	}
}

func (t *Term) writeRaw(s []byte) {
	if t.writer != nil {
		t.writer(s)
	}
}

// WriteToTTY sends user input to the pty.
func (t *Term) WriteToTTY(s []byte, mayEcho bool) { t.ttywrite(s, mayEcho) }
