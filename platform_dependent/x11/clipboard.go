package x11

import (
	"encoding/binary"
	"github.com/BurntSushi/xgb/xproto"
	"strings"
	"time"
	"unicode/utf8"

	"st-go/limits"
)

// selrequest answers requests for our PRIMARY selection from other clients.
func (t *Driver) SelectionRequest(e xproto.SelectionRequestEvent) {
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
		text := t.selectionText
		if e.Selection == t.atoms["CLIPBOARD"] && t.clipboardSet {
			text = t.clipboardText
		}
		if text == "" && !(e.Selection == t.atoms["CLIPBOARD"] && t.clipboardSet) && !(e.Selection == t.atoms["PRIMARY"] && t.primarySet) {
			fail()
			return
		}
		if e.Target == t.atoms["STRING"] {
			var out strings.Builder
			for _, r := range text {
				if r > 255 {
					out.WriteByte('?')
				} else {
					out.WriteByte(byte(r))
				}
			}
			text = out.String()
		}
		if len(text) > 32768 {
			if t.clipboardTransfers == nil {
				t.clipboardTransfers = make(map[clipboardTransferID]*clipboardTransfer)
			}
			for id, transfer := range t.clipboardTransfers {
				if time.Since(transfer.started) > 10*time.Second {
					delete(t.clipboardTransfers, id)
				}
			}
			if len(t.clipboardTransfers) >= 4 {
				fail()
				return
			}
			id := clipboardTransferID{e.Requestor, prop}
			t.clipboardTransfers[id] = &clipboardTransfer{text: text, target: e.Target, started: time.Now()}
			xproto.ChangeWindowAttributes(t.conn, e.Requestor, xproto.CwEventMask, []uint32{xproto.EventMaskPropertyChange})
			var size [4]byte
			binary.LittleEndian.PutUint32(size[:], uint32(len(text)))
			xproto.ChangeProperty(t.conn, xproto.PropModeReplace, e.Requestor, prop, t.atoms["INCR"], 32, 1, size[:])
		} else {
			_ = xproto.ChangePropertyChecked(t.conn, xproto.PropModeReplace, e.Requestor, prop, e.Target, 8, uint32(len(text)), []byte(text)).Check()
		}
		ok()
	default:
		fail()
	}
}

// clippaste requests the CLIPBOARD content.
func (t *Driver) RequestPaste() {
	t.incrActive, t.incrDiscard, t.incrData = false, false, nil
	t.pasteTarget = t.atoms["CLIPBOARD"]
	xproto.ConvertSelection(t.conn, t.win, t.atoms["CLIPBOARD"],
		t.atoms["UTF8_STRING"], t.atoms["CLIPBOARD"], xproto.TimeCurrentTime)
}

// selnotify handles the response to our ConvertSelection request.
func (t *Driver) SelectionNotify(e xproto.SelectionNotifyEvent) {
	if t.clipboardQuery != nil && e.Requestor == t.win && e.Selection == t.clipboardQuery.selection && (e.Property == t.clipboardQuery.property || e.Property == 0 && e.Target == t.clipboardQuery.target) {
		t.clipboardQueryNotify(e)
		return
	}
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
	t.callbacks.Paste(text)
}

