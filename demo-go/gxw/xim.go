package main

// XIM's X transport and root-window preedit style. X resources, protocol state
// and editor state are owned by the WM goroutine. Only dictionary work is async.
// Wire reference: https://www.x.org/releases/current/doc/libX11/XIM/xim.html
import (
	"encoding/binary"
	"fmt"
	"image"
	"image/draw"
	"os"
	"slices"
	"strings"
	"time"

	"github.com/jezek/xgb"
	"github.com/jezek/xgb/xproto"
)

const ximLimit = 65536

type ximServer struct {
	w             *wm
	selection     xproto.Atom
	peers         map[xproto.Window]*ximPeer
	enabled       bool
	wake          chan struct{}
	focused       *ximContext
	popup         xproto.Window
	popupVisible  bool
	popupBounds   image.Rectangle
	popupText     string
	keysyms       map[byte][]xproto.Keysym
	capture       *keyboardCapture
	barLabel      string
	paste         string
	pastePending  bool
	pasteTimer    *time.Timer
	pasteDeadline <-chan time.Time
}
type ximPeer struct {
	s                       *ximServer
	window, client          xproto.Window
	order                   binary.ByteOrder
	contexts                map[uint16]*ximContext
	next                    uint16
	opened, utf8, connected bool
	waiting, synchronous    bool
	queued                  [][]byte
	output                  []ximPacket
	propertyOffset          map[xproto.Atom]uint32
}
type ximContext struct {
	p     *ximPeer
	id    uint16
	attrs map[uint16][]byte
}

type ximPacket struct {
	op   byte
	body []byte
}

// Xlib puts fabricated events at the front of its queue. Wait for its
// acknowledgement before sending another commit/forward, or literal bracket
// recovery followed by a native key can arrive backwards.
func (p *ximPeer) sendInput(op byte, body []byte) {
	if p.waiting {
		if len(p.output) >= 256 {
			p.s.remove(p)
			return
		}
		p.output = append(p.output, ximPacket{op, body})
		return
	}
	p.waiting = true
	p.send(op, body)
}

func (w *wm) startXIM() error {
	s := &ximServer{w: w, enabled: true, peers: make(map[xproto.Window]*ximPeer), wake: make(chan struct{}, 1)}
	for _, name := range []string{"@server=gxw", "XIM_SERVERS", "LOCALES", "TRANSPORT", "_XIM_XCONNECT", "_XIM_PROTOCOL", "_GXW_XIM_DATA", "TARGETS", "UTF8_STRING", "TEXT", "CLIPBOARD"} {
		if _, err := w.intern(name); err != nil {
			return err
		}
	}
	s.selection = w.atoms["@server=gxw"]
	owner, err := xproto.GetSelectionOwner(w.conn, s.selection).Reply()
	if err != nil {
		return err
	}
	if owner.Owner != 0 {
		return fmt.Errorf("gxw XIM selection already owned")
	}
	xproto.SetSelectionOwner(w.conn, w.support, s.selection, xproto.TimeCurrentTime)
	servers := w.words(w.root, "XIM_SERVERS")
	if !slices.Contains(servers, uint32(s.selection)) {
		servers = append(servers, uint32(s.selection))
	}
	w.setWords(w.root, "XIM_SERVERS", xproto.AtomAtom, servers...)
	w.xim = s
	if err := s.loadKeys(); err != nil {
		return err
	}
	return s.grabTriggers()
}

func (s *ximServer) loadKeys() error {
	setup := xproto.Setup(s.w.conn)
	r, err := xproto.GetKeyboardMapping(s.w.conn, setup.MinKeycode, byte(int(setup.MaxKeycode)-int(setup.MinKeycode)+1)).Reply()
	if err != nil {
		return err
	}
	s.keysyms = make(map[byte][]xproto.Keysym)
	stride := int(r.KeysymsPerKeycode)
	if stride == 0 {
		return fmt.Errorf("empty XIM keymap")
	}
	for i := 0; i+stride <= len(r.Keysyms); i += stride {
		s.keysyms[byte(setup.MinKeycode)+byte(i/stride)] = r.Keysyms[i : i+stride]
	}
	return nil
}

