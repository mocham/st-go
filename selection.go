package main

import (
	"github.com/BurntSushi/xgb/xproto"
	"st-go/limits"
)

// setSelection claims PRIMARY ownership with the given text.
func (t *Terminal) setSelection(text string) {
	if t.conn == nil || !limits.Check("clipboard", int64(len(text)), limits.ClipboardBytes) {
		return
	}
	win := t.win
	sel := t.atoms["PRIMARY"]
	t.selectionText = text
	t.primarySet = true
	xproto.SetSelectionOwner(t.conn, win, sel, xproto.TimeCurrentTime)
}

func (t *Terminal) SetClipboard(selection byte, text string) {
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
func (t *Terminal) SetSel(s string) {
	t.setSelection(s)
}

func (t *Terminal) SetIconTitle(s string) {
	if s == "" || t.lockTitle {
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

func (t *Terminal) SetTitle(s string) { t.setTitle(s) }

// getImage reads back the window pixels (test helper).
func getImage(t *Terminal) (*xproto.GetImageReply, error) {
	return xproto.GetImage(t.conn, xproto.ImageFormatZPixmap,
		xproto.Drawable(t.win), int16(0), int16(0),
		uint16(fbW), uint16(fbH), 0xFFFFFFFF).Reply()
}
