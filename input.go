package main

import (
	"github.com/BurntSushi/xgb/xproto"

	"st-go/config"
	"st-go/term"
)

// loadInputConfig builds the keys + shortcuts tables from config.
func (t *Terminal) loadInputConfig(cfg *config.Config) {
	t.keys = defaultKeymap()
	for _, k := range cfg.Keymap {
		ks, ok := keysymByName[k.Keysym]
		if !ok {
			continue
		}
		mask := k.Mask
		if mask == 0 {
			mask = XKAnyMod
		}
		t.keys = append(t.keys, keyDef{
			keysym: ks, mask: mask, str: k.Str, appkey: k.Appkey, appcur: k.Appcur,
		})
	}
	for _, s := range cfg.Shortcuts {
		ks, ok := keysymByName[s.Keysym]
		if !ok {
			continue
		}
		t.shortcuts = append(t.shortcuts, shortcut{
			keysym: ks, mask: int(s.Mask), action: s.Action, arg: s.Arg,
		})
	}
	for _, ms := range cfg.Mshortcuts {
		t.mshortcuts = append(t.mshortcuts, mShortcut{
			mask: int(ms.Mask), button: byte(ms.Button),
			action: ms.Action, arg: ms.Arg, release: ms.Release,
		})
	}
}

// Key mapping entry (from config.h key[])
type keyDef struct {
	keysym  uint
	mask    int
	str     string
	appkey  int // >0 app keypad on, <0 off, 0 no value
	appcur  int // >0 app cursor on, <0 off, 0 no value
}