func (s *ximServer) selectionRequest(e xproto.SelectionRequestEvent) {
	property := e.Property
	if property == 0 {
		property = e.Target
	}
	var text string
	switch e.Target {
	case s.w.atoms["TRANSPORT"]:
		text = "@transport=X/"
	case s.w.atoms["LOCALES"]:
		text = "@locale=C,C.UTF-8,C.utf8,en_US.UTF-8,en_US.utf8,zh_CN.UTF-8,zh_CN.utf8,zh_TW.UTF-8,ja_JP.UTF-8,ko_KR.UTF-8"
		for _, key := range []string{"LC_ALL", "LC_CTYPE", "LANG"} {
			if locale := os.Getenv(key); locale != "" && !strings.ContainsAny(locale, ",\x00") {
				text += "," + locale
			}
		}
	default:
		property = 0
	}
	if property != 0 {
		xproto.ChangeProperty(s.w.conn, xproto.PropModeReplace, e.Requestor, property, e.Target, 8, uint32(len(text)), []byte(text))
	}
	n := xproto.SelectionNotifyEvent{Time: e.Time, Requestor: e.Requestor, Selection: e.Selection, Target: e.Target, Property: property}
	xproto.SendEvent(s.w.conn, false, e.Requestor, 0, string(n.Bytes()))
}

func (s *ximServer) event(event xgb.Event) bool {
	switch e := event.(type) {
	case xproto.KeyPressEvent:
		return s.captureKey(capturedKey{event: e})
	case xproto.KeyReleaseEvent:
		return s.captureKey(capturedKey{event: xproto.KeyPressEvent(e), release: true})
	case xproto.SelectionRequestEvent:
		if e.Selection == s.w.atoms["CLIPBOARD"] && s.paste != "" {
			s.pasteRequest(e)
			return true
		}
		if e.Selection == s.selection {
			s.selectionRequest(e)
			return true
		}
	case xproto.DestroyNotifyEvent:
		if s.capture != nil && s.capture.focus == e.Window {
			s.endCapture()
		}
		for _, p := range s.peers {
			if e.Window == p.client {
				s.remove(p)
			}
		}
	case xproto.ExposeEvent:
		if e.Window == s.popup {
			if e.Count == 0 {
				s.popupText = ""
				s.draw()
			}
			return true
		}
	case xproto.SelectionClearEvent:
		if e.Selection == s.selection {
			s.enabled = false
			s.toggle(false)
			return true
		}
	case xproto.MappingNotifyEvent:
		s.loadKeys()
	case xproto.ClientMessageEvent:
		if e.Type == s.w.atoms["_XIM_XCONNECT"] && e.Window == s.w.support && e.Format == 32 {
			if len(s.peers) >= 128 {
				return true
			}
			client := xproto.Window(e.Data.Data32[0])
			window, err := s.w.newWindow(0, 0, 1, 1, "gxw XIM")
			if err != nil {
				return true
			}
			if err := xproto.ChangeWindowAttributesChecked(s.w.conn, client, xproto.CwEventMask, []uint32{xproto.EventMaskStructureNotify}).Check(); err != nil {
				xproto.DestroyWindow(s.w.conn, window)
				return true
			}
			p := &ximPeer{s: s, window: window, client: client, order: binary.LittleEndian, contexts: make(map[uint16]*ximContext), propertyOffset: make(map[xproto.Atom]uint32)}
			s.peers[window] = p
			reply := xproto.ClientMessageEvent{Format: 32, Window: client, Type: e.Type, Data: xproto.ClientMessageDataUnionData32New([]uint32{uint32(window), 0, 0, 20, 0})}
			xproto.SendEvent(s.w.conn, false, client, 0, string(reply.Bytes()))
			return true
		}
		if e.Type != s.w.atoms["_XIM_PROTOCOL"] {
			break
		}
		p := s.peers[e.Window]
		if p == nil {
			return true
		}
		var data []byte
		switch e.Format {
		case 8:
			data = e.Data.Data8
		case 32:
			n, atom := e.Data.Data32[0], xproto.Atom(e.Data.Data32[1])
			if n < 4 || n > ximLimit || n%4 != 0 {
				s.remove(p)
				return true
			}
			r, err := xproto.GetProperty(s.w.conn, true, p.window, atom, xproto.AtomString, p.propertyOffset[atom], n/4).Reply()
			if err != nil || r.Format != 8 || len(r.Value) != int(n) {
				s.remove(p)
				return true
			}
			data = r.Value
			if r.BytesAfter > 0 {
				p.propertyOffset[atom] += n / 4
			} else {
				delete(p.propertyOffset, atom)
			}
		default:
			return true
		}
		p.receive(data)
		return true
	}
	return false
}

