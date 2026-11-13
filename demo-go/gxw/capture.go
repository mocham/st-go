package main

// Capture is owned by the WM, before application event dispatch. XIM is only
// the text/native-event delivery channel; applications need not forward '['
// for the trigger to be held or for the system bar to show its pending state.
import (
	"fmt"
	"slices"

	"github.com/jezek/xgb/xproto"
	"st-go/tui/ime"
)

type capturedKey struct {
	event   xproto.KeyPressEvent
	release bool
}

type keyboardCapture struct {
	stream         ime.Stream
	context        *ximContext
	focus          xproto.Window
	queue          []capturedKey
	down, consumed [256]bool
}

// Synchronous activation prevents a fast second key from escaping while the
// WM promotes the passive grab to an explicit grab that survives key release.
// Keep the passive grab while disabled: ReplayKeyboard passes it through, and
// a button click followed immediately by '[' cannot race grab installation.
func (s *ximServer) grabTriggers() error {
	for code, syms := range s.keysyms {
		for level := 0; level < min(2, len(syms)); level++ {
			if syms[level] != '[' {
				continue
			}
			mods := uint16(0)
			if level == 1 {
				mods = xproto.ModMaskShift
			}
			for ignore := uint16(0); ignore < 256; ignore++ {
				if ignore & ^s.w.ignoredMods != 0 {
					continue
				}
				if err := xproto.GrabKeyChecked(s.w.conn, false, s.w.root, mods|ignore, xproto.Keycode(code), xproto.GrabModeAsync, xproto.GrabModeSync).Check(); err != nil {
					return fmt.Errorf("grab [: %w", err)
				}
			}
		}
	}
	return nil
}

func (s *ximServer) keyName(code byte, mods uint16) string {
	syms := s.keysyms[code]
	var sym uint32
	if len(syms) > 0 {
		sym = uint32(syms[0])
		if mods&xproto.ModMaskShift != 0 {
			if len(syms) > 1 && syms[1] != 0 {
				sym = uint32(syms[1])
			} else if sym >= 'a' && sym <= 'z' {
				sym -= 32
			}
		}
		if mods&xproto.ModMaskLock != 0 {
			if sym >= 'a' && sym <= 'z' {
				sym -= 32
			} else if sym >= 'A' && sym <= 'Z' {
				sym += 32
			}
		}
	}
	return ime.KeySym(sym, mods & ^s.w.ignoredMods)
}

func (s *ximServer) captureKey(key capturedKey) bool {
	if s.capture != nil && !key.release && s.w.ownsKey(key.event) {
		// Active keyboard grabs also receive WM shortcuts. Flush back to the
		// original application before changing tabs or running a system action.
		s.flushCapture()
		s.w.timestamp = key.event.Time
		s.w.key(key.event)
		return true
	}
	if s.capture == nil {
		if key.release || s.keyName(byte(key.event.Detail), key.event.State) != "[" {
			return false
		}
		if !s.enabled {
			xproto.AllowEvents(s.w.conn, xproto.AllowReplayKeyboard, xproto.TimeCurrentTime)
			return true
		}
		focus, err := xproto.GetInputFocus(s.w.conn).Reply()
		if err != nil || focus.Focus <= 1 || focus.Focus == s.w.root || s.directInput(focus.Focus) {
			xproto.AllowEvents(s.w.conn, xproto.AllowReplayKeyboard, xproto.TimeCurrentTime)
			return true
		}
		r, err := xproto.GrabKeyboard(s.w.conn, false, s.w.root, xproto.TimeCurrentTime, xproto.GrabModeAsync, xproto.GrabModeSync).Reply()
		if err != nil || r.Status != xproto.GrabStatusSuccess {
			xproto.AllowEvents(s.w.conn, xproto.AllowReplayKeyboard, key.event.Time)
			return true
		}
		c := s.focused
		if c != nil && !s.contextAt(c, focus.Focus) {
			c = nil
		}
		s.capture = &keyboardCapture{focus: focus.Focus, context: c, stream: ime.Stream{Enabled: true, Notify: func() {
			select {
			case s.wake <- struct{}{}:
			default:
			}
		}}}
		s.capture.stream.Key("[")
		s.capture.down[byte(key.event.Detail)], s.capture.consumed[byte(key.event.Detail)] = true, true
		s.draw()
		if c == nil {
			// GTK creates its text context lazily on the first delivered key.
			// A modifier pair wakes that path without leaking the held bracket.
			for code, syms := range s.keysyms {
				if len(syms) > 0 && syms[0] == 0xffe1 {
					e := xproto.KeyPressEvent{Detail: xproto.Keycode(code), Time: key.event.Time}
					s.nativeKey(capturedKey{event: e})
					e.State = xproto.ModMaskShift
					s.nativeKey(capturedKey{event: e, release: true})
					break
				}
			}
		}
		if c != nil {
			// Barrier behind input already delivered to this application. In
			// particular a preceding ordinary key must stay ahead of our commit.
			c.p.sendInput(61, c.p.ids(c.id))
		}
		s.drainCapture()
		return true
	}
	c := s.capture
	c.queue = append(c.queue, key)
	s.drainCapture()
	return true
}

