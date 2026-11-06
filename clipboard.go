package main

import (
	"github.com/BurntSushi/xgb/xproto"

	"st-go/limits"
)

// selrequest answers requests for our PRIMARY selection from other clients.
func (t *Terminal) selrequest(e xproto.SelectionRequestEvent) {
	target := e.Target
	prop := e.Property
	if prop == 0 {
		prop = target // requested property "none" -> use target
	}

	ok := func() {
		xproto.SendEvent(t.conn, false, e.Requestor, 0,
			string(xproto.SelectionNotifyEvent{
				Time:      e.Time,
				Requestor: e.Requestor,
				Selection: e.Selection,
				Target:    e.Target,
				Property:  prop,
			}.Bytes()))
	}
	fail := func() {
		xproto.SendEvent(t.conn, false, e.Requestor, 0,
			string(xproto.SelectionNotifyEvent{
				Time:      e.Time,
				Requestor: e.Requestor,
				Selection: e.Selection,
				Target:    e.Target,
				Property:  0,
			}.Bytes()))
	}

	switch target {
	case t.atoms["TARGETS"]:
		// list of supported targets as 32-bit atoms
		targets := []uint32{
			uint32(t.atoms["TARGETS"]),
			uint32(t.atoms["UTF8_STRING"]),
			uint32(t.atoms["STRING"]),
			uint32(t.atoms["TIMESTAMP"]),
		}
		buf := make([]byte, 0, len(targets)*4)
		for _, v := range targets {
			buf = append(buf, byte(v), byte(v>>8), byte(v>>16), byte(v>>24))
		}
		_ = xproto.ChangePropertyChecked(t.conn, xproto.PropModeReplace, e.Requestor,
			prop, t.atoms["ATOM"], 32, uint32(len(targets)), buf).Check()
		ok()
	case t.atoms["TIMESTAMP"]:
		// send a timestamp (0 = current)
		buf := []byte{0, 0, 0, 0}
		_ = xproto.ChangePropertyChecked(t.conn, xproto.PropModeReplace, e.Requestor,
			prop, t.atoms["INTEGER"], 32, 1, buf).Check()
		ok()
	case t.atoms["UTF8_STRING"], t.atoms["STRING"]:
		if t.selectionText == "" {
			fail()
			return
		}
		_ = xproto.ChangePropertyChecked(t.conn, xproto.PropModeReplace, e.Requestor,
			prop, t.atoms["UTF8_STRING"], 8, uint32(len(t.selectionText)),
			[]byte(t.selectionText)).Check()
		ok()
	default:
		fail()
	}
}

// clipcopy copies the current selection into the CLIPBOARD selection.
func (t *Terminal) clipcopy() {
	// copy PRIMARY -> CLIPBOARD by claiming ownership
	sel := t.termCore.GetSel()
	if sel == "" {
		return
	}
	t.selectionText = sel
	xproto.SetSelectionOwner(t.conn, t.win, t.atoms["CLIPBOARD"], xproto.TimeCurrentTime)
}

// clippaste requests the CLIPBOARD content.
func (t *Terminal) clippaste() {
	t.incrActive, t.incrDiscard, t.incrData = false, false, nil
	t.pasteTarget = t.atoms["CLIPBOARD"]
	xproto.ConvertSelection(t.conn, t.win, t.atoms["CLIPBOARD"],
		t.atoms["UTF8_STRING"], t.atoms["CLIPBOARD"], xproto.TimeCurrentTime)
}

// selnotify handles the response to our ConvertSelection request.
func (t *Terminal) selnotify(e xproto.SelectionNotifyEvent) {
	if e.Property == 0 || e.Requestor != t.win {
		return
	}
	// read the property
	rep, err := xproto.GetProperty(t.conn, false, e.Requestor,
		e.Property, 0, 0, limits.ClipboardBytes/4).Reply()
	if err != nil || rep == nil {
		return
	}
	defer xproto.DeleteProperty(t.conn, e.Requestor, e.Property)
	if rep.Type == t.atoms["INCR"] {
		// incremental transfer; start reading chunks
		t.incrActive = true
		t.incrDiscard = false
		t.incrData = nil
		t.incrProperty = e.Property
		if len(rep.Value) >= 4 {
			size := uint32(rep.Value[0]) | uint32(rep.Value[1])<<8 | uint32(rep.Value[2])<<16 | uint32(rep.Value[3])<<24
			t.incrDiscard = !limits.Check("clipboard INCR", int64(size), limits.ClipboardBytes)
		}
		return
	}
	if !limits.Check("clipboard", int64(len(rep.Value))+int64(rep.BytesAfter), limits.ClipboardBytes) {
		return
	}
	text := string(rep.Value)
	t.pasteToTTY(replaceNewlines(text))
}

// propnotify handles INCR chunk data arriving on the property.
func (t *Terminal) propnotify(e xproto.PropertyNotifyEvent) {
	if !t.incrActive || e.State != xproto.PropertyNewValue || e.Window != t.win || e.Atom != t.incrProperty {
		return
	}
	remaining := limits.ClipboardBytes - len(t.incrData)
	if t.incrDiscard {
		remaining = 0 // acknowledge subsequent chunks without downloading them
	}
	rep, err := xproto.GetProperty(t.conn, false, e.Window,
		e.Atom, 0, 0, uint32((remaining+3)/4)).Reply()
	if err != nil || rep == nil {
		return
	}
	defer xproto.DeleteProperty(t.conn, e.Window, e.Atom)
	if len(rep.Value) == 0 && rep.BytesAfter == 0 {
		// ICCCM INCR ends with an empty property of the target type.
		t.incrActive = false
		if !t.incrDiscard {
			t.pasteToTTY(replaceNewlines(string(t.incrData)))
		}
		t.incrData = nil
		t.incrDiscard = false
		return
	}
	if t.incrDiscard {
		return
	}
	if !limits.Check("clipboard INCR", int64(len(t.incrData))+int64(len(rep.Value))+int64(rep.BytesAfter), limits.ClipboardBytes) {
		t.incrDiscard = true
		t.incrData = nil
		return
	}
	t.incrData = append(t.incrData, rep.Value...)
}

func replaceNewlines(s string) string {
	out := make([]byte, 0, len(s))
	for i := 0; i < len(s); i++ {
		if s[i] == '\n' {
			out = append(out, '\r')
		} else {
			out = append(out, s[i])
		}
	}
	return string(out)
}
