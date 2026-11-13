package main

import (
	"fmt"

	"github.com/jezek/xgb/xproto"
)

func (w *wm) setupKeys() error {
	setup := xproto.Setup(w.conn)
	r, err := xproto.GetKeyboardMapping(w.conn, setup.MinKeycode, byte(int(setup.MaxKeycode)-int(setup.MinKeycode)+1)).Reply()
	if err != nil {
		return err
	}
	m, err := xproto.GetModifierMapping(w.conn).Reply()
	if err != nil {
		return err
	}
	w.ignoredMods = xproto.ModMaskLock
	stride := int(r.KeysymsPerKeycode)
	if stride == 0 {
		return fmt.Errorf("empty X keyboard map")
	}
	for mod := 0; mod < 8; mod++ {
		for _, code := range m.Keycodes[mod*int(m.KeycodesPerModifier) : (mod+1)*int(m.KeycodesPerModifier)] {
			i := (int(code) - int(setup.MinKeycode)) * stride
			if i < 0 || i+stride > len(r.Keysyms) {
				continue
			}
			for _, sym := range r.Keysyms[i : i+stride] {
				if sym == 0xff7f || sym == 0xff14 {
					w.ignoredMods |= 1 << mod
				}
			}
		}
	}
	xproto.UngrabKey(w.conn, xproto.GrabAny, w.root, xproto.ModMaskAny)
	w.keys = make(map[xproto.Keycode]uint32)
	for i := 0; i < len(r.Keysyms); i += stride {
		sym := uint32(r.Keysyms[i])
		code := setup.MinKeycode + xproto.Keycode(i/stride)
		w.keys[code] = sym
		var modifiers []uint16
		switch {
		case sym >= '0' && sym <= '9', sym == 0xff0d, sym == 0xffc1, sym == 0xff51, sym == 0xff53:
			modifiers = []uint16{xproto.ModMask1}
		case sym == 'n', sym == 0xff09:
			modifiers = []uint16{xproto.ModMask1, xproto.ModMask1 | xproto.ModMaskShift}
		case sym == 'q':
			modifiers = []uint16{xproto.ModMask1 | xproto.ModMaskShift}
		case sym == 0xff61, sym == 0x1008ff11, sym == 0x1008ff12, sym == 0x1008ff13, sym == 0x1008ff02, sym == 0x1008ff03:
			modifiers = []uint16{0}
		}
		for _, mods := range modifiers {
			for ignore := uint16(0); ignore < 256; ignore++ {
				if ignore & ^w.ignoredMods != 0 {
					continue
				}
				if err := xproto.GrabKeyChecked(w.conn, false, w.root, mods|ignore, code, xproto.GrabModeAsync, xproto.GrabModeAsync).Check(); err != nil {
					return fmt.Errorf("grab key %#x: %w", sym, err)
				}
			}
		}
	}
	return nil
}

func (w *wm) key(e xproto.KeyPressEvent) {
	sym := w.keys[e.Detail]
	// Button masks share State with keyboard modifiers. In particular a
	// click-to-focus replay may still be pending when the next key arrives.
	mods := e.State & 0xff & ^w.ignoredMods
	if mods == 0 {
		switch sym {
		case 0xff61:
			w.screenshot()
		case 0x1008ff11:
			w.systemAction("vol-")
		case 0x1008ff12:
			w.systemAction("mute")
		case 0x1008ff13:
			w.systemAction("vol+")
		case 0x1008ff02:
			w.systemAction("bright+")
		case 0x1008ff03:
			w.systemAction("bright-")
		}
		return
	}
	if mods == xproto.ModMask1|xproto.ModMaskShift {
		switch sym {
		case 'q':
			w.quit = true
		case 'n', 0xff09:
			w.cycle(-1)
		}
		return
	}
	if mods != xproto.ModMask1 {
		return
	}
	if sym >= '0' && sym <= '9' {
		i := int(sym - '1')
		if sym == '0' {
			i = 9
		}
		if i < len(w.tabs) {
			w.activate(w.tabs[i])
		}
		return
	}
	switch sym {
	case 'n', 0xff09, 0xff53:
		w.cycle(1)
	case 0xff51:
		w.cycle(-1)
	case 0xff0d:
		w.launchTerminal()
	case 0xffc1:
		w.closeClient(w.focused)
	}
}
