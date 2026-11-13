package main

import (
	"slices"
	"strings"

	"github.com/jezek/xgb"
	"github.com/jezek/xgb/xproto"
)

var atomNames = []string{
	"_ST_GO_DIRECT_INPUT",
	"_GXW_ST_WORKAREA",
	"UTF8_STRING", "WM_PROTOCOLS", "WM_DELETE_WINDOW", "WM_TAKE_FOCUS", "WM_STATE", "WM_CHANGE_STATE", "WM_TRANSIENT_FOR", "WM_NORMAL_HINTS", "WM_HINTS", "WM_NAME", "WM_CLASS", "MANAGER",
	"_NET_SUPPORTED", "_NET_SUPPORTING_WM_CHECK", "_NET_WM_NAME", "_NET_CLIENT_LIST", "_NET_CLIENT_LIST_STACKING", "_NET_ACTIVE_WINDOW", "_NET_CLOSE_WINDOW",
	"_NET_NUMBER_OF_DESKTOPS", "_NET_CURRENT_DESKTOP", "_NET_DESKTOP_NAMES", "_NET_DESKTOP_GEOMETRY", "_NET_DESKTOP_VIEWPORT", "_NET_WORKAREA", "_NET_WM_DESKTOP",
	"_NET_WM_WINDOW_TYPE", "_NET_WM_WINDOW_TYPE_NORMAL", "_NET_WM_WINDOW_TYPE_DIALOG", "_NET_WM_WINDOW_TYPE_DOCK",
	"_NET_WM_STATE", "_NET_WM_STATE_FULLSCREEN", "_NET_WM_STATE_HIDDEN", "_NET_WM_STATE_MODAL", "_NET_WM_STATE_DEMANDS_ATTENTION", "_NET_WM_STATE_MAXIMIZED_VERT", "_NET_WM_STATE_MAXIMIZED_HORZ",
	"_NET_WM_ALLOWED_ACTIONS", "_NET_WM_ACTION_CLOSE", "_NET_WM_ACTION_FULLSCREEN", "_NET_FRAME_EXTENTS", "_NET_REQUEST_FRAME_EXTENTS", "_NET_WM_STRUT_PARTIAL", "_NET_WM_PID",
}

func (w *wm) intern(name string) (xproto.Atom, error) {
	r, err := xproto.InternAtom(w.conn, false, uint16(len(name)), name).Reply()
	if err != nil {
		return 0, err
	}
	w.atoms[name] = r.Atom
	return r.Atom, nil
}

func (w *wm) property(id xproto.Window, name string) *xproto.GetPropertyReply {
	r, err := xproto.GetProperty(w.conn, false, id, w.atoms[name], xproto.GetPropertyTypeAny, 0, 4096).Reply()
	if err != nil {
		return nil
	}
	return r
}

func (w *wm) words(id xproto.Window, name string) []uint32 {
	r := w.property(id, name)
	if r == nil || r.Format != 32 {
		return nil
	}
	values := make([]uint32, len(r.Value)/4)
	for i := range values {
		values[i] = xgb.Get32(r.Value[i*4:])
	}
	return values
}

func (w *wm) text(id xproto.Window, name string) string {
	r := w.property(id, name)
	if r == nil || r.Format != 8 {
		return ""
	}
	s := strings.ToValidUTF8(string(r.Value), "�")
	return strings.TrimSpace(strings.Map(func(r rune) rune {
		if r < 32 || r == 127 {
			return ' '
		}
		return r
	}, s))
}

func (w *wm) setWords(id xproto.Window, name string, typ xproto.Atom, values ...uint32) {
	b := make([]byte, len(values)*4)
	for i, v := range values {
		xgb.Put32(b[i*4:], v)
	}
	xproto.ChangeProperty(w.conn, xproto.PropModeReplace, id, w.atoms[name], typ, 32, uint32(len(values)), b)
}

func (w *wm) setText(id xproto.Window, name, value string) {
	xproto.ChangeProperty(w.conn, xproto.PropModeReplace, id, w.atoms[name], w.atoms["UTF8_STRING"], 8, uint32(len(value)), []byte(value))
}

func (w *wm) has(values []uint32, name string) bool {
	return slices.Contains(values, uint32(w.atoms[name]))
}

func (w *wm) sendProtocol(c *client, name string, timestamp xproto.Timestamp) {
	e := xproto.ClientMessageEvent{Format: 32, Window: c.id, Type: w.atoms["WM_PROTOCOLS"], Data: xproto.ClientMessageDataUnionData32New([]uint32{uint32(w.atoms[name]), uint32(timestamp), 0, 0, 0})}
	xproto.SendEvent(w.conn, false, c.id, xproto.EventMaskNoEvent, string(e.Bytes()))
}

func (w *wm) closeClient(id xproto.Window) {
	if c := w.clients[id]; c != nil {
		if c.deleteWindow {
			w.sendProtocol(c, "WM_DELETE_WINDOW", w.timestamp)
		} else {
			xproto.KillClient(w.conn, uint32(id))
		}
	}
}

func (w *wm) clientMessage(e xproto.ClientMessageEvent) {
	if e.Format != 32 {
		return
	}
	d := e.Data.Data32
	c := w.clients[e.Window]
	switch e.Type {
	case w.atoms["_NET_ACTIVE_WINDOW"]:
		if c != nil {
			w.activate(c.id)
		}
	case w.atoms["_NET_CLOSE_WINDOW"]:
		w.closeClient(e.Window)
	case w.atoms["_NET_REQUEST_FRAME_EXTENTS"]:
		w.setWords(e.Window, "_NET_FRAME_EXTENTS", xproto.AtomCardinal, 0, 0, 0, 0)
	case w.atoms["_NET_WM_STATE"]:
		if c == nil || d[0] > 2 {
			return
		}
		apply := func(old bool) bool {
			if d[0] == 2 {
				return !old
			}
			return d[0] == 1
		}
		for _, atom := range d[1:3] {
			switch xproto.Atom(atom) {
			case w.atoms["_NET_WM_STATE_FULLSCREEN"]:
				c.fullscreen = apply(c.fullscreen)
			case w.atoms["_NET_WM_STATE_MODAL"]:
				c.modal = apply(c.modal)
			case w.atoms["_NET_WM_STATE_DEMANDS_ATTENTION"]:
				c.urgent = apply(c.urgent)
			}
		}
		w.updateState(c)
		w.layout()
	case w.atoms["WM_CHANGE_STATE"]:
		// There is no minimized-window store: another tab is the hiding action.
		if c != nil && d[0] == 3 && family(c.id, w.clients) == w.active {
			w.cycle(1)
		}
	}
}

func (w *wm) updateState(c *client) {
	var state []uint32
	add := func(yes bool, name string) {
		if yes {
			state = append(state, uint32(w.atoms[name]))
		}
	}
	add(c.fullscreen, "_NET_WM_STATE_FULLSCREEN")
	add(!c.mapped, "_NET_WM_STATE_HIDDEN")
	add(c.modal, "_NET_WM_STATE_MODAL")
	add(c.urgent, "_NET_WM_STATE_DEMANDS_ATTENTION")
	add(!c.dialog, "_NET_WM_STATE_MAXIMIZED_VERT")
	add(!c.dialog, "_NET_WM_STATE_MAXIMIZED_HORZ")
	w.setWords(c.id, "_NET_WM_STATE", xproto.AtomAtom, state...)
	stateID := uint32(1)
	if !c.mapped {
		stateID = 3
	}
	w.setWords(c.id, "WM_STATE", w.atoms["WM_STATE"], stateID, 0)
}
