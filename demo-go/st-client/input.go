package main

import (
	"bytes"
	"fmt"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

type localEvent struct {
	input  inputMessage
	reply  string
	escape bool
}
type inputParser struct {
	pending, paste        []byte
	inPaste, discardPaste bool
	since                 time.Time
}

func (p *inputParser) feed(data []byte, now time.Time, emit func(localEvent)) {
	if len(p.pending) == 0 {
		p.since = now
	}
	p.pending = append(p.pending, data...)
	for len(p.pending) > 0 {
		b := p.pending
		if p.inPaste {
			end := bytes.Index(b, []byte("\x1b[201~"))
			n := end
			if n < 0 {
				n = max(0, len(b)-5)
			}
			if !p.discardPaste {
				if len(p.paste)+n > 8<<20 {
					p.discardPaste = true
					p.paste = nil
				} else {
					p.paste = append(p.paste, b[:n]...)
				}
			}
			p.pending = b[n:]
			if end < 0 {
				break
			}
			p.pending = p.pending[6:]
			p.inPaste = false
			if !p.discardPaste && utf8.Valid(p.paste) {
				emit(localEvent{input: inputMessage{Type: "paste", Text: string(p.paste)}})
			}
			p.paste = nil
			p.discardPaste = false
			continue
		}
		if b[0] != 27 {
			if !utf8.FullRune(b) {
				break
			}
			r, n := utf8.DecodeRune(b)
			p.pending = b[n:]
			if r == 0x1d {
				emit(localEvent{escape: true})
				continue
			}
			emit(localEvent{input: keyRune(r, 0)})
			continue
		}
		if len(b) < 2 {
			break
		}
		if b[1] == 'P' || b[1] == ']' {
			end := bytes.Index(b[2:], []byte("\x1b\\"))
			skip := 2
			if b[1] == ']' {
				if bell := bytes.IndexByte(b[2:], 7); bell >= 0 && (end < 0 || bell < end) {
					end = bell
					skip = 1
				}
			}
			if end < 0 {
				break
			}
			emit(localEvent{reply: string(b[2 : 2+end])})
			p.pending = b[2+end+skip:]
			continue
		}
		if b[1] == '[' || b[1] == 'O' {
			end := 2
			for end < len(b) && (b[end] < 0x40 || b[end] > 0x7e) {
				end++
			}
			if end == len(b) {
				break
			}
			sequence := string(b[2 : end+1])
			p.pending = b[end+1:]
			if sequence == "200~" {
				p.inPaste = true
				continue
			}
			if sequence == "I" || sequence == "O" {
				emit(localEvent{input: inputMessage{Type: "focus", Focused: sequence == "I"}})
				continue
			}
			if strings.HasPrefix(sequence, "<") {
				if m, ok := mouseSequence(sequence); ok {
					emit(localEvent{input: m})
					if m.Type == "press" && m.Button >= 4 && m.Button <= 7 {
						m.Type = "release"
						emit(localEvent{input: m})
					}
				}
				continue
			}
			if m, ok := keySequence(sequence); ok {
				emit(localEvent{input: m})
			}
			continue
		}
		if !utf8.FullRune(b[1:]) {
			break
		}
		r, n := utf8.DecodeRune(b[1:])
		p.pending = b[1+n:]
		emit(localEvent{input: keyRune(r, 8)})
	}
	if len(p.pending) > 128<<10 {
		p.pending = nil
	} // bound malformed local replies
}

func keyRune(r rune, mods uint16) inputMessage {
	m := inputMessage{Type: "key", Modifiers: mods}
	switch r {
	case '\r', '\n':
		m.Key = "Enter"
	case '\t':
		m.Key = "Tab"
	case 0x7f, 8:
		m.Key = "Backspace"
	case 27:
		m.Key = "Escape"
	case 0:
		m.Key = " "
		m.Modifiers |= 4
	default:
		if r < 32 {
			m.Key = string(r + 64)
			m.Modifiers |= 4
		} else if r < 127 {
			m.Key = string(r)
		} else {
			m.Type = "text"
			m.Text = string(r)
		}
	}
	return m
}

func keySequence(sequence string) (inputMessage, bool) {
	m := inputMessage{Type: "key"}
	if len(sequence) == 0 {
		return m, false
	}
	final := sequence[len(sequence)-1]
	parts := strings.Split(sequence[:len(sequence)-1], ";")
	if len(parts) > 1 {
		n, err := strconv.Atoi(parts[len(parts)-1])
		if err != nil || n < 1 || n > 16 {
			return m, false
		}
		n--
		if n&1 != 0 {
			m.Modifiers |= 1
		}
		if n&2 != 0 {
			m.Modifiers |= 8
		}
		if n&4 != 0 {
			m.Modifiers |= 4
		}
		if n&8 != 0 {
			m.Modifiers |= 64
		}
	}
	m.Key = map[byte]string{'A': "ArrowUp", 'B': "ArrowDown", 'C': "ArrowRight", 'D': "ArrowLeft", 'H': "Home", 'F': "End", 'P': "F1", 'Q': "F2", 'R': "F3", 'S': "F4", 'Z': "Tab"}[final]
	if final == 'Z' {
		m.Modifiers |= 1
	}
	if final == '~' {
		n, _ := strconv.Atoi(parts[0])
		m.Key = map[int]string{1: "Home", 2: "Insert", 3: "Delete", 4: "End", 5: "PageUp", 6: "PageDown", 7: "Home", 8: "End", 11: "F1", 12: "F2", 13: "F3", 14: "F4", 15: "F5", 17: "F6", 18: "F7", 19: "F8", 20: "F9", 21: "F10", 23: "F11", 24: "F12"}[n]
	}
	return m, m.Key != ""
}

func mouseSequence(sequence string) (inputMessage, bool) {
	var code, x, y int
	var final rune
	if _, err := fmt.Sscanf(sequence, "<%d;%d;%d%c", &code, &x, &y, &final); err != nil || x < 1 || y < 1 || x > 65535 || y > 65535 || code < 0 || code > 255 || (final != 'M' && final != 'm') {
		return inputMessage{}, false
	}
	m := inputMessage{Type: "press", X: x, Y: y, Button: (code & 3) + 1}
	if code&4 != 0 {
		m.Modifiers |= 1
	}
	if code&8 != 0 {
		m.Modifiers |= 8
	}
	if code&16 != 0 {
		m.Modifiers |= 4
	}
	if code&64 != 0 {
		m.Button = 4 + (code & 3)
	} else if code&128 != 0 {
		m.Button = 8 + (code & 3)
	}
	if code&32 != 0 {
		m.Type = "motion"
		m.Button = 0
	} else if final == 'm' {
		m.Type = "release"
	}
	return m, true
}

func (p *inputParser) deadline() time.Time {
	if len(p.pending) == 0 || p.inPaste {
		return time.Time{}
	}
	return p.since.Add(150 * time.Millisecond)
}
func (p *inputParser) expire(now time.Time, emit func(localEvent)) {
	if at := p.deadline(); at.IsZero() || now.Before(at) {
		return
	}
	if string(p.pending) == "\x1b" {
		emit(localEvent{input: inputMessage{Type: "key", Key: "Escape"}})
	}
	p.pending = nil
}
