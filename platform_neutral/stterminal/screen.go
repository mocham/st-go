package stterminal

import (
	"unicode"

	"st-go/limits"
)

// CellWidth exposes the terminal's width approximation to text-only clients
// that must wrap ordinary selectable terminal cells without crossing a pane.
func CellWidth(r rune) int { return runeWidthTbl(r) }

// runeWidthTbl: wcwidth approximation.
// wide == East Asian Wide/Fullwidth + common wide symbols.
func runeWidthTbl(u rune) int {
	if unicode.Is(unicode.Mn, u) || unicode.Is(unicode.Me, u) || unicode.Is(unicode.Mc, u) {
		return 0
	}
	switch {
	case u >= 0x1100 && u <= 0x115F: // Hangul Jamo
	case u >= 0x2E80 && u <= 0xA4CF: // CJK Radicals .. Yi
	case u >= 0xAC00 && u <= 0xD7A3: // Hangul Syllables
	case u >= 0xF900 && u <= 0xFAFF: // CJK Compatibility Ideographs
	case u >= 0xFE30 && u <= 0xFE4F: // CJK Compatibility Forms
	case u >= 0xFF00 && u <= 0xFF60: // Fullwidth Forms
	case u >= 0xFFE0 && u <= 0xFFE6:
	case u >= 0x1F300 && u <= 0x1FAFF: // Emoji / symbols
	case u >= 0x20000 && u <= 0x3FFFD: // CJK Ext B..G
	default:
		return 1
	}
	return 2
}

// tlinelen: visual line length
func (t *Term) tlinelen(y int) int {
	i := t.col
	if t.line[y][i-1].Mode&ATTRWrap != 0 {
		return i
	}
	for i > 0 && t.line[y][i-1].U == ' ' {
		i--
	}
	return i
}

// SelInit
func (t *Term) SelInit() {
	t.sel.mode = selIdle
	t.sel.snap = 0
	t.sel.ob.x = -1
}

func (t *Term) selstart(col, row, snap int) {
	t.selclear()
	t.sel.mode = selEmpty
	t.sel.typ = selRegular
	t.sel.alt = t.isSet(termModeAltScreen)
	t.sel.snap = snap
	t.sel.oe.x, t.sel.ob.x = col, col
	t.sel.oe.y, t.sel.ob.y = row, row
	t.selnormalize()

	if t.sel.snap != 0 {
		t.sel.mode = selReady
	}
	t.tsetdirt(t.sel.nb.y, t.sel.ne.y)
}

func (t *Term) selextend(col, row, typ, done int) {
	if t.sel.mode == selIdle {
		return
	}
	if done != 0 && t.sel.mode == selEmpty {
		t.selclear()
		return
	}
	oldey, oldex := t.sel.oe.y, t.sel.oe.x
	oldsby, oldsey := t.sel.nb.y, t.sel.ne.y
	oldtype := t.sel.typ

	t.sel.oe.x, t.sel.oe.y = col, row
	t.selnormalize()
	t.sel.typ = typ

	if oldey != t.sel.oe.y || oldex != t.sel.oe.x || oldtype != t.sel.typ || t.sel.mode == selEmpty {
		t.tsetdirt(min(t.sel.nb.y, oldsby), max(t.sel.ne.y, oldsey))
	}
	if done != 0 {
		t.sel.mode = selIdle
	} else {
		t.sel.mode = selReady
	}
}

func (t *Term) selnormalize() {
	if t.sel.typ == selRegular && t.sel.ob.y != t.sel.oe.y {
		if t.sel.ob.y < t.sel.oe.y {
			t.sel.nb.x, t.sel.ne.x = t.sel.ob.x, t.sel.oe.x
		} else {
			t.sel.nb.x, t.sel.ne.x = t.sel.oe.x, t.sel.ob.x
		}
	} else {
		t.sel.nb.x, t.sel.ne.x = min(t.sel.ob.x, t.sel.oe.x), max(t.sel.ob.x, t.sel.oe.x)
	}
	t.sel.nb.y, t.sel.ne.y = min(t.sel.ob.y, t.sel.oe.y), max(t.sel.ob.y, t.sel.oe.y)

	t.selsnap(&t.sel.nb.x, &t.sel.nb.y, -1)
	t.selsnap(&t.sel.ne.x, &t.sel.ne.y, +1)

	if t.sel.typ == selRectangular {
		return
	}
	i := t.tlinelen(t.sel.nb.y)
	if i < t.sel.nb.x {
		t.sel.nb.x = i
	}
	if t.tlinelen(t.sel.ne.y) <= t.sel.ne.x {
		t.sel.ne.x = t.col - 1
	}
}