func (s *ximServer) remove(p *ximPeer) {
	if s.capture != nil && s.capture.context != nil && s.capture.context.p == p {
		s.endCapture()
	}
	for _, c := range p.contexts {
		if s.focused == c {
			s.focused = nil
		}
	}
	delete(s.peers, p.window)
	xproto.DestroyWindow(s.w.conn, p.window)
	s.draw()
}

func (p *ximPeer) u16(v uint16) []byte { b := make([]byte, 2); p.order.PutUint16(b, v); return b }
func (p *ximPeer) u32(v uint32) []byte { b := make([]byte, 4); p.order.PutUint32(b, v); return b }
func join(parts ...[]byte) []byte {
	var b []byte
	for _, part := range parts {
		b = append(b, part...)
	}
	return b
}
func padded(b []byte) []byte            { return append(b, make([]byte, (-len(b))&3)...) }
func (p *ximPeer) ids(ic uint16) []byte { return join(p.u16(1), p.u16(ic)) }
func (p *ximPeer) send(op byte, body []byte) {
	body = padded(body)
	data := join([]byte{op, 0}, p.u16(uint16(len(body)/4)), body)
	w := p.s.w
	e := xproto.ClientMessageEvent{Window: p.client, Type: w.atoms["_XIM_PROTOCOL"]}
	if len(data) <= 20 {
		e.Format = 8
		data = append(data, make([]byte, 20-len(data))...)
		e.Data = xproto.ClientMessageDataUnionData8New(data)
	} else {
		// Append, rather than replace: multiple replies can precede a client's
		// property read. Xlib tracks the property offset until the final read.
		atom := w.atoms["_GXW_XIM_DATA"]
		xproto.ChangeProperty(w.conn, xproto.PropModeAppend, p.client, atom, xproto.AtomString, 8, uint32(len(data)), data)
		e.Format = 32
		e.Data = xproto.ClientMessageDataUnionData32New([]uint32{uint32(len(data)), uint32(atom), 0, 0, 0})
	}
	xproto.SendEvent(w.conn, false, p.client, 0, string(e.Bytes()))
}
func (p *ximPeer) fail(ic uint16) {
	p.send(20, join(p.ids(ic), p.u16(1), p.u16(13), p.u16(0), p.u16(0)))
}

type ximAttr struct {
	id, kind uint16
	name     string
}

var ximIMAttrs = []ximAttr{{1, 10, "queryInputStyle"}}
var ximICAttrs = []ximAttr{{1, 3, "inputStyle"}, {2, 5, "clientWindow"}, {3, 5, "focusWindow"}, {4, 3, "filterEvents"}, {5, 0, "separatorofNestedList"}}

