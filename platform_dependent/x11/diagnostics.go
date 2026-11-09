package x11

import (
	"github.com/BurntSushi/xgb"
	"github.com/BurntSushi/xgb/xproto"
)

// These concrete-backend accessors support the existing X integration tests.
// They are deliberately absent from the uniform api.Host contract.
func (t *Driver) Connection() *xgb.Conn        { return t.conn }
func (t *Driver) WindowID() xproto.Window      { return t.win }
func (t *Driver) Atom(name string) xproto.Atom { return t.atoms[name] }
func Attach(conn *xgb.Conn, window xproto.Window, gc xproto.Gcontext) *Driver {
	return &Driver{conn: conn, win: window, gc: gc, surfaces: make(map[xproto.Window]*Surface)}
}

type PasteState struct {
	Active, Discard bool
	Data            []byte
}

func (t *Driver) PasteState() PasteState { return PasteState{t.incrActive, t.incrDiscard, t.incrData} }