func (t *Term) selected(x, y int) bool {
	if t.sel.mode == selEmpty || t.sel.ob.x == -1 ||
		t.sel.alt != t.isSet(termModeAltScreen) {
		return false
	}
	if t.sel.typ == selRectangular {
		return between(y, t.sel.nb.y, t.sel.ne.y) &&
			between(x, t.sel.nb.x, t.sel.ne.x)
	}
	return between(y, t.sel.nb.y, t.sel.ne.y) &&
		(y != t.sel.nb.y || x >= t.sel.nb.x) &&
		(y != t.sel.ne.y || x <= t.sel.ne.x)
}

func (t *Term) selsnap(x, y *int, direction int) {
	switch t.sel.snap {
	case snapWord:
		var newx, newy, xt, yt int
		prevgp := &t.line[*y][*x]
		prevdelim := t.isDelim(prevgp.U)
		for {
			newx = *x + direction
			newy = *y
			if !between(newx, 0, t.col-1) {
				newy += direction
				newx = (newx + t.col) % t.col
				if !between(newy, 0, t.row-1) {
					break
				}
				if direction > 0 {
					yt, xt = *y, *x
				} else {
					yt, xt = newy, newx
				}
				if t.line[yt][xt].Mode&ATTRWrap == 0 {
					break
				}
			}
			if newx >= t.tlinelen(newy) {
				break
			}
			gp := &t.line[newy][newx]
			delim := t.isDelim(gp.U)
			if gp.Mode&ATTRWdummy == 0 && (delim != prevdelim ||
				(delim && gp.U != prevgp.U)) {
				break
			}
			*x, *y = newx, newy
			prevgp = gp
			prevdelim = delim
		}
	case snapLine:
		if direction < 0 {
			*x = 0
			for ; *y > 0; *y += direction {
				if t.line[*y-1][t.col-1].Mode&ATTRWrap == 0 {
					break
				}
			}
		} else if direction > 0 {
			*x = t.col - 1
			for ; *y < t.row-1; *y += direction {
				if t.line[*y][t.col-1].Mode&ATTRWrap == 0 {
					break
				}
			}
		}
	}
}

// GetSel returns selected text as UTF-8 string.
func (t *Term) GetSel() string {
	if t.sel.ob.x == -1 {
		return ""
	}
	var sb []byte
	for y := t.sel.nb.y; y <= t.sel.ne.y; y++ {
		linelen := t.tlinelen(y)
		if linelen == 0 {
			sb = append(sb, '\n')
			continue
		}
		gp, lastx := 0, 0
		if t.sel.typ == selRectangular {
			gp = t.sel.nb.x
			lastx = t.sel.ne.x
		} else {
			if t.sel.nb.y == y {
				gp = t.sel.nb.x
			}
			if t.sel.ne.y == y {
				lastx = t.sel.ne.x
			} else {
				lastx = t.col - 1
			}
		}
		last := min(lastx, linelen-1)
		for last >= gp && t.line[y][last].U == ' ' {
			last--
		}
		for i := gp; i <= last; i++ {
			if t.line[y][i].Mode&ATTRWdummy != 0 {
				continue
			}
			sb = append(sb, t.line[y][i].Text()...)
		}
		if (y < t.sel.ne.y || lastx >= linelen) &&
			(t.sel.typ == selRectangular || last < 0 || t.line[y][last].Mode&ATTRWrap == 0) {
			sb = append(sb, '\n')
		}
	}
	return string(sb)
}