func (p *ximPeer) attrList(attrs []ximAttr) []byte {
	var data []byte
	for _, a := range attrs {
		data = append(data, padded(join(p.u16(a.id), p.u16(a.kind), p.u16(uint16(len(a.name))), []byte(a.name)))...)
	}
	return data
}
func (p *ximPeer) attr(id uint16, value []byte) []byte {
	return join(p.u16(id), p.u16(uint16(len(value))), padded(value))
}
func (p *ximPeer) readAttrs(data []byte, attrs map[uint16][]byte) bool {
	for len(data) != 0 {
		if len(data) < 4 {
			return false
		}
		id, n := p.order.Uint16(data), int(p.order.Uint16(data[2:]))
		total := 4 + ((n + 3) &^ 3)
		if total > len(data) || id == 0 || id > 5 {
			return false
		}
		attrs[id] = slices.Clone(data[4 : 4+n])
		data = data[total:]
	}
	return true
}

func (p *ximPeer) receive(data []byte) {
	if len(data) < 4 {
		return
	}
	if !p.connected {
		if data[0] != 1 || len(data) < 12 {
			return
		}
		switch data[4] {
		case 'B':
			p.order = binary.BigEndian
		case 'l':
			p.order = binary.LittleEndian
		default:
			return
		}
	}
	n := int(p.order.Uint16(data[2:]))*4 + 4
	if n > len(data) || n > ximLimit {
		p.fail(0)
		return
	}
	data = data[:n]
	op, b := data[0], data[4:]
	if op == 60 && p.waiting {
		if len(p.queued) >= 256 {
			p.s.remove(p)
			return
		}
		p.queued = append(p.queued, slices.Clone(data))
		return
	}
	if op == 1 {
		p.connected = true
		p.send(2, join(p.u16(1), p.u16(0)))
		return
	}
	if op == 3 {
		p.send(4, nil)
		p.s.remove(p)
		return
	}
	if op == 30 {
		im, ic := p.attrList(ximIMAttrs), p.attrList(ximICAttrs)
		p.opened = true
		p.send(31, join(p.u16(1), p.u16(uint16(len(im))), im, p.u16(uint16(len(ic))), p.u16(0), ic))
		p.send(37, join(p.ids(0), p.u32(3), p.u32(0)))
		return
	}
	if len(b) < 4 || !p.opened || p.order.Uint16(b) != 1 {
		p.fail(0)
		return
	}
	ic := p.order.Uint16(b[2:])
	if op == 62 { // SYNC_REPLY can acknowledge a context just destroyed by its client.
		p.waiting = false
		p.drain()
		if !p.waiting {
			p.s.poll()
		}
		return
	}
	switch op {
	case 32: // CLOSE
		if p.s.capture != nil && p.s.capture.context != nil && p.s.capture.context.p == p {
			p.s.flushCapture()
		}
		for _, c := range p.contexts {
			if p.s.focused == c {
				p.s.focused = nil
			}
		}
		clear(p.contexts)
		p.opened = false
		p.send(33, p.ids(0))
		p.s.draw()
	case 38: // ENCODING_NEGOTIATION
		n := int(ic)
		if n > len(b)-4 {
			p.fail(0)
			return
		}
		index, chosen := 0, -1
		for names := b[4 : 4+n]; len(names) > 0; index++ {
			length := int(names[0])
			names = names[1:]
			if length > len(names) {
				p.fail(0)
				return
			}
			name := string(names[:length])
			names = names[length:]
			if name == "COMPOUND_TEXT" && chosen < 0 {
				chosen = index
			}
			if name == "UTF8_STRING" {
				chosen = index
				p.utf8 = true
			}
		}
		p.send(39, join(p.ids(0), p.u16(uint16(chosen)), p.u16(0)))
	case 40: // QUERY_EXTENSION
		p.send(41, p.ids(0)) // no protocol extensions
	case 42: // SET_IM_VALUES
		p.send(43, p.ids(0))
	case 44: // GET_IM_VALUES
		styles := join(p.u16(1), p.u16(0), p.u32(0x408)) // PreeditNothing | StatusNothing
		attrs := p.attr(1, styles)
		p.send(45, join(p.u16(1), p.u16(uint16(len(attrs))), attrs))
	case 50: // CREATE_IC
		if int(ic) > len(b)-4 || len(p.contexts) >= 128 {
			p.fail(0)
			return
		}
		attrs := make(map[uint16][]byte)
		if !p.readAttrs(b[4:4+int(ic)], attrs) {
			p.fail(0)
			return
		}
		if len(attrs[1]) != 4 || p.order.Uint32(attrs[1]) != 0x408 {
			p.fail(0)
			return
		}
		for {
			p.next++
			if p.next != 0 && p.contexts[p.next] == nil {
				break
			}
		}
		c := &ximContext{p: p, id: p.next, attrs: attrs}
		p.contexts[c.id] = c
		p.send(51, p.ids(c.id))
		p.send(37, join(p.ids(c.id), p.u32(3), p.u32(0)))
	default:
		c := p.contexts[ic]
		if c == nil {
			p.fail(ic)
			return
		}
		switch op {
		case 52: // DESTROY_IC
			if p.s.capture != nil && p.s.capture.context == c {
				p.s.commitCapture(p.s.capture.stream.Flush())
				p.s.endCapture()
			}
			delete(p.contexts, ic)
			if p.s.focused == c {
				p.s.focused = nil
			}
			p.send(53, p.ids(ic))
			p.s.draw()
		case 54: // SET_IC_VALUES
			if len(b) < 8 || int(p.order.Uint16(b[4:])) > len(b)-8 || !p.readAttrs(b[8:8+int(p.order.Uint16(b[4:]))], c.attrs) {
				p.fail(ic)
				return
			}
			p.send(55, p.ids(ic))
		case 56: // GET_IC_VALUES
			if len(b) < 6 {
				p.fail(ic)
				return
			}
			n := int(p.order.Uint16(b[4:]))
			if n > len(b)-6 || n%2 != 0 {
				p.fail(ic)
				return
			}
			var attrs []byte
			for at := 6; at < 6+n; at += 2 {
				id := p.order.Uint16(b[at:])
				value := c.attrs[id]
				if id == 4 {
					value = p.u32(3)
				}
				attrs = append(attrs, p.attr(id, value)...)
			}
			p.send(57, join(p.ids(ic), p.u16(uint16(len(attrs))), p.u16(0), attrs))
		case 58: // SET_IC_FOCUS
			p.s.focused = c
			if capture := p.s.capture; capture != nil && capture.context == nil && p.s.contextAt(c, capture.focus) {
				capture.context = c
				p.sendInput(61, p.ids(c.id))
			}
			p.s.draw()
		case 59: // UNSET_IC_FOCUS
			if p.s.capture != nil && p.s.capture.context == c {
				p.s.commitCapture(p.s.capture.stream.Flush())
				p.s.endCapture()
			}
			if p.s.focused == c {
				p.s.focused = nil
			}
			p.s.draw()
		case 60: // FORWARD_EVENT
			c.forward(b)
		case 61: // SYNC
			p.send(62, p.ids(ic))
		case 64: // RESET_IC
			var text []byte
			if p.s.capture != nil && p.s.capture.context == c {
				text = c.encode(p.s.capture.stream.Flush())
				p.s.endCapture()
			}
			p.send(65, join(p.ids(ic), p.u16(uint16(len(text))), text))
			p.s.draw()
		default:
			p.fail(ic)
		}
	}
}

