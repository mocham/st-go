package x11

import (
	"log"

	"github.com/BurntSushi/xgb"
	"github.com/BurntSushi/xgb/xproto"
	"st-go/platform_neutral/api"
)

func (t *Driver) Bind(callbacks api.Callbacks) { t.callbacks = callbacks }

// Run retains X's own event representation and dispatch loop. Only discrete
// terminal callbacks cross into the neutral host, under its existing lock.
func (t *Driver) Run() {
	t.SetupKeys()
	for {
		ev, err := t.conn.WaitForEvent()
		if err != nil {
			if t.closed() {
				return
			}
			log.Printf("x: %v", err)
			continue
		}
		if ev == nil {
			if t.closed() {
				return
			}
			continue
		}
		t.callbacks.Lock()
		if t.callbacks.Closed() {
			t.callbacks.Unlock()
			return
		}
		t.Dispatch(ev)
		t.callbacks.PaintDirty()
		t.callbacks.Unlock()
	}
}

// Dispatch is also used by native integration tests to replay X events.
func (t *Driver) Dispatch(ev xgb.Event) {
	c := t.callbacks
	switch e := ev.(type) {
	case xproto.ExposeEvent:
		if e.Count != 0 {
			return
		}
		if e.Window != 0 && e.Window != t.win {
			var surface api.Surface
			t.surfaceMu.RLock()
			if s := t.surfaces[e.Window]; s != nil {
				surface = s
			}
			t.surfaceMu.RUnlock()
			c.ExposeSurface(surface)
		} else {
			c.Expose()
		}
	case xproto.KeyPressEvent:
		c.KeyPress(t.keysymForEvent(e.Detail, e.State), e.State)
	case xproto.ButtonPressEvent:
		c.PointerPress(byte(e.Detail), e.State, int(e.EventX), int(e.EventY))
	case xproto.ButtonReleaseEvent:
		c.PointerRelease(byte(e.Detail), e.State, int(e.EventX), int(e.EventY))
	case xproto.MotionNotifyEvent:
		c.PointerMotion(e.State, int(e.EventX), int(e.EventY))
	case xproto.ConfigureNotifyEvent:
		c.Resize(int(e.Width), int(e.Height))
	case xproto.FocusInEvent:
		if e.Mode != 1 && e.Mode != 2 {
			c.Focus(true)
		}
	case xproto.FocusOutEvent:
		if e.Mode != 1 && e.Mode != 2 {
			c.Focus(false)
		}
	case xproto.ClientMessageEvent:
		if e.Type == t.atoms["WM_PROTOCOLS"] && xproto.Atom(e.Data.Data32[0]) == t.atoms["WM_DELETE_WINDOW"] {
			c.Close()
		}
	case xproto.SelectionRequestEvent:
		t.SelectionRequest(e)
	case xproto.SelectionNotifyEvent:
		t.SelectionNotify(e)
	case xproto.PropertyNotifyEvent:
		t.PropertyNotify(e)
	}
}
