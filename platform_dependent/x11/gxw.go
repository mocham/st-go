package x11

import (
	"image"

	"github.com/BurntSushi/xgb"
	"github.com/BurntSushi/xgb/xproto"
)

// gxw publishes a pixel-exact initial allocation on its EWMH support window.
// Resolve the live WM each time a terminal opens, rather than inheriting stale
// launch geometry through a shell after a resolution or bar-height change.
func (t *Driver) gxwWorkarea() (image.Rectangle, bool) {
	check, area := t.atoms["_NET_SUPPORTING_WM_CHECK"], t.atoms["_GXW_ST_WORKAREA"]
	if check == 0 || area == 0 {
		return image.Rectangle{}, false
	}
	words := func(window xproto.Window, atom, typ xproto.Atom, n uint32) []uint32 {
		r, err := xproto.GetProperty(t.conn, false, window, atom, typ, 0, n).Reply()
		if err != nil || r.Format != 32 || r.Type != typ || len(r.Value) != int(n)*4 || r.BytesAfter != 0 {
			return nil
		}
		result := make([]uint32, n)
		for i := range result {
			result[i] = xgb.Get32(r.Value[4*i:])
		}
		return result
	}
	owner := words(t.scr.Root, check, xproto.AtomWindow, 1)
	if len(owner) != 1 || owner[0] == 0 {
		return image.Rectangle{}, false
	}
	wm := xproto.Window(owner[0])
	self := words(wm, check, xproto.AtomWindow, 1)
	if len(self) != 1 || self[0] != owner[0] {
		return image.Rectangle{}, false
	}
	r := words(wm, area, xproto.AtomCardinal, 4)
	if len(r) != 4 || r[2] == 0 || r[3] == 0 || uint64(r[0])+uint64(r[2]) > uint64(t.scr.WidthInPixels) || uint64(r[1])+uint64(r[3]) > uint64(t.scr.HeightInPixels) || r[0] > 32767 || r[1] > 32767 {
		return image.Rectangle{}, false
	}
	return image.Rect(int(r[0]), int(r[1]), int(r[0]+r[2]), int(r[1]+r[3])), true
}