func (c *ximContext) forward(b []byte) {
	p := c.p
	if len(b) < 40 {
		p.fail(c.id)
		return
	}
	p.synchronous = p.order.Uint16(b[4:])&1 != 0
	defer func() {
		if p.synchronous {
			p.send(62, p.ids(c.id))
		}
		p.synchronous = false
	}()
	// Trigger handling belongs to the WM's keyboard grab. Forward application
	// events unchanged, including replayed brackets, without filtering twice.
	p.sendInput(60, join(p.ids(c.id), p.u16(1), b[6:40]))
}

func (c *ximContext) encode(text string) []byte {
	if text == "" {
		return nil
	}
	if c.p.utf8 {
		return []byte(text)
	}
	// COMPOUND_TEXT's registered UTF-8 extension (Xlib's ISO10646 path).
	if strings.IndexFunc(text, func(r rune) bool { return r >= 128 }) >= 0 {
		return []byte("\x1b%G" + text + "\x1b%@")
	}
	return []byte(text)
}
func (c *ximContext) commit(text string) {
	if text == "" {
		return
	}
	p := c.p
	data := c.encode(text)
	p.sendInput(63, join(p.ids(c.id), p.u16(3), p.u16(uint16(len(data))), data))
}

func (s *ximServer) poll() {
	for _, p := range s.peers {
		if p.waiting {
			continue
		}
		p.drain()
	}
	s.drainCapture()
	s.draw()
}

