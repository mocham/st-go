package frontend

import (
	"sync/atomic"

	"st-go/platform_neutral/api"
	"st-go/tui/ime"
)

type terminalIME struct {
	stream *ime.Stream
	view   atomic.Pointer[imeView]
	queued []imeInput
}

// Published under Terminal.mu; the tab bar reads immutable snapshots in the
// window event domain, without taking a terminal lock while painting.
type imeView struct {
	enabled bool
	text    string
}

type imeInput struct {
	key   uint
	mods  uint16
	text  string
	paste bool
}

func (t *Terminal) deferIME(input imeInput) bool {
	s := t.inputMethod.stream
	if s == nil || !s.Pending() {
		return false
	}
	if len(t.inputMethod.queued) >= 4096 {
		t.flushIME()
		return false
	}
	t.inputMethod.queued = append(t.inputMethod.queued, input)
	return true
}

func (t *Terminal) drainIME() {
	for len(t.inputMethod.queued) > 0 && !t.inputMethod.stream.Pending() {
		input := t.inputMethod.queued[0]
		t.inputMethod.queued = t.inputMethod.queued[1:]
		switch {
		case input.paste:
			t.pasteToTTY(input.text)
		case input.text != "":
			t.textInput(input.text)
		default:
			t.keyPress(input.key, input.mods)
		}
	}
}

func (t *Terminal) flushIME() {
	s := t.inputMethod.stream
	if s == nil {
		return
	}
	t.imeCommit(s.Flush())
	enabled := s.Enabled
	s.Enabled = false
	t.drainIME()
	s.Enabled = enabled
}

func (t *Terminal) initIME() {
	enabled := true
	if host, ok := t.host.(api.BackendIdentity); ok {
		if host.BackendName() == "web" {
			return
		}
		enabled = host.BackendName() != "fb"
	}
	t.inputMethod.stream = &ime.Stream{Enabled: enabled, Notify: t.wakeEvents}
	t.paintIME()
}

func (t *Terminal) imeCommit(text string) {
	if text != "" {
		t.termCore.WriteToTTY([]byte(text), true)
	}
}

func (t *Terminal) toggleIME() {
	s := t.inputMethod.stream
	if s == nil {
		return
	}
	t.flushIME()
	s.Enabled = !s.Enabled
	t.paintIME()
}

func (t *Terminal) imeKey(key uint, mods uint16) bool {
	s := t.inputMethod.stream
	if s == nil {
		return false
	}
	name := ime.KeySym(uint32(key), mods&^uint16(t.ignoreMod))
	if name == "" {
		return false
	}
	text, consumed := s.Key(name)
	t.imeCommit(text)
	t.paintIME()
	return consumed
}

func (t *Terminal) pollIME() {
	if s := t.inputMethod.stream; s != nil {
		t.imeCommit(s.Poll())
		t.drainIME()
		t.paintIME()
	}
}

func (t *Terminal) paintIME() {
	b := &t.inputMethod
	if b.stream == nil {
		return
	}
	v := imeView{enabled: b.stream.Enabled, text: b.stream.Display(max(1, t.fbW/max(1, t.cw)-10))}
	if old := b.view.Load(); old != nil && *old == v {
		return
	}
	b.view.Store(&v)
	if t.tabs != nil {
		t.tabs.imeChanged(t.Widget().ID())
	}
}

func (t *Terminal) closeIME() {
	if t.inputMethod.stream != nil {
		t.inputMethod.stream.Close()
	}
}