// defaultKeymap mirrors config.h's key[] array.
func defaultKeymap() []keyDef {
	return []keyDef{
		{XKKPHome, ShiftMask, "\x1b[2J", 0, -1},
		{XKKPHome, ShiftMask, "\x1b[1;2H", 0, +1},
		{XKKPHome, XKAnyMod, "\x1b[H", 0, -1},
		{XKKPHome, XKAnyMod, "\x1b[1~", 0, +1},
		{XKKPUp, XKAnyMod, "\x1bOx", +1, 0},
		{XKKPUp, XKAnyMod, "\x1b[A", 0, -1},
		{XKKPUp, XKAnyMod, "\x1bOA", 0, +1},
		{XKKPDown, XKAnyMod, "\x1bOr", +1, 0},
		{XKKPDown, XKAnyMod, "\x1b[B", 0, -1},
		{XKKPDown, XKAnyMod, "\x1bOB", 0, +1},
		{XKKPLeft, XKAnyMod, "\x1bOt", +1, 0},
		{XKKPLeft, XKAnyMod, "\x1b[D", 0, -1},
		{XKKPLeft, XKAnyMod, "\x1bOD", 0, +1},
		{XKKPRight, XKAnyMod, "\x1bOv", +1, 0},
		{XKKPRight, XKAnyMod, "\x1b[C", 0, -1},
		{XKKPRight, XKAnyMod, "\x1bOC", 0, +1},
		{XKKPPrior, ShiftMask, "\x1b[5;2~", 0, 0},
		{XKKPPrior, XKAnyMod, "\x1b[5~", 0, 0},
		{XKKPBegin, XKAnyMod, "\x1b[E", 0, 0},
		{XKKPEnd, ControlMask, "\x1b[J", -1, 0},
		{XKKPEnd, ControlMask, "\x1b[1;5F", +1, 0},
		{XKKPEnd, ShiftMask, "\x1b[K", -1, 0},
		{XKKPEnd, ShiftMask, "\x1b[1;2F", +1, 0},
		{XKKPEnd, XKAnyMod, "\x1b[4~", 0, 0},
		{XKKPNext, ShiftMask, "\x1b[6;2~", 0, 0},
		{XKKPNext, XKAnyMod, "\x1b[6~", 0, 0},
		{XKKPInsert, ShiftMask, "\x1b[2;2~", +1, 0},
		{XKKPInsert, ShiftMask, "\x1b[4l", -1, 0},
		{XKKPInsert, ControlMask, "\x1b[L", -1, 0},
		{XKKPInsert, ControlMask, "\x1b[2;5~", +1, 0},
		{XKKPInsert, XKAnyMod, "\x1b[4h", -1, 0},
		{XKKPInsert, XKAnyMod, "\x1b[2~", +1, 0},
		{XKKPDelete, ControlMask, "\x1b[M", -1, 0},
		{XKKPDelete, ControlMask, "\x1b[3;5~", +1, 0},
		{XKKPDelete, ShiftMask, "\x1b[2K", -1, 0},
		{XKKPDelete, ShiftMask, "\x1b[3;2~", +1, 0},
		{XKKPDelete, XKAnyMod, "\x1b[P", -1, 0},
		{XKKPDelete, XKAnyMod, "\x1b[3~", +1, 0},
		{XKKPMultiply, XKAnyMod, "\x1bOj", +2, 0},
		{XKKPAdd, XKAnyMod, "\x1bOk", +2, 0},
		{XKKPEnter, XKAnyMod, "\x1bOM", +2, 0},
		{XKKPEnter, XKAnyMod, "\r", -1, 0},
		{XKKPSubtract, XKAnyMod, "\x1bOm", +2, 0},
		{XKKPDecimal, XKAnyMod, "\x1bOn", +2, 0},
		{XKKPDivide, XKAnyMod, "\x1bOo", +2, 0},
		{XKKP0, XKAnyMod, "\x1bOp", +2, 0},
		{XKKP1, XKAnyMod, "\x1bOq", +2, 0},
		{XKKP2, XKAnyMod, "\x1bOr", +2, 0},
		{XKKP3, XKAnyMod, "\x1bOs", +2, 0},
		{XKKP4, XKAnyMod, "\x1bOt", +2, 0},
		{XKKP5, XKAnyMod, "\x1bOu", +2, 0},
		{XKKP6, XKAnyMod, "\x1bOv", +2, 0},
		{XKKP7, XKAnyMod, "\x1bOw", +2, 0},
		{XKKP8, XKAnyMod, "\x1bOx", +2, 0},
		{XKKP9, XKAnyMod, "\x1bOy", +2, 0},

		{XKUp, ShiftMask, "\x1b[1;2A", 0, 0},
		{XKUp, Mod1Mask, "\x1b[1;3A", 0, 0},
		{XKUp, ShiftMask | Mod1Mask, "\x1b[1;4A", 0, 0},
		{XKUp, ControlMask, "\x1b[1;5A", 0, 0},
		{XKUp, ShiftMask | ControlMask, "\x1b[1;6A", 0, 0},
		{XKUp, ControlMask | Mod1Mask, "\x1b[1;7A", 0, 0},
		{XKUp, ShiftMask | ControlMask | Mod1Mask, "\x1b[1;8A", 0, 0},
		{XKUp, XKAnyMod, "\x1b[A", 0, -1},
		{XKUp, XKAnyMod, "\x1bOA", 0, +1},

		{XKDown, ShiftMask, "\x1b[1;2B", 0, 0},
		{XKDown, Mod1Mask, "\x1b[1;3B", 0, 0},
		{XKDown, ShiftMask | Mod1Mask, "\x1b[1;4B", 0, 0},
		{XKDown, ControlMask, "\x1b[1;5B", 0, 0},
		{XKDown, ShiftMask | ControlMask, "\x1b[1;6B", 0, 0},
		{XKDown, ControlMask | Mod1Mask, "\x1b[1;7B", 0, 0},
		{XKDown, ShiftMask | ControlMask | Mod1Mask, "\x1b[1;8B", 0, 0},
		{XKDown, XKAnyMod, "\x1b[B", 0, -1},
		{XKDown, XKAnyMod, "\x1bOB", 0, +1},

		{XKLeft, ShiftMask, "\x1b[1;2D", 0, 0},
		{XKLeft, Mod1Mask, "\x1b[1;3D", 0, 0},
		{XKLeft, ShiftMask | Mod1Mask, "\x1b[1;4D", 0, 0},
		{XKLeft, ControlMask, "\x1b[1;5D", 0, 0},
		{XKLeft, ShiftMask | ControlMask, "\x1b[1;6D", 0, 0},
		{XKLeft, ControlMask | Mod1Mask, "\x1b[1;7D", 0, 0},
		{XKLeft, ShiftMask | ControlMask | Mod1Mask, "\x1b[1;8D", 0, 0},
		{XKLeft, XKAnyMod, "\x1b[D", 0, -1},
		{XKLeft, XKAnyMod, "\x1bOD", 0, +1},

		{XKRight, ShiftMask, "\x1b[1;2C", 0, 0},
		{XKRight, Mod1Mask, "\x1b[1;3C", 0, 0},
		{XKRight, ShiftMask | Mod1Mask, "\x1b[1;4C", 0, 0},
		{XKRight, ControlMask, "\x1b[1;5C", 0, 0},
		{XKRight, ShiftMask | ControlMask, "\x1b[1;6C", 0, 0},
		{XKRight, ControlMask | Mod1Mask, "\x1b[1;7C", 0, 0},
		{XKRight, ShiftMask | ControlMask | Mod1Mask, "\x1b[1;8C", 0, 0},
		{XKRight, XKAnyMod, "\x1b[C", 0, -1},
		{XKRight, XKAnyMod, "\x1bOC", 0, +1},

		{XKISOLeftTab, ShiftMask, "\x1b[Z", 0, 0},
		{XKReturn, Mod1Mask, "\x1b\r", 0, 0},
		{XKReturn, XKAnyMod, "\r", 0, 0},
		{XKInsert, ShiftMask, "\x1b[4l", -1, 0},
		{XKInsert, ShiftMask, "\x1b[2;2~", +1, 0},
		{XKInsert, ControlMask, "\x1b[L", -1, 0},
		{XKInsert, ControlMask, "\x1b[2;5~", +1, 0},
		{XKInsert, XKAnyMod, "\x1b[4h", -1, 0},
		{XKInsert, XKAnyMod, "\x1b[2~", +1, 0},
		{XKDelete, ControlMask, "\x1b[M", -1, 0},
		{XKDelete, ControlMask, "\x1b[3;5~", +1, 0},
		{XKDelete, ShiftMask, "\x1b[2K", -1, 0},
		{XKDelete, ShiftMask, "\x1b[3;2~", +1, 0},
		{XKDelete, XKAnyMod, "\x1b[P", -1, 0},
		{XKDelete, XKAnyMod, "\x1b[3~", +1, 0},
		{XKBackSpace, XKNoMod, "\x7f", 0, 0},
		{XKBackSpace, Mod1Mask, "\x1b\x7f", 0, 0},
		{XKHome, ShiftMask, "\x1b[2J", 0, -1},
		{XKHome, ShiftMask, "\x1b[1;2H", 0, +1},
		{XKHome, XKAnyMod, "\x1b[H", 0, -1},
		{XKHome, XKAnyMod, "\x1b[1~", 0, +1},
		{XKEnd, ControlMask, "\x1b[J", -1, 0},
		{XKEnd, ControlMask, "\x1b[1;5F", +1, 0},
		{XKEnd, ShiftMask, "\x1b[K", -1, 0},
		{XKEnd, ShiftMask, "\x1b[1;2F", +1, 0},
		{XKEnd, XKAnyMod, "\x1b[4~", 0, 0},
		{XKPrior, ControlMask, "\x1b[5;5~", 0, 0},
		{XKPrior, ShiftMask, "\x1b[5;2~", 0, 0},
		{XKPrior, XKAnyMod, "\x1b[5~", 0, 0},
		{XKNext, ControlMask, "\x1b[6;5~", 0, 0},
		{XKNext, ShiftMask, "\x1b[6;2~", 0, 0},
		{XKNext, XKAnyMod, "\x1b[6~", 0, 0},
	}
}