func (t *Term) selclear() {
	if t.sel.ob.x == -1 {
		return
	}
	t.sel.mode = selIdle
	t.sel.ob.x = -1
	t.tsetdirt(t.sel.nb.y, t.sel.ne.y)
}

func (t *Term) selscroll(orig, n int) {
	if t.sel.ob.x == -1 || t.sel.alt != t.isSet(termModeAltScreen) {
		return
	}
	if between(t.sel.nb.y, orig, t.bot) != between(t.sel.ne.y, orig, t.bot) {
		t.selclear()
	} else if between(t.sel.nb.y, orig, t.bot) {
		t.sel.ob.y += n
		t.sel.oe.y += n
		if t.sel.ob.y < t.top || t.sel.ob.y > t.bot ||
			t.sel.oe.y < t.top || t.sel.oe.y > t.bot {
			t.selclear()
		} else {
			t.selnormalize()
		}
	}
}

// Tattrset reports whether any visible cell has the given attribute.
func (t *Term) Tattrset(attr uint16) bool {
	for i := 0; i < t.row; i++ {
		for j := 0; j < t.col; j++ {
			if t.line[i][j].Mode&attr != 0 {
				return true
			}
		}
	}
	return false
}

func (t *Term) tsetdirt(top, bot int) {
	top = clamp(top, 0, t.row-1)
	bot = clamp(bot, 0, t.row-1)
	t.markDirty(0, top, t.col-1, bot)
}

func (t *Term) tsetdirtattr(attr uint16) {
	for i := 0; i < t.row; i++ {
		for j := 0; j < t.col; j++ {
			if t.line[i][j].Mode&attr != 0 {
				t.tsetdirt(i, i)
				break
			}
		}
	}
}

func (t *Term) tfulldirt() { t.tsetdirt(0, t.row-1) }

func (t *Term) tcursor(mode int) {
	alt := t.isSet(termModeAltScreen)
	if mode == cursorSave {
		if alt {
			t.saveCursorAlt = t.c
		} else {
			t.saveCursor = t.c
		}
	} else if mode == cursorLoad {
		if alt {
			t.c = t.saveCursorAlt
		} else {
			t.c = t.saveCursor
		}
		t.tmoveto(t.c.x, t.c.y)
	}
}

func (t *Term) treset() {
	t.htmlTransfer = nil
	if t.hooks != nil {
		t.hooks.ImageClearAll()
	}
	t.c = TCursor{
		attr:  Glyph{Mode: ATTRNull, Fg: uint32(t.cfg.DefaultFg), Bg: uint32(t.cfg.DefaultBg)},
		state: cursorDefault,
	}
	for i := range t.tabs {
		t.tabs[i] = false
	}
	for i, step := t.tabspaces(), t.tabspaces(); i < t.col; i += step {
		t.tabs[i] = true
	}
	t.top = 0
	t.bot = t.row - 1
	t.mode = termModeWrap | termModeUTF8
	for i := range t.trantbl {
		t.trantbl[i] = csUSA
	}
	t.charset = 0

	for i := 0; i < 2; i++ {
		t.tmoveto(0, 0)
		t.tcursor(cursorSave)
		t.tclearregion(0, 0, t.col-1, t.row-1)
		t.tswapscreen()
	}
}

// New initializes the screen. Called once at startup.
func (t *Term) New(col, row int) {
	if !limits.Grid(col, row) {
		col, row = 80, 24
	}
	t.tresize(col, row)
	t.treset()
}

func (t *Term) tswapscreen() {
	t.htmlTransfer = nil
	t.line, t.alt = t.alt, t.line
	t.mode ^= termModeAltScreen
	if hooks, ok := t.hooks.(AlternateScreenHooks); ok {
		hooks.AlternateScreen(t.isSet(termModeAltScreen))
	}
	t.tfulldirt()
}

func (t *Term) tscrolldown(orig, n int) {
	n = clamp(n, 0, t.bot-orig+1)
	t.tsetdirt(orig, t.bot-n)
	t.tclearregion(0, t.bot-n+1, t.col-1, t.bot)
	for i := t.bot; i >= orig+n; i-- {
		t.line[i], t.line[i-n] = t.line[i-n], t.line[i]
	}
	t.selscroll(orig, n)
}

