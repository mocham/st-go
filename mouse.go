package main

import (
	"time"

	"github.com/BurntSushi/xgb/xproto"

	"st-go/term"
)

// mouse state for selection
type mouseSel struct {
	active     bool
	oldx, oldy int
	selx, sely int
	tclick1    time.Time
	tclick2    time.Time
}

var msel mouseSel

// buttons holds currently-pressed mouse buttons (bitmask).
var buttons uint

func buttonmask(btn byte) uint {
	if btn < 1 || btn > 32 {
		return 0
	}
	return 1 << (btn - 1)
}

// evcol/evrow mirror st's evcol/evrow: clamp the pointer position to the
// window area before dividing by cell size, so clicks near (or past) the
// window edges never yield out-of-bounds cell coordinates (st: LIMIT(x,0,tw-1)).
func (t *Terminal) evcol(px int) int {
	x := px - t.borderpx
	x = clampInt(x, 0, t.cols*t.cw-1)
	return x / t.cw
}

func (t *Terminal) evrow(py int) int {
	y := py - t.borderpx
	y = clampInt(y, 0, t.rows*t.ch-1)
	return y / t.ch
}

func (t *Terminal) bpress(e xproto.ButtonPressEvent) {
	if t.restoreGeometryOnPress(e) {
		return
	}
	btn := byte(e.Detail)
	x := t.evcol(int(e.EventX))
	y := t.evrow(int(e.EventY))

	if 1 <= btn && btn <= 11 {
		buttons |= buttonmask(btn)
	}

	if t.termCore.WinMode()&term.ModeMouse != 0 && e.State&t.forceMouseMod == 0 {
		t.mousereport(btn, e.State, x, y, evPress)
		return
	}

	if t.mouseaction(btn, e.State, false) {
		return
	}

	if btn == 1 {
		now := time.Now()
		snap := 0
		if now.Sub(msel.tclick2) < time.Duration(t.tripleClick)*time.Millisecond {
			snap = 2 // SNAP_LINE
		} else if now.Sub(msel.tclick1) < time.Duration(t.doubleClick)*time.Millisecond {
			snap = 1 // SNAP_WORD
		}
		msel.tclick2 = msel.tclick1
		msel.tclick1 = now
		msel.active = true
		msel.selx, msel.sely = x, y
		t.termCore.SelStart(x, y, snap)
	}
}

func (t *Terminal) bmotion(e xproto.MotionNotifyEvent) {
	if t.suppressRestoreButton {
		return
	}
	x := t.evcol(int(e.EventX))
	y := t.evrow(int(e.EventY))

	if t.termCore.WinMode()&term.ModeMouse != 0 && e.State&t.forceMouseMod == 0 {
		t.mousereport(0, e.State, x, y, evMotion)
		return
	}

	if !msel.active {
		return
	}
	if x != msel.oldx || y != msel.oldy {
		msel.oldx, msel.oldy = x, y
		seltype := t.selTypeForState(e.State)
		t.termCore.SelExtend(x, y, seltype, 0)
		t.termCore.Redraw()
	}
}

func (t *Terminal) brelease(e xproto.ButtonReleaseEvent) {
	btn := byte(e.Detail)
	if btn == 1 && t.suppressRestoreButton {
		t.suppressRestoreButton = false
		return
	}
	x := t.evcol(int(e.EventX))
	y := t.evrow(int(e.EventY))

	if 1 <= btn && btn <= 11 {
		buttons &^= buttonmask(btn)
	}

	if t.termCore.WinMode()&term.ModeMouse != 0 && e.State&t.forceMouseMod == 0 {
		t.mousereport(btn, e.State, x, y, evRelease)
		return
	}

	if t.mouseaction(btn, e.State, true) {
		return
	}

	if btn == 1 && msel.active {
		msel.active = false
		seltype := t.selTypeForState(e.State)
		t.termCore.SelExtend(x, y, seltype, 1)
		sel := t.termCore.GetSel()
		if sel != "" {
			t.setSelection(sel)
		}
		t.termCore.Redraw()
	}
}