// function key table F1-F35
func addFunctionKeys() []keyDef {
	return nil
}

// fallback: composed string (XLookupString equivalent).
// When Ctrl is held, translate Latin-1 keysyms to control bytes per Xlib.
func (t *Terminal) kpress(e xproto.KeyPressEvent) {
	// resolve keysym (Shift-aware, Control-independent)
	ks := t.keysymForEvent(e.Detail, e.State)
	state := uint(e.State) &^ t.ignoreMod
	ctrl := e.State&ControlMask != 0

	// 1. shortcuts
	if t.handleShortcut(ks, state) {
		return
	}

	// 2. custom keys from config.h
	if str, matched := t.kmap(ks, state); matched {
		t.termCore.WriteToTTY([]byte(str), false)
		return
	}

	// 3. composed string
	buf, n := t.lookupString(ks, ctrl)
	if n == 0 {
		return
	}
	if n == 1 && e.State&Mod1Mask != 0 {
		if t.termCore.WinMode()&term.Mode8bit != 0 {
			if buf[0] < 0177 {
				buf[0] |= 0x80
			}
		} else {
			buf = []byte{0x1b, buf[0]}
			n = 2
		}
	}
	t.termCore.WriteToTTY(buf[:n], true)
}

// lookupString reproduces XLookupString's control-modifier translation
// for a Latin-1 keysym.
func (t *Terminal) lookupString(ks uint, ctrl bool) ([]byte, int) {
	// Only Latin-1 (0x20-0xFF) and Unicode keysyms (0x100-0x10FFFF, excluding
	// the X11 function-key range 0xFE00-0xFFFF) map to characters.
	// Modifier keys (Control_L=0xFFE3, Shift_L=0xFFE1, etc.) and function
	// keys must never be emitted as text.
	if ctrl {
		if ks >= 0x20 && ks <= 0x7E {
			// Xlib control translation table
			switch {
			case ks >= 0x40 && ks <= 0x7E:
				// @ A-Z [ \ ] ^ _ ` a-z { | } ~
				return []byte{byte(ks & 0x1f)}, 1
			case ks == 0x20, ks == 0x32: // space, 2
				return []byte{0}, 1
			case ks == 0x33: // 3
				return []byte{0x1b}, 1
			case ks == 0x34: // 4
				return []byte{0x1c}, 1
			case ks == 0x35: // 5
				return []byte{0x1d}, 1
			case ks == 0x36: // 6
				return []byte{0x1e}, 1
			case ks == 0x37: // 7
				return []byte{0x1f}, 1
			case ks == 0x38, ks == 0x3F: // 8, ?
				return []byte{0x7f}, 1
			case ks == 0x2F: // /
				return []byte{0x1f}, 1
			default:
				return nil, 0
			}
		}
		return nil, 0
	}
	switch {
	case ks >= 0x20 && ks <= 0x7E:
		return []byte{byte(ks)}, 1
	case ks >= 0xA0 && ks <= 0xFF:
		return []byte{byte(ks)}, 1 // Latin-1 supplement (ISO-8859-1)
	case ks >= 0x100 && ks <= 0x10FFFF && !(ks >= 0xFE00 && ks <= 0xFFFF):
		var buf [4]byte
		n := encodeUTF8(buf[:], rune(ks))
		return buf[:n], n
	default:
		// Function keys (0xFE00-0xFFFF), modifiers, and controls
		// produce no text.
		return nil, 0
	}
}

// kmap finds a matching key definition for the keysym+state.
func (t *Terminal) kmap(keysym uint, state uint) (string, bool) {
	appkey := t.termCore.WinMode()&term.ModeAppKeypad != 0
	appcur := t.termCore.WinMode()&term.ModeAppCursor != 0

	for _, k := range t.keys {
		if k.keysym != keysym {
			continue
		}
		if !match(k.mask, int(state)) {
			continue
		}
		if k.appkey != 0 {
			if k.appkey > 0 {
				if !appkey && k.appkey != 2 {
					continue
				}
			} else {
				if appkey {
					continue
				}
			}
		}
		if k.appcur != 0 {
			if k.appcur > 0 {
				if !appcur {
					continue
				}
			} else {
				if appcur {
					continue
				}
			}
		}
		return k.str, true
	}
	return "", false
}

// match implements st's match(): mask is a set of required modifiers,
// XKAnyMod matches any, XKNoMod matches none.
func match(mask int, state int) bool {
	if mask == XKAnyMod {
		return true
	}
	if mask == XKNoMod {
		return state == 0
	}
	return state&mask == mask
}