func (t *Term) tscrollup(orig, n int) {
	n = clamp(n, 0, t.bot-orig+1)
	t.tclearregion(0, orig, t.col-1, orig+n-1)
	t.tsetdirt(orig+n, t.bot)
	for i := orig; i <= t.bot-n; i++ {
		t.line[i], t.line[i+n] = t.line[i+n], t.line[i]
	}
	t.selscroll(orig, -n)
}

func (t *Term) tnewline(firstCol bool) {
	y := t.c.y
	if y == t.bot {
		t.tscrollup(t.top, 1)
	} else {
		y++
	}
	if firstCol {
		t.tmoveto(0, y)
	} else {
		t.tmoveto(t.c.x, y)
	}
}

func (t *Term) tmoveato(x, y int) {
	if t.c.state&cursorOrigin != 0 {
		t.tmoveto(x, y+t.top)
	} else {
		t.tmoveto(x, y)
	}
}

func (t *Term) tmoveto(x, y int) {
	miny, maxy := 0, t.row-1
	if t.c.state&cursorOrigin != 0 {
		miny, maxy = t.top, t.bot
	}
	t.c.state &^= cursorWrapNext
	t.c.x = clamp(x, 0, t.col-1)
	t.c.y = clamp(y, miny, maxy)
	t.markCursorDirty()
}

var vt100_0 [62]rune

func init() {
	// indices 0..6  (A-G)
	table := []rune("↑↓→←█▚☃")
	copy(vt100_0[0:7], table)
	// index 0x1e (0x41+0x1e=0x5f '_') is a space
	vt100_0[0x1e] = ' '
	// indices 0x1f..0x3c for '`' through '~'
	table2 := []rune("◆▒␉␌␍␊°±␤␋┘┐┌└┼⎺⎻─⎼⎽├┤┴┬│≤≥π≠£·")
	copy(vt100_0[0x1f:0x1f+len(table2)], table2)
}

func (t *Term) tsetchar(u rune, attr *Glyph, x, y int) {
	t.cancelAnimCell(x, y)

	if t.trantbl[t.charset] == csGraphic0 && between(int(u), 0x41, 0x7e) {
		r := vt100_0[u-0x41]
		if r != 0 {
			u = r
		}
	}

	first, last := x, x
	if t.line[y][x].Mode&ATTRWide != 0 {
		if x+1 < t.col {
			t.line[y][x+1].U = ' '
			t.line[y][x+1].Suffix = ""
			t.line[y][x+1].Mode &^= ATTRWdummy
			last++
		}
	} else if t.line[y][x].Mode&ATTRWdummy != 0 && x > 0 {
		t.line[y][x-1].U = ' '
		t.line[y][x-1].Suffix = ""
		t.line[y][x-1].Mode &^= ATTRWide
		first--
	}

	t.markDirty(first, y, last, y)
	t.line[y][x] = *attr
	t.line[y][x].U = u
	t.line[y][x].Suffix = ""
}

func (t *Term) tclearregion(x1, y1, x2, y2 int) {
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

	t.cancelAnimRegion(x1, y1, x2, y2)

	for y := y1; y <= y2; y++ {
		for x := x1; x <= x2; x++ {
			if t.selected(x, y) {
				t.selclear()
			}
			gp := &t.line[y][x]
			gp.Fg = t.c.attr.Fg
			gp.Bg = t.c.attr.Bg
			gp.Mode = 0
			gp.U = ' '
		}
	}
	t.markDirty(x1, y1, x2, y2)
}

func (t *Term) tdeletechar(n int) {
	n = clamp(n, 0, t.col-t.c.x)
	dst, src := t.c.x, t.c.x+n
	size := t.col - src
	line := t.line[t.c.y]
	copy(line[dst:dst+size], line[src:src+size])
	t.tclearregion(t.col-n, t.c.y, t.col-1, t.c.y)
}

func (t *Term) tinsertblank(n int) {
	n = clamp(n, 0, t.col-t.c.x)
	dst, src := t.c.x+n, t.c.x
	size := t.col - dst
	line := t.line[t.c.y]
	copy(line[dst:dst+size], line[src:src+size])
	t.tclearregion(src, t.c.y, dst-1, t.c.y)
}

