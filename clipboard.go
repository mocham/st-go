package main

import (
	"github.com/BurntSushi/xgb/xproto"
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
	t.pasteTarget = t.atoms["CLIPBOARD"]
	xproto.ConvertSelection(t.conn, t.win, t.atoms["CLIPBOARD"],
		t.atoms["UTF8_STRING"], t.atoms["CLIPBOARD"], xproto.TimeCurrentTime)
}

// selnotify handles the response to our ConvertSelection request.
func (t *Terminal) selnotify(e xproto.SelectionNotifyEvent) {
	if e.Property == 0 {
		return
	}
	// read the property
	rep, err := xproto.GetProperty(t.conn, false, e.Requestor,
		e.Property, 0, 0, 0x7FFFFFFF).Reply()
	if err != nil {
		return
	}
	if rep.Type == t.atoms["INCR"] {
		// incremental transfer; start reading chunks
		t.incrActive = true
		t.incrData = nil
		t.incrProperty = e.Property
		xproto.DeleteProperty(t.conn, e.Requestor, e.Property)
		return
	}
	text := string(rep.Value)
	t.pasteToTTY(replaceNewlines(text))
	xproto.DeleteProperty(t.conn, e.Requestor, e.Property)
}

// propnotify handles INCR chunk data arriving on the property.
func (t *Terminal) propnotify(e xproto.PropertyNotifyEvent) {
	if !t.incrActive || e.State != xproto.PropertyNewValue {
		return
	}
	rep, err := xproto.GetProperty(t.conn, false, e.Window,
		e.Atom, 0, 0, 0x7FFFFFFF).Reply()
	if err != nil {
		return
	}
	if rep.Type == t.atoms["INCR"] {
		// transfer complete
		t.incrActive = false
		t.pasteToTTY(replaceNewlines(string(t.incrData)))
		xproto.DeleteProperty(t.conn, e.Window, e.Atom)
		return
	}
	t.incrData = append(t.incrData, rep.Value...)
	xproto.DeleteProperty(t.conn, e.Window, e.Atom)
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