func (s *ximServer) directInput(window xproto.Window) bool {
	for depth := 0; depth < 32 && window > 1 && window != s.w.root; depth++ {
		if value := s.w.words(window, "_ST_GO_DIRECT_INPUT"); len(value) > 0 && value[0] != 0 {
			return true
		}
		r, err := xproto.QueryTree(s.w.conn, window).Reply()
		if err != nil {
			return false
		}
		window = r.Parent
	}
	return false
}

func (s *ximServer) contextAt(c *ximContext, focus xproto.Window) bool {
	for _, id := range []uint16{3, 2} {
		if b := c.attrs[id]; len(b) == 4 && xproto.Window(c.p.order.Uint32(b)) == focus {
			return true
		}
	}
	// GTK can focus a descendant of the IC's client window.
	for depth := 0; depth < 32 && focus != 0 && focus != s.w.root; depth++ {
		r, err := xproto.QueryTree(s.w.conn, focus).Reply()
		if err != nil {
			return false
		}
		focus = r.Parent
		if b := c.attrs[2]; len(b) == 4 && xproto.Window(c.p.order.Uint32(b)) == focus {
			return true
		}
	}
	return false
}

func (s *ximServer) drainCapture() {
	c := s.capture
	if c == nil || s.pastePending {
		return
	}
	if c.context != nil && c.context.p.waiting {
		return
	}
	s.commitCapture(c.stream.Poll())
	for len(c.queue) > 0 && !c.stream.Pending() {
		if s.pastePending || c.context != nil && c.context.p.waiting {
			break
		}
		k := c.queue[0]
		c.queue = c.queue[1:]
		code := byte(k.event.Detail)
		c.down[code] = !k.release
		consumed := false
		if k.release {
			consumed = c.consumed[code]
			c.consumed[code] = false
		} else {
			name := s.keyName(code, k.event.State)
			if name != "" {
				text, used := c.stream.Key(name)
				consumed = used
				s.commitCapture(text)
			}
			c.consumed[code] = consumed
		}
		if !consumed {
			s.nativeKey(k)
			if c.context != nil {
				// Deliver through the real focus window (Chromium's IC can use
				// a hidden helper window). Keep the next commit behind this key.
				c.context.p.sendInput(61, c.context.p.ids(c.context.id))
			}
		}
	}
	s.draw()
	if !s.pastePending && len(c.queue) == 0 && !c.stream.Active() && c.stream.Display(1) == "" && !slices.Contains(c.down[:], true) && (c.context == nil || !c.context.p.waiting) {
		s.endCapture()
	} else if !s.pastePending && !c.stream.Pending() && (c.context == nil || !c.context.p.waiting) {
		xproto.AllowEvents(s.w.conn, xproto.AllowSyncKeyboard, xproto.TimeCurrentTime)
	}
}

func (s *ximServer) commitCapture(text string) {
	if text == "" || s.capture == nil {
		return
	}
	if c := s.capture.context; c != nil {
		c.commit(text)
		return
	}
	// Without a text context, recover literal brackets as native keystrokes.
	// Composition commits use the clipboard delivery path.
	if text == "[" || text == "[[" {
		for range len(text) {
			for code, syms := range s.keysyms {
				level := slices.Index(syms[:min(2, len(syms))], xproto.Keysym('['))
				if level >= 0 {
					e := xproto.KeyPressEvent{Detail: xproto.Keycode(code)}
					if level == 1 {
						e.State = xproto.ModMaskShift
					}
					s.nativeKey(capturedKey{event: e})
					s.nativeKey(capturedKey{event: e, release: true})
					break
				}
			}
		}
		return
	}
	s.pasteText(text)
}

func (s *ximServer) endCapture() {
	c := s.capture
	if c == nil {
		return
	}
	s.capture = nil
	s.finishPaste()
	c.stream.Close()
	xproto.UngrabKeyboard(s.w.conn, xproto.TimeCurrentTime)
	s.draw()
}

func (s *ximServer) flushCapture() {
	if s.capture != nil {
		s.commitCapture(s.capture.stream.Flush())
		s.endCapture()
	}
}

func (s *ximServer) nativeKey(k capturedKey) {
	e := k.event
	e.Event = s.capture.focus
	e.Root = s.w.root
	e.Child = 0
	e.SameScreen = true
	data := e.Bytes()
	mask := uint32(xproto.EventMaskKeyPress)
	if k.release {
		data[0] = xproto.KeyRelease
		mask = xproto.EventMaskKeyRelease
	}
	xproto.SendEvent(s.w.conn, false, e.Event, mask, string(data))
}

func (s *ximServer) captureLabel() string {
	if !s.enabled {
		return "IME OFF"
	}
	if c := s.capture; c != nil {
		if c.stream.Active() {
			return "CJK"
		}
		if text := c.stream.Display(2); text != "" {
			return "IME " + text
		}
	}
	return "IME ON"
}
