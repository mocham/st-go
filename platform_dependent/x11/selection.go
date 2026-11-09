package x11

import (
	"github.com/BurntSushi/xgb/xproto"
	"st-go/limits"
)

// setSelection claims PRIMARY ownership with the given text.
func (t *Driver) setSelection(text string) {
	if t.conn == nil || !limits.Check("clipboard", int64(len(text)), limits.ClipboardBytes) {
		return
	}
	win := t.win
	sel := t.atoms["PRIMARY"]
	t.selectionText = text
	t.primarySet = true
	xproto.SetSelectionOwner(t.conn, win, sel, xproto.TimeCurrentTime)
}

func (t *Driver) SetClipboard(selection byte, text string) {
	if t.conn == nil || t.closed() || !limits.Check("clipboard", int64(len(text)), limits.ClipboardBytes) {
		return
	}
	if selection == 'p' {
		t.setSelection(text)
		return
	}
	if selection != 'c' {
		return
	}
	t.clipboardText, t.clipboardSet = text, true
	xproto.SetSelectionOwner(t.conn, t.win, t.atoms["CLIPBOARD"], xproto.TimeCurrentTime)
}

// SetSel is the Hooks interface entry for OSC 52 / selection text.
func (t *Driver) SetSel(s string) {
	t.setSelection(s)
}

func (t *Driver) SetIconTitle(s string) {
	if s == "" {
		return
	}
	t.iconTitle = s
	// st sets WM_ICON_NAME and _NET_WM_ICON_NAME (xseticontitle)
	_ = xproto.ChangePropertyChecked(t.conn, xproto.PropModeReplace, t.win,
		t.atoms["WM_ICON_NAME"], t.atoms["UTF8_STRING"], 8,
		uint32(len(s)), []byte(s)).Check()
	if t.atoms["_NET_WM_ICON_NAME"] != 0 {
		_ = xproto.ChangePropertyChecked(t.conn, xproto.PropModeReplace, t.win,
			t.atoms["_NET_WM_ICON_NAME"], t.atoms["UTF8_STRING"], 8,
			uint32(len(s)), []byte(s)).Check()
	}
}

// getImage reads back the window pixels (test helper).
func (t *Driver) Image(width, height int) (*xproto.GetImageReply, error) {
	return xproto.GetImage(t.conn, xproto.ImageFormatZPixmap,
		xproto.Drawable(t.win), int16(0), int16(0),
		uint16(width), uint16(height), 0xFFFFFFFF).Reply()
}
