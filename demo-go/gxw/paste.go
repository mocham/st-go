package main

import (
	"github.com/jezek/xgb/xproto"
	"time"
)

// Clipboard paste delivers commits to existing applications that did not
// connect a text context to gxw.
func (s *ximServer) pasteText(text string) {
	s.paste = text
	s.pastePending = true
	s.pasteTimer = time.NewTimer(2 * time.Second)
	s.pasteDeadline = s.pasteTimer.C
	xproto.SetSelectionOwner(s.w.conn, s.w.support, s.w.atoms["CLIPBOARD"], xproto.TimeCurrentTime)
	var paste byte
	for code, syms := range s.keysyms {
		for _, sym := range syms {
			if sym == 'v' {
				paste = code
			}
		}
	}
	e := xproto.KeyPressEvent{Detail: xproto.Keycode(paste), State: xproto.ModMaskControl}
	s.nativeKey(capturedKey{event: e})
	s.nativeKey(capturedKey{event: e, release: true})
}

func (s *ximServer) pasteRequest(e xproto.SelectionRequestEvent) {
	property := e.Property
	if property == 0 {
		property = e.Target
	}
	delivered := false
	switch e.Target {
	case s.w.atoms["TARGETS"]:
		data := []uint32{uint32(s.w.atoms["TARGETS"]), uint32(s.w.atoms["UTF8_STRING"]), uint32(s.w.atoms["TEXT"]), uint32(xproto.AtomString)}
		b := make([]byte, 4*len(data))
		for i, a := range data {
			b[i*4] = byte(a)
			b[i*4+1] = byte(a >> 8)
			b[i*4+2] = byte(a >> 16)
			b[i*4+3] = byte(a >> 24)
		}
		xproto.ChangeProperty(s.w.conn, xproto.PropModeReplace, e.Requestor, property, xproto.AtomAtom, 32, uint32(len(data)), b)
	case s.w.atoms["UTF8_STRING"], s.w.atoms["TEXT"], xproto.AtomString:
		xproto.ChangeProperty(s.w.conn, xproto.PropModeReplace, e.Requestor, property, e.Target, 8, uint32(len(s.paste)), []byte(s.paste))
		delivered = true
	default:
		property = 0
	}
	n := xproto.SelectionNotifyEvent{Time: e.Time, Requestor: e.Requestor, Selection: e.Selection, Target: e.Target, Property: property}
	xproto.SendEvent(s.w.conn, false, e.Requestor, 0, string(n.Bytes()))
	if delivered {
		s.finishPaste()
		s.poll()
	}
}

func (s *ximServer) finishPaste() {
	if s.pasteTimer != nil {
		s.pasteTimer.Stop()
		s.pasteTimer = nil
	}
	s.pasteDeadline = nil
	s.pastePending = false
}