// propnotify handles INCR chunk data arriving on the property.
func (t *Driver) PropertyNotify(e xproto.PropertyNotifyEvent) {
	if e.State == xproto.PropertyDelete {
		id := clipboardTransferID{e.Window, e.Atom}
		if transfer := t.clipboardTransfers[id]; transfer != nil {
			end := min(len(transfer.text), transfer.offset+32768)
			chunk := transfer.text[transfer.offset:end]
			xproto.ChangeProperty(t.conn, xproto.PropModeReplace, id.window, id.property, transfer.target, 8, uint32(len(chunk)), []byte(chunk))
			if len(chunk) == 0 {
				delete(t.clipboardTransfers, id)
			} else {
				transfer.offset = end
				transfer.started = time.Now()
			}
			return
		}
	}
	if query := t.clipboardQuery; query != nil && query.incr && e.Window == t.win && e.Atom == query.property && e.State == xproto.PropertyNewValue {
		t.clipboardQueryChunk()
		return
	}
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
			t.callbacks.Paste(string(t.incrData))
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

type clipboardTransferID struct {
	window   xproto.Window
	property xproto.Atom
}
type clipboardTransfer struct {
	text    string
	offset  int
	target  xproto.Atom
	started time.Time
}
type clipboardQuery struct {
	selection, property, target xproto.Atom
	incr, discard               bool
	data                        []byte
	reply                       func(string, bool)
	timer                       *time.Timer
}

// QueryClipboard keeps desktop retrieval in the terminal frontend. The TUI
// receives only an OSC response through its normal input stream.
func (t *Driver) QueryClipboard(selection byte, reply func(string, bool)) {
	if t.conn == nil || t.closed() || t.clipboardQuery != nil {
		reply("", false)
		return
	}
	name := "CLIPBOARD"
	if selection == 'p' {
		name = "PRIMARY"
	} else if selection != 'c' {
		reply("", false)
		return
	}
	atomName := "ST_GO_OSC52"
	property, err := xproto.InternAtom(t.conn, false, uint16(len(atomName)), atomName).Reply()
	if err != nil {
		reply("", false)
		return
	}
	query := &clipboardQuery{selection: t.atoms[name], property: property.Atom, target: t.atoms["UTF8_STRING"], reply: reply}
	t.clipboardQuery = query
	query.timer = time.AfterFunc(1500*time.Millisecond, func() {
		t.callbacks.Lock()
		defer t.callbacks.Unlock()
		if t.clipboardQuery == query && !t.callbacks.Closed() {
			t.finishClipboardQuery("", false)
		}
	})
	xproto.ConvertSelection(t.conn, t.win, query.selection, query.target, query.property, xproto.TimeCurrentTime)
}
func (t *Driver) finishClipboardQuery(text string, ok bool) {
	query := t.clipboardQuery
	if query == nil {
		return
	}
	t.clipboardQuery = nil
	if query.timer != nil {
		query.timer.Stop()
	}
	query.reply(text, ok)
}
func (t *Driver) clipboardQueryNotify(event xproto.SelectionNotifyEvent) {
	query := t.clipboardQuery
	if event.Property == 0 {
		if query.target == t.atoms["UTF8_STRING"] {
			query.target = t.atoms["STRING"]
			xproto.ConvertSelection(t.conn, t.win, query.selection, query.target, query.property, xproto.TimeCurrentTime)
		} else {
			t.finishClipboardQuery("", false)
		}
		return
	}
	if event.Property != query.property {
		return
	}
	property, err := xproto.GetProperty(t.conn, false, t.win, query.property, xproto.GetPropertyTypeAny, 0, limits.ClipboardBytes/4+1).Reply()
	if err != nil || property == nil {
		t.finishClipboardQuery("", false)
		return
	}
	defer xproto.DeleteProperty(t.conn, t.win, query.property)
	if property.Type == t.atoms["INCR"] {
		query.incr = true
		if len(property.Value) >= 4 && binary.LittleEndian.Uint32(property.Value) > limits.ClipboardBytes {
			query.discard = true
		}
		return
	}
	if property.BytesAfter > 0 || len(property.Value) > limits.ClipboardBytes {
		t.finishClipboardQuery("", false)
		return
	}
	text, ok := t.clipboardQueryText(property.Type, property.Format, property.Value)
	t.finishClipboardQuery(text, ok)
}
func (t *Driver) clipboardQueryText(target xproto.Atom, format byte, data []byte) (string, bool) {
	if format != 8 {
		return "", false
	}
	if target == t.atoms["STRING"] {
		runes := make([]rune, len(data))
		for i, b := range data {
			runes[i] = rune(b)
		}
		text := string(runes)
		return text, len(text) <= limits.ClipboardBytes
	}
	return string(data), target == t.atoms["UTF8_STRING"] && utf8.Valid(data)
}
func (t *Driver) clipboardQueryChunk() {
	query := t.clipboardQuery
	remaining := limits.ClipboardBytes - len(query.data)
	if query.discard {
		remaining = 0
	}
	property, err := xproto.GetProperty(t.conn, false, t.win, query.property, xproto.GetPropertyTypeAny, 0, uint32((remaining+3)/4)).Reply()
	if err != nil || property == nil {
		t.finishClipboardQuery("", false)
		return
	}
	defer xproto.DeleteProperty(t.conn, t.win, query.property)
	if len(property.Value) == 0 && property.BytesAfter == 0 {
		if query.discard {
			t.finishClipboardQuery("", false)
		} else {
			text, ok := t.clipboardQueryText(query.target, 8, query.data)
			t.finishClipboardQuery(text, ok)
		}
		return
	}
	if query.discard {
		return
	}
	if property.Type != query.target || property.Format != 8 || len(query.data)+len(property.Value)+int(property.BytesAfter) > limits.ClipboardBytes {
		query.discard = true
		query.data = nil
		return
	}
	query.data = append(query.data, property.Value...)
}