func (t *Term) tinsertblankline(n int) {
	if between(t.c.y, t.top, t.bot) {
		t.tscrolldown(t.c.y, n)
	}
}

func (t *Term) tdeleteline(n int) {
	if between(t.c.y, t.top, t.bot) {
		t.tscrollup(t.c.y, n)
	}
}

func (t *Term) tsetscroll(t0, b int) {
	t0 = clamp(t0, 0, t.row-1)
	b = clamp(b, 0, t.row-1)
	if t0 > b {
		t0, b = b, t0
	}
	t.top, t.bot = t0, b
}

func (t *Term) tputtab(n int) {
	x := t.c.x
	if n > 0 {
		for x < t.col && n != 0 {
			for x++; x < t.col && !t.tabs[x]; x++ {
			}
			n--
		}
	} else if n < 0 {
		for x > 0 && n != 0 {
			for x--; x > 0 && !t.tabs[x]; x-- {
			}
			n++
		}
	}
	t.c.x = clamp(x, 0, t.col-1)
	t.markCursorDirty()
}

func (t *Term) tdefutf8(ascii rune) {
	if ascii == 'G' {
		t.mode |= termModeUTF8
	} else if ascii == '@' {
		t.mode &^= termModeUTF8
	}
}

func (t *Term) tdeftran(ascii rune) {
	switch ascii {
	case '0':
		t.trantbl[t.icharset] = csGraphic0
	case 'B':
		t.trantbl[t.icharset] = csUSA
	default:
		logf("esc unhandled charset: ESC ( %c\n", ascii)
	}
}

func (t *Term) tdectest(c rune) {
	if c == '8' {
		for x := 0; x < t.col; x++ {
			for y := 0; y < t.row; y++ {
				t.tsetchar('E', &t.c.attr, x, y)
			}
		}
	}
}

func (t *Term) tstrsequence(c byte) {
	switch c {
	case 0x90:
		c = 'P'
	case 0x9f:
		c = '_'
	case 0x9e:
		c = '^'
	case 0x9d:
		c = ']'
	}
	t.strreset()
	t.strescseq.typ = c
	t.esc |= escSTR
}

func (t *Term) dumpLine(n int) {
	if t.iofd < 0 {
		return
	}
	var sb []byte
	end := min(t.tlinelen(n), t.col) - 1
	if t.tlinelen(n) > 0 && t.line[n][0].U != ' ' || end > 0 {
		for i := 0; i <= end; i++ {
			sb = append(sb, t.line[n][i].Text()...)
		}
	}
	sb = append(sb, '\n')
	t.printer(sb)
}

func (t *Term) dump() {
	for i := 0; i < t.row; i++ {
		t.dumpLine(i)
	}
}

func (t *Term) printer(s []byte) {
	if t.printerFn != nil {
		t.printerFn(s)
	}
}

func (t *Term) csiparse() {
	buf := t.csiescseq.buf
	p := 0
	t.csiescseq.narg = 0
	if len(buf) > 0 && buf[0] == '?' {
		t.csiescseq.priv = true
		p++
	}
	for p < len(buf) && t.csiescseq.narg < escArgSiz {
		v := 0
		start := p
		for p < len(buf) && buf[p] >= '0' && buf[p] <= '9' {
			v = v*10 + int(buf[p]-'0')
			p++
		}
		if p == start {
			v = 0
		}
		t.csiescseq.arg[t.csiescseq.narg] = v
		t.csiescseq.narg++
		if p >= len(buf) || buf[p] != ';' {
			break
		}
		p++
	}
	t.csiescseq.mode[0] = 0
	t.csiescseq.mode[1] = 0
	if p < len(buf) {
		t.csiescseq.mode[0] = buf[p]
		p++
	}
	if p < len(buf) {
		t.csiescseq.mode[1] = buf[p]
	}
}

