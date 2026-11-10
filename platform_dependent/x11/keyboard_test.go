package x11

import (
	"testing"

	"github.com/BurntSushi/xgb/xproto"
)

func TestKeyboardMappingBelongsToNativeWindow(t *testing.T) {
	a := &Driver{firstKey: 8, keysymsPer: 2, keymapReply: &xproto.GetKeyboardMappingReply{Keysyms: []xproto.Keysym{'a', 'A'}}}
	b := &Driver{firstKey: 10, keysymsPer: 2, keymapReply: &xproto.GetKeyboardMappingReply{Keysyms: []xproto.Keysym{'b', 'B'}}}
	if a.keysymForEvent(8, 0) != 'a' || a.keysymForEvent(8, 1) != 'A' || b.keysymForEvent(10, 0) != 'b' || b.keysymForEvent(10, 1) != 'B' {
		t.Fatal("native windows shared or lost keyboard mapping")
	}
	if a.keysymForEvent(7, 0) != 0 || b.keysymForEvent(20, 0) != 0 {
		t.Fatal("out-of-map keycode was not rejected")
	}
}
