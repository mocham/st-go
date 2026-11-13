//go:build linux

package fb

import (
	"unicode/utf8"

	"st-go/platform_neutral/api"
)

const (
	shiftMask   uint16 = 1
	controlMask uint16 = 4
	altMask     uint16 = 8
	superMask   uint16 = 64
)

type keyboard struct {
	maps         [4][256]uint16 // kernel normal, Shift, AltGr, Shift+AltGr maps
	pressed      [256]bool
	caps, num    bool
	accents      map[[2]rune]rune
	dead         rune
	compose      bool
	prefix       [3]byte
	prefixLength int
}

func (k *keyboard) reset() {
	k.pressed = [256]bool{}
	k.prefixLength = 0
	k.dead = 0
	k.compose = false
}
func (k *keyboard) modifiers() uint16 {
	var mask uint16
	for code, down := range k.pressed {
		if !down {
			continue
		}
		symbol := k.maps[0][code]
		if symbol>>8 == 7 { // Linux KT_SHIFT, including left/right variants.
			switch symbol & 255 {
			case 0, 4, 5, 8:
				mask |= shiftMask
			case 2, 6, 7:
				mask |= controlMask
			case 3:
				mask |= altMask
			}
		}
		if code == 125 || code == 126 {
			mask |= superMask
		}
	}
	return mask
}
func (k *keyboard) altGr() bool {
	for code, down := range k.pressed {
		if down && k.maps[0][code] == 0x0701 {
			return true
		}
	}
	return false
}

// Linux's extended medium-raw records are either one byte (<128 keycode), or
// 0/up-flag followed by two seven-bit keycode bytes with their high bits set.
func (k *keyboard) feed(data []byte, cb api.Callbacks, switchVT func(int), closeHost func()) {
	for _, b := range data {
		if cb.Closed != nil && cb.Closed() {
			return
		}
		if k.prefixLength == 0 && b&127 != 0 {
			k.key(int(b&127), b&128 == 0, cb, switchVT, closeHost)
			continue
		}
		if k.prefixLength == 0 {
			k.prefix[0] = b
			k.prefixLength = 1
			continue
		}
		if b&128 == 0 {
			k.prefixLength = 0
			continue
		}
		k.prefix[k.prefixLength] = b
		k.prefixLength++
		if k.prefixLength == 3 {
			code := int(k.prefix[1]&127)<<7 | int(k.prefix[2]&127)
			k.key(code, k.prefix[0]&128 == 0, cb, switchVT, closeHost)
			k.prefixLength = 0
		}
	}
}

func (k *keyboard) key(code int, down bool, cb api.Callbacks, switchVT func(int), closeHost func()) {
	if code < 0 || code >= len(k.pressed) {
		return
	}
	repeat := k.pressed[code]
	k.pressed[code] = down
	if !down {
		return
	}
	mods := k.modifiers()
	if key := systemMediaKey(code); key != 0 {
		if k.altGr() {
			mods |= 128 // X11 Mod5: media shortcuts require no held modifiers.
		}
		cb.KeyPress(key, mods)
		return
	}
	if mods&(controlMask|altMask) == controlMask|altMask {
		if code == 14 {
			if !repeat {
				closeHost()
			}
			return
		} // emergency Ctrl-Alt-Backspace
		n := 0
		if code >= 59 && code <= 68 {
			n = code - 58
		} else if code == 87 || code == 88 {
			n = code - 76
		}
		if n != 0 {
			if !repeat {
				switchVT(n)
			}
			return
		}
	}
	table := 0
	if mods&shiftMask != 0 {
		table |= 1
	}
	if k.altGr() {
		table |= 2
	}
	symbol := k.maps[table][code]
	// Console Shift-Fn and Shift-navigation maps often select other function
	// strings or kernel scrollback commands. Keep the base key plus modifiers
	// for the shared terminal keymap, just as other display hosts do.
	baseKind := k.maps[0][code] >> 8
	if table != 0 && (baseKind == 1 || baseKind == 3 || baseKind == 6) {
		symbol = k.maps[0][code]
	}
	if symbol>>8 == 11 && k.caps {
		symbol = k.maps[table^1][code]
	}
	kind, value := symbol>>8, symbol&255
	if kind == 7 || kind == 12 {
		return
	} // modifiers are state, not text
	if kind == 2 {
		switch value {
		case 1:
			cb.KeyPress(0xff0d, mods)
		case 7:
			if !repeat {
				k.caps = !k.caps
			}
		case 8, 19:
			if !repeat {
				k.num = !k.num
			}
		case 14:
			k.compose = true
			k.dead = 0
		}
		return
	}
	if kind == 1 {
		key := uint(0)
		switch {
		case value < 20:
			key = 0xffbe + uint(value)
		case value >= 30 && value <= 44:
			key = 0xffbe + uint(value-10)
		default:
			key = map[uint16]uint{20: 0xff50, 21: 0xff63, 22: 0xffff, 23: 0xff57, 24: 0xff55, 25: 0xff56, 29: 0xff13}[value]
		}
		if key != 0 {
			cb.KeyPress(key, mods)
		}
		return
	}
	if kind == 6 && value < 4 {
		cb.KeyPress([]uint{0xff54, 0xff51, 0xff53, 0xff52}[value], mods)
		return
	}
	if kind == 3 {
		if value <= 9 {
			if k.num {
				cb.KeyPress(uint('0'+value), mods)
			} else {
				cb.KeyPress([]uint{0xff63, 0xff57, 0xff54, 0xff56, 0xff51, 0xff9d, 0xff53, 0xff50, 0xff52, 0xff55}[value], mods)
			}
		} else if value == 14 {
			cb.KeyPress(0xff8d, mods)
		} else if value == 16 && !k.num {
			cb.KeyPress(0xffff, mods)
		} else {
			if r := map[uint16]rune{10: '+', 11: '-', 12: '*', 13: '/', 15: ',', 16: '.'}[value]; r != 0 {
				cb.KeyPress(uint(r), mods)
			}
		}
		return
	}
	if kind == 4 || kind == 13 {
		r := rune(value)
		if kind == 4 {
			const dead = "`'^~\",_U.*=cki#o!?+-)(:n;$@"
			if int(value) >= len(dead) {
				return
			}
			r = rune(dead[value])
		}
		k.dead = r
		return
	}
	var r rune
	switch {
	case kind == 0 || kind == 11:
		r = rune(value)
	case kind > 15:
		r = rune(symbol ^ 0xf000) // kernel Unicode keymap representation
	default:
		return
	}
	if !utf8.ValidRune(r) || r == 0 || r == 0xffff {
		return
	}
	if k.compose && k.dead == 0 {
		k.dead = r
		return
	}
	if k.dead != 0 {
		d := k.dead
		k.dead = 0
		k.compose = false
		if result, ok := k.accents[[2]rune{d, r}]; ok {
			r = result
		} else if r == ' ' || r == d {
			r = d
		} else {
			cb.Text(string(d))
		}
	}
	switch r {
	case 8, 127:
		cb.KeyPress(0xff08, mods)
	case 9:
		if mods&shiftMask != 0 {
			cb.KeyPress(0xfe20, mods)
		} else {
			cb.KeyPress(0xff09, mods)
		}
	case 13:
		cb.KeyPress(0xff0d, mods)
	case 27:
		cb.KeyPress(0xff1b, mods)
	default:
		if r >= 32 && r <= 126 {
			cb.KeyPress(uint(r), mods)
		} else if mods&controlMask == 0 {
			text := string(r)
			if mods&altMask != 0 {
				text = "\x1b" + text
			}
			cb.Text(text)
		}
	}
}