// mouseaction checks the configured mshortcuts.
func (t *Terminal) mouseaction(button byte, state uint16, release bool) bool {
	// ignore Button<N>mask for Button<N> - it's set on release
	state &^= uint16(buttonmask(button))
	// ignore numlock/layout modifiers (st's match does this internally)
	state &^= uint16(t.ignoreMod)

	for _, ms := range t.mshortcuts {
		if ms.release == release && ms.button == button &&
			(match(int(ms.mask), int(state)) ||
				match(int(ms.mask), int(state)&^int(t.forceMouseMod))) {
			t.msAction(ms.action, ms.arg)
			return true
		}
	}
	return false
}

func (t *Terminal) msAction(action, arg string) {
	switch action {
	case "selpaste":
		sel := t.termCore.GetSel()
		if sel != "" {
			t.pasteToTTY(sel)
		}
	case "ttysend":
		if arg != "" {
			t.termCore.WriteToTTY([]byte(arg), false)
		}
	}
}

// selTypeForState: rectangular selection with the configured modifier.
func (t *Terminal) selTypeForState(state uint16) int {
	if t.forceMouseMod != 0 && state&t.forceMouseMod != 0 {
		return term.SelRectangular
	}
	return term.SelRegular
}

// mouse event kinds
const (
	evPress   = 1
	evRelease = 2
	evMotion  = 3
)

// mousereport ports x.c mousereport.
// etype: evPress/evRelease/evMotion
func (t *Terminal) mousereport(btn byte, state uint16, x, y int, etype byte) {
	var code int

	if etype == evMotion {
		if x == msel.oldx && y == msel.oldy {
			return
		}
		if t.termCore.WinMode()&(term.ModeMouseMotion|term.ModeMouseMany) == 0 {
			return
		}
		// MODE_MOUSEMOTION: no reporting if no button is pressed
		if t.termCore.WinMode()&term.ModeMouseMotion != 0 && buttons == 0 {
			return
		}
		// btn = lowest-numbered pressed button, or 12 if none
		b := 1
		for ; b <= 11 && buttons&buttonmask(byte(b)) == 0; b++ {
		}
		btn = byte(b)
		code = 32
	} else {
		if btn < 1 || btn > 11 {
			return
		}
		if etype == evRelease {
			if t.termCore.WinMode()&term.ModeMouseX10 != 0 {
				return
			}
			// no release events for the scroll wheel
			if btn == 4 || btn == 5 {
				return
			}
		}
		code = 0
	}

	msel.oldx, msel.oldy = x, y

	// encode btn into code
	if (t.termCore.WinMode()&term.ModeMouseSgr == 0 && etype == evRelease) ||
		btn == 12 {
		code += 3
	} else if btn >= 8 {
		code += 128 + int(btn) - 8
	} else if btn >= 4 {
		code += 64 + int(btn) - 4
	} else {
		code += int(btn) - 1
	}

	if t.termCore.WinMode()&term.ModeMouseX10 == 0 {
		if state&1 != 0 {
			code += 4 // shift
		}
		if state&(1<<3) != 0 {
			code += 8 // alt/meta
		}
		if state&4 != 0 {
			code += 16 // ctrl
		}
	}

	var s string
	if t.termCore.WinMode()&term.ModeMouseSgr != 0 {
		c := byte('M')
		if etype == evRelease {
			c = 'm'
		}
		s = "\x1b[<" + itoa(code) + ";" + itoa(x+1) + ";" + itoa(y+1) + string(c)
	} else if x < 223 && y < 223 {
		s = "\x1b[M" + string(rune(32+code)) + string(rune(32+x+1)) + string(rune(32+y+1))
	} else {
		return
	}
	t.termCore.WriteToTTY([]byte(s), false)
}