func (p *ximPeer) drain() {
	if p.waiting {
		return
	}
	if len(p.output) > 0 {
		next := p.output[0]
		p.output = p.output[1:]
		p.sendInput(next.op, next.body)
		return
	}
	for !p.waiting && len(p.queued) > 0 {
		next := p.queued[0]
		p.queued = p.queued[1:]
		p.receive(next)
	}
}
func (s *ximServer) toggle(enabled bool) {
	if s.capture != nil {
		s.commitCapture(s.capture.stream.Flush())
	}
	s.enabled = enabled
	if s.capture != nil {
		s.capture.stream.Enabled = enabled
		s.drainCapture()
	}
	s.draw()
	s.w.drawBars()
}
func (s *ximServer) draw() {
	if label := s.captureLabel(); s.barLabel != label {
		s.barLabel = label
		s.w.setText(s.w.systembar, "_NET_WM_NAME", "gxw system bar — "+label)
		s.w.drawBars()
	}
	if s.capture == nil || !s.capture.stream.Active() {
		if s.popupVisible {
			xproto.UnmapWindow(s.w.conn, s.popup)
			s.popupVisible = false
		}
		return
	}
	w := s.w
	height := w.barHeight + 8
	y := w.workarea().Max.Y - height
	bounds := image.Rect(0, y, w.width, y+height)
	text := s.capture.stream.Display(max(1, w.width/(10*w.scale)))
	if s.popupVisible && s.popupBounds == bounds && s.popupText == text {
		return
	}
	if s.popup == 0 {
		s.popup, _ = w.newWindow(0, y, w.width, height, "gxw Pinyin")
	}
	if s.popup == 0 {
		return
	}
	s.popupVisible, s.popupBounds, s.popupText = true, bounds, text
	xproto.ConfigureWindow(w.conn, s.popup, xproto.ConfigWindowY|xproto.ConfigWindowWidth|xproto.ConfigWindowHeight|xproto.ConfigWindowStackMode, []uint32{uint32(y), uint32(w.width), uint32(height), xproto.StackModeAbove})
	img := image.NewRGBA(image.Rect(0, 0, w.width, height))
	draw.Draw(img, img.Rect, image.NewUniform(barBG), image.Point{}, draw.Src)
	w.label(img, img.Rect.Inset(4), text, barFG)
	xproto.MapWindow(w.conn, s.popup)
	w.upload(s.popup, img)
}
func (s *ximServer) close() {
	if s.capture != nil {
		s.capture.stream.Close()
		s.capture = nil
		xproto.UngrabKeyboard(s.w.conn, xproto.TimeCurrentTime)
	}
	s.finishPaste()
	servers := s.w.words(s.w.root, "XIM_SERVERS")
	servers = slices.DeleteFunc(servers, func(a uint32) bool { return a == uint32(s.selection) })
	s.w.setWords(s.w.root, "XIM_SERVERS", xproto.AtomAtom, servers...)
}