// Draw is the compatibility wrapper returning the bounding box of DrawRegions.
// Painting still visits only dirty rows. Frontends should use DrawRegions to
// avoid uploading the unchanged pixels between separate dirty areas.
func (t *Term) Draw() Region {
	r := emptyRegion()
	for _, q := range t.DrawRegions() {
		if r.Empty() {
			r = q
		} else {
			r = regionUnion(r, q)
		}
	}
	return r
}

// DrawRegions paints dirty row spans in the same serialized context as terminal
// mutation. Legacy hooks include old/new cursor cell damage; overlay-aware
// frontends present caret changes separately without rerasterizing glyphs.
func (t *Term) DrawRegions() []Region {
	regions := t.TakeRegions()
	if !t.xstartdraw() {
		return nil
	}
	cx := t.c.x
	t.ocx = clamp(t.ocx, 0, t.col-1)
	t.ocy = clamp(t.ocy, 0, t.row-1)
	if t.ocx > 0 && t.line[t.ocy][t.ocx].Mode&ATTRWdummy != 0 {
		t.ocx--
	}
	if cx > 0 && t.line[t.c.y][cx].Mode&ATTRWdummy != 0 {
		cx--
	}
	painted := make([]Region, 0, len(regions)+2)
	for _, r := range regions {
		for y := r.Y1; y <= r.Y2; y++ {
			first, last := r.X1, r.X2
			// Partial invalidations can start/end inside a wide glyph.
			if first > 0 && t.line[y][first].Mode&ATTRWdummy != 0 {
				first--
			}
			if last+1 < t.col && t.line[y][last].Mode&ATTRWide != 0 {
				last++
			}
			t.xdrawline(t.line[y], first, y, last+1)
			q := Region{first, y, last, y}
			if n := len(painted); n > 0 && painted[n-1].Y2+1 == y && painted[n-1].X1 == first && painted[n-1].X2 == last {
				painted[n-1].Y2 = y
			} else {
				painted = append(painted, q)
			}
		}
	}
	t.xdrawcursor(cx, t.c.y, t.line[t.c.y][cx],
		t.ocx, t.ocy, t.line[t.ocy][t.ocx])
	t.cursorDirty = false
	if overlay, ok := t.hooks.(CursorOverlayHooks); ok && overlay.CursorIsOverlay() {
		t.ocx, t.ocy = cx, t.c.y
		return painted
	}
	// Cursor painting restores its old cell and overlays its new cell. Keep
	// these separate too; a cursor jump must not upload all intervening rows.
	for _, cell := range [][2]int{{t.ocx, t.ocy}, {cx, t.c.y}} {
		x, y := cell[0], cell[1]
		last := x
		if x+1 < t.col && t.line[y][x].Mode&ATTRWide != 0 {
			last++
		}
		covered := false
		for _, r := range painted {
			if r.Y1 <= y && y <= r.Y2 && r.X1 <= x && last <= r.X2 {
				covered = true
				break
			}
		}
		if !covered {
			painted = append(painted, Region{x, y, last, y})
		}
	}
	t.ocx = cx
	t.ocy = t.c.y
	return painted
}

// Redraw forces a full repaint of the whole screen.
func (t *Term) Redraw() {
	t.tfulldirt()
	t.requestPaint()
}

// RedrawAttr repaints rows containing attr without forcing a full redraw.
func (t *Term) RedrawAttr(attr uint16) {
	t.tsetdirtattr(attr)
	t.requestPaint()
}

// ClearSel public wrapper.
func (t *Term) SelClear()                         { t.selclear() }
func (t *Term) SelStart(col, row, snap int)       { t.selstart(col, row, snap) }
func (t *Term) SelExtend(col, row, typ, done int) { t.selextend(col, row, typ, done) }
func (t *Term) Selected(x, y int) bool            { return t.selected(x, y) }

// WinMode exposes window mode flags.
func (t *Term) WinMode() uint { return t.winMode }

// Rows returns the number of screen rows (test/debug helper).
func (t *Term) Rows() int { return t.row }

// CursorX returns the cursor column (test helper).
func (t *Term) CursorX() int { return t.c.x }

// CursorY returns the cursor row (test helper).
func (t *Term) CursorY() int { return t.c.y }
