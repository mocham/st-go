package main

import (
	"github.com/BurntSushi/xgb/xproto"

	"st-go/term"
)

// keysyms per keycode from the server (min_keycode..max_keycode)
var (
	keymapReply *xproto.GetKeyboardMappingReply
	firstKey    xproto.Keycode
	keysymsPer  byte
)

func (t *Terminal) setupKeys() {
	setup := xproto.Setup(t.conn)
	firstKey = setup.MinKeycode
	count := byte(setup.MaxKeycode - setup.MinKeycode + 1)
	rep, err := xproto.GetKeyboardMapping(t.conn, xproto.Keycode(firstKey), count).Reply()
	if err != nil {
		logf("keyboard mapping: %v", err)
		return
	}
	keymapReply = rep
	keysymsPer = rep.KeysymsPerKeycode
}

// keysymForEvent resolves the keysym for a keycode with modifier state,
// using the standard X11 algorithm.
func (t *Terminal) keysymForEvent(keycode xproto.Keycode, state uint16) uint {
	idx := int(keycode-firstKey) * int(keysymsPer)
	if keymapReply == nil || idx < 0 || idx >= len(keymapReply.Keysyms) {
		return 0
	}
	col := 0
	if (state&ShiftMask != 0 || state&LockMask != 0) && int(keysymsPer) > 1 {
		col = 1
	}
	ks := uint(keymapReply.Keysyms[idx+col])
	// NoSymbol (0) means no char for this shift state; try col 0
	if ks == 0 && col == 1 {
		ks = uint(keymapReply.Keysyms[idx])
	}
	return ks
}

func (t *Terminal) handleShortcut(ks uint, state uint) bool {
	for _, s := range t.shortcuts {
		if s.keysym != ks {
			continue
		}
		if !match(s.mask, int(state)) {
			continue
		}
		return t.runAction(s.action, s.arg)
	}
	return false
}

func (t *Terminal) runAction(action, arg string) bool {
	switch action {
	case "clipcopy":
		t.clipcopy()
	case "clippaste":
		t.clippaste()
	case "selpaste":
		sel := t.termCore.GetSel()
		if sel != "" {
			t.pasteToTTY(sel)
		}
	case "sendbreak":
		// stub
	case "zoom", "zoomreset", "printscreen", "printsel", "toggleprinter", "numlock":
		// stub
	}
	return true
}

// pasteToTTY sends pasted text, honoring bracketed paste mode.
func (t *Terminal) pasteToTTY(s string) {
	if t.termCore.WinMode()&term.ModeBrcktPaste != 0 {
		t.termCore.WriteToTTY([]byte("\x1b[200~"), false)
	}
	t.termCore.WriteToTTY([]byte(s), false)
	if t.termCore.WinMode()&term.ModeBrcktPaste != 0 {
		t.termCore.WriteToTTY([]byte("\x1b[201~"), false)
	}
}
