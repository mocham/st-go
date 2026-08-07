package main

import (
	"log"
	"time"

	"github.com/BurntSushi/xgb"
	"github.com/BurntSushi/xgb/xproto"

	"st-go/term"
)

// run is the main event loop, mirroring st's run().
func (t *Terminal) run(termCore *term.Term) {
	// keyboard mapping setup
	t.setupKeys()

	t.termCore = termCore

	// cursor blink timer
	if t.blinkMs > 0 {
		go func() {
			for {
				time.Sleep(time.Duration(t.blinkMs) * time.Millisecond)
				t.mu.Lock()
				if t.termCore != nil && t.termCore.Tattrset(term.ATTRBlink) {
					t.toggleBlink()
					t.termCore.Redraw()
				}
				t.mu.Unlock()
			}
		}()
	}

	for {
		ev, err := t.conn.WaitForEvent()
		if err != nil {
			log.Printf("x: %v", err)
			continue
		}
		if ev == nil {
			continue
		}
		t.mu.Lock()
		t.handleEvent(ev)
		t.mu.Unlock()
	}
}

// toggleBlink flips the blink window mode so glyphs render on/off.
func (t *Terminal) toggleBlink() {
	if t.winMode&term.ModeBlink != 0 {
		t.winMode &^= term.ModeBlink
	} else {
		t.winMode |= term.ModeBlink
	}
}

func (t *Terminal) handleEvent(ev xgb.Event) {
	switch e := ev.(type) {
	case xproto.ExposeEvent:
		if e.Count == 0 {
			t.termCore.Redraw()
		}
	case xproto.KeyPressEvent:
		t.kpress(e)
	case xproto.KeyReleaseEvent:
		// ignore
	case xproto.ButtonPressEvent:
		t.bpress(e)
	case xproto.ButtonReleaseEvent:
		t.brelease(e)
	case xproto.MotionNotifyEvent:
		t.bmotion(e)
	case xproto.ConfigureNotifyEvent:
		t.resize(int(e.Width), int(e.Height))
	case xproto.FocusInEvent:
		t.setWinMode(true, term.ModeFocused)
		t.termCore.Redraw()
	case xproto.FocusOutEvent:
		t.setWinMode(false, term.ModeFocused)
		t.termCore.Redraw()
	case xproto.ClientMessageEvent:
		if e.Type == t.atoms["WM_PROTOCOLS"] &&
			xproto.Atom(e.Data.Data32[0]) == t.atoms["WM_DELETE_WINDOW"] {
			t.Close()
			return
		}
	case xproto.SelectionRequestEvent:
		t.selrequest(e)
	case xproto.SelectionNotifyEvent:
		t.selnotify(e)
	case xproto.PropertyNotifyEvent:
		t.propnotify(e)
	}
}

// resize recomputes cols/rows and resizes the terminal.
func (t *Terminal) resize(w, h int) {
	newCols := (w - 2*t.borderpx) / t.cw
	newRows := (h - 2*t.borderpx) / t.ch
	if newCols < 1 {
		newCols = 1
	}
	if newRows < 1 {
		newRows = 1
	}
	if newCols == t.cols && newRows == t.rows && w == fbW && h == fbH {
		return
	}
	t.cols, t.rows = newCols, newRows
	ensureFramebuffer(w, h)
	t.termCore.Tresize(newCols, newRows)
	// propagate the new size to the pty so the app (vim, etc.) sees it
	if t.ttyResize != nil {
		t.ttyResize(newRows, newCols)
	}
	t.termCore.Redraw()
}
