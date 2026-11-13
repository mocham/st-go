// Package ime adapts readline's Pinyin editor to a stream of application input.
// Owners serialize all calls; Notify only wakes the owner's event loop.
package ime

import (
	"strings"
	"unicode"

	"st-go/tui/readline"
)

type Stream struct {
	Enabled bool
	Notify  func()
	opening string
	line    *readline.Line
}

// Key returns committed text followed by whether the original key was consumed.
// Non-trigger input is returned to the caller unchanged, including native keys.
func (s *Stream) Key(key string) (string, bool) {
	text := s.Poll()
	if !s.Enabled {
		return text, false
	}
	if s.line == nil {
		if key == "[" {
			s.opening += key
			if len(s.opening) == 3 {
				s.opening = ""
				s.line = readline.New("", 0, 4096)
				s.line.IMENotify = s.Notify
				for range 3 {
					s.line.Handle("[")
				}
			}
			return text, true
		}
		text += s.opening
		s.opening = ""
		return text, false
	}
	// Preserve native shortcuts/navigation instead of editing the application's
	// text locally. ESC belongs to the IME, and backspace edits its preedit only.
	escape := key == "\x1b" || key == "<Esc>"
	backspace := key == "\x7f" || key == "\b" || key == "<BS>"
	r := []rune(key)
	if backspace && s.line.Text() == "[[[" {
		return text, true
	}
	if !escape && !backspace && (len(r) != 1 || unicode.IsControl(r[0])) {
		return text + s.Flush(), false
	}
	result := s.line.Handle(key)
	if result.Err != nil && !result.Changed {
		return text + s.Flush(), false
	}
	if !s.line.IMEActive() {
		text += s.Flush()
	}
	return text, true
}

// Poll is nonblocking; Notify makes periodic polling unnecessary.
func (s *Stream) Poll() string {
	if s.line == nil {
		return ""
	}
	s.line.Poll()
	if !s.line.IMEActive() {
		return s.Flush()
	}
	return ""
}

func (s *Stream) Active() bool  { return s.line != nil }
func (s *Stream) Pending() bool { return s.line != nil && s.line.IMESelectionPending() }

func (s *Stream) Display(cells int) string {
	if s.line == nil {
		return s.opening
	}
	v := s.line.ViewDisplay(cells, nil)
	return v.Before + v.Candidates + v.After
}

// Flush preserves unfinished literal input when disabling or leaving a field.
// Trigger delimiters are not application input once composition has started.
func (s *Stream) Flush() string {
	text := s.opening
	s.opening = ""
	if s.line != nil {
		text += strings.TrimPrefix(s.line.Text(), "[[[")
		s.line.Close()
		s.line = nil
	}
	return text
}

func (s *Stream) Close() { s.Flush() }

// KeySym converts unmodified X-style key symbols for both native terminal
// backends and XIM. Empty means an ordinary modifier, which must not break [[.
func KeySym(sym uint32, mods uint16) string {
	if sym >= 0xffe1 && sym <= 0xffee {
		return ""
	}
	if mods & ^uint16(1|2|16) != 0 {
		return "<Shortcut>"
	}
	switch sym {
	case 0xff1b:
		return "\x1b"
	case 0xff08:
		return "\x7f"
	}
	if sym&0xff000000 == 0x01000000 {
		sym &= 0xffffff
	}
	if sym >= 32 && sym <= 0x10ffff && !(sym >= 0xfe00 && sym <= 0xffff) {
		return string(rune(sym))
	}
	return "<Key>"
}
