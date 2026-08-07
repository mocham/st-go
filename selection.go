package main

import (
	"github.com/BurntSushi/xgb/xproto"
)

// setSelection claims PRIMARY ownership with the given text.
func (t *Terminal) setSelection(text string) {
	win := t.win
	sel := t.atoms["PRIMARY"]
	t.selectionText = text
	xproto.SetSelectionOwner(t.conn, win, sel, xproto.TimeCurrentTime)
}

// SetSel is the Hooks interface entry for OSC 52 / selection text.
func (t *Terminal) SetSel(s string) {
	t.setSelection(s)
}

func (t *Terminal) SetIconTitle(s string) {
	if s == "" {
		return
	}
	t.iconTitle = s
	_ = xproto.ChangePropertyChecked(t.conn, xproto.PropModeReplace, t.win,
		t.atoms["WM_ICON_NAME"], t.atoms["UTF8_STRING"], 8,
		uint32(len(s)), []byte(s)).Check()
}

func (t *Terminal) SetTitle(s string) { t.setTitle(s) }

// getImage reads back the window pixels (test helper).
func getImage(t *Terminal) (*xproto.GetImageReply, error) {
	return xproto.GetImage(t.conn, xproto.ImageFormatZPixmap,
		xproto.Drawable(t.win), int16(0), int16(0),
		uint16(fbW), uint16(fbH), 0xFFFFFFFF).Reply()
}
