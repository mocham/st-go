package x11

import (
	"log"

	"github.com/BurntSushi/xgb"
	"github.com/BurntSushi/xgb/xfixes"
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
		if e.Window == t.win && e.Width > 0 && e.Height > 0 && (int(e.Width) != t.width || int(e.Height) != t.height) {
			t.width, t.height = int(e.Width), int(e.Height)
			c.Resize(t.width, t.height)
		}
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
	case xproto.SelectionClearEvent:
		t.SelectionClear(e)
	case xproto.PropertyNotifyEvent:
		t.PropertyNotify(e)
	case xfixes.SelectionNotifyEvent:
		t.XFixesSelectionNotify(e)
	}
}

func (t *Driver) SelectionClear(event xproto.SelectionClearEvent) {
	if event.Selection == t.clipboardManager {
		t.setClipboardBridge(false)
		return
	}
	selection, ok := t.clipboardByte(event.Selection)
	if !ok {
		return
	}
	if selection == 'c' {
		t.clipboardSet = false
	} else {
		t.primarySet = false
	}
	if !t.xfixesAvailable && t.clipboardBridge {
		if owner, err := xproto.GetSelectionOwner(t.conn, event.Selection).Reply(); err == nil {
			t.clipboardOwners[selection] = owner.Owner
			if owner.Owner != 0 && owner.Owner != t.win {
				t.queueExternalClipboard(selection, owner.Owner)
			}
		}
	}
}
