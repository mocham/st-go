package x11

import (
	"github.com/BurntSushi/xgb/xproto"
	"log"
)

func (t *Driver) SetupKeys() {
	t.keyOnce.Do(func() {
		setup := xproto.Setup(t.conn)
		t.firstKey = setup.MinKeycode
		count := byte(setup.MaxKeycode - setup.MinKeycode + 1)
		rep, err := xproto.GetKeyboardMapping(t.conn, t.firstKey, count).Reply()
		if err != nil {
			log.Printf("keyboard mapping: %v", err)
			return
		}
		t.keymapReply, t.keysymsPer = rep, rep.KeysymsPerKeycode
	})
}

// keysymForEvent resolves the keysym for a keycode with modifier state,
// using the standard X11 algorithm.
func (t *Driver) keysymForEvent(keycode xproto.Keycode, state uint16) uint {
	idx := (int(keycode) - int(t.firstKey)) * int(t.keysymsPer)
	if t.keymapReply == nil || idx < 0 || idx >= len(t.keymapReply.Keysyms) {
		return 0
	}
	col := 0
	if (state&1 != 0 || state&2 != 0) && int(t.keysymsPer) > 1 {
		col = 1
	}
	if idx+col >= len(t.keymapReply.Keysyms) {
		return 0
	}
	ks := uint(t.keymapReply.Keysyms[idx+col])
	// NoSymbol (0) means no char for this shift state; try col 0
	if ks == 0 && col == 1 {
		ks = uint(t.keymapReply.Keysyms[idx])
	}
	return ks
}
