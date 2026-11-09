package main

import (
	"bytes"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

type mouseEvent struct {
	button, x, y int
	release      bool
}
type keyEvent struct {
	Key       string
	Paste     string
	Mouse     *mouseEvent
	Err       string
	Clipboard *clipboardReply
	Preview   *previewEvent
}
type inputParser struct {
	pending, paste                        []byte
	since                                 time.Time
	inPaste, discardPaste, discardControl bool
	osc                                   []byte
	inOSC, oscEscape, discardOSC          bool
	dcs                                   []byte
	inDCS, dcsEscape, discardDCS          bool
}

func (p *inputParser) feed(data []byte, now time.Time, emit func(keyEvent)) {
	if len(p.pending) == 0 {
		p.since = now
	}
	p.pending = append(p.pending, data...)
	for len(p.pending) > 0 {
		if p.inDCS {
			b := p.pending[0]
			p.pending = p.pending[1:]
			if b == 0x18 || b == 0x1a || b == 7 || p.dcsEscape && b == '\\' {
				if b == '\\' && !p.discardDCS {
					if event := parsePreviewEvent(string(p.dcs)); event != nil {
						emit(keyEvent{Preview: event})
					}
				}
				p.dcs = nil
				p.inDCS, p.dcsEscape, p.discardDCS = false, false, false
				continue
			}
			if p.dcsEscape && !p.discardDCS {
				p.dcs = append(p.dcs, 0x1b)
			}
			p.dcsEscape = b == 0x1b
			if !p.dcsEscape && !p.discardDCS {
				if len(p.dcs) >= 8192 {
					p.dcs = nil
					p.discardDCS = true
				} else {
					p.dcs = append(p.dcs, b)
				}
			}
			continue
		}
		if p.inOSC {
			b := p.pending[0]
			p.pending = p.pending[1:]
			if b == 7 || (p.oscEscape && b == '\\') {
				if !p.discardOSC {
					if reply := parseClipboardReply(p.osc); reply != nil {
						emit(keyEvent{Clipboard: reply})
					}
				}
				p.osc = nil
				p.inOSC, p.oscEscape, p.discardOSC = false, false, false
				continue
			}
			if p.oscEscape && !p.discardOSC {
				p.osc = append(p.osc, 0x1b)
			}
			p.oscEscape = b == 0x1b
			if !p.oscEscape && !p.discardOSC {
				maximum := 8192
				if bytes.HasPrefix(p.osc, []byte("52;")) {
					maximum = maxClipboardControl
				}
				if len(p.osc) >= maximum {
					p.osc = nil
					p.discardOSC = true
				} else {
					p.osc = append(p.osc, b)
				}
			}
			continue
		}
		if p.inPaste {
			end := bytes.Index(p.pending, []byte("\x1b[201~"))
			if end < 0 {
				keep := min(5, len(p.pending))
				p.appendPaste(p.pending[:len(p.pending)-keep])
				p.pending = append([]byte(nil), p.pending[len(p.pending)-keep:]...)
				return
			}
			p.appendPaste(p.pending[:end])
			p.pending = p.pending[end+6:]
			if p.discardPaste {
				emit(keyEvent{Err: "paste exceeds 16 MiB limit; paste discarded"})
			} else if !utf8.Valid(p.paste) {
				emit(keyEvent{Err: "paste is not valid UTF-8; paste discarded"})
			} else {
				emit(keyEvent{Key: "<Paste>", Paste: string(p.paste)})
			}
			p.inPaste, p.discardPaste, p.paste = false, false, nil
			continue
		}
		b := p.pending
		if p.discardControl {
			at := 0
			for at < len(b) && (b[at] < 0x40 || b[at] > 0x7e) {
				at++
			}
			if at == len(b) {
				p.pending = nil
				return
			}
			p.discardControl = false
			p.pending = b[at+1:]
			continue
		}
		if b[0] != 0x1b {
			if !utf8.FullRune(b) {
				return
			}
			r, n := utf8.DecodeRune(b)
			key := string(r)
			switch r {
			case 0x7f:
				key = "<BS>"
			case '\r', '\n':
				key = "<Enter>"
			case '\t':
				key = "<Tab>"
			}
			emit(keyEvent{Key: key})
			p.pending = b[n:]
			continue
		}
		if len(b) < 2 {
			return
		}
		if key := map[byte]string{'b': "<A-b>", 'f': "<A-f>", 'd': "<A-d>", 0x7f: "<A-BS>", 0x08: "<A-BS>"}[b[1]]; key != "" {
			emit(keyEvent{Key: key})
			p.pending = b[2:]
			continue
		}
		if b[1] == ']' {
			p.pending = b[2:]
			p.inOSC = true
			continue
		}
		if b[1] == 'P' {
			p.pending = b[2:]
			p.inDCS = true
			continue
		}
		if b[1] == '[' || b[1] == 'O' {
			if bytes.HasPrefix(b, []byte("\x1b[200~")) {
				p.pending = b[6:]
				p.inPaste = true
				continue
			}
			if len(b) >= 3 && b[1] == '[' && b[2] == 'M' {
				if len(b) < 6 {
					return
				}
				emit(keyEvent{Mouse: &mouseEvent{button: int(b[3]) - 32, x: int(b[4]) - 32, y: int(b[5]) - 32}})
				p.pending = b[6:]
				continue
			}
			end := 2
			for end < len(b) && (b[end] < 0x40 || b[end] > 0x7e) {
				end++
			}
			if end == len(b) {
				if len(b) > 128 {
					p.pending = nil
					p.discardControl = true
				}
				return
			}
			sequence := string(b[1 : end+1])
			p.pending = b[end+1:]
			if strings.HasPrefix(sequence, "[<") && (strings.HasSuffix(sequence, "M") || strings.HasSuffix(sequence, "m")) {
				parts := strings.Split(sequence[2:len(sequence)-1], ";")
				if len(parts) == 3 {
					button, e1 := strconv.Atoi(parts[0])
					x, e2 := strconv.Atoi(parts[1])
					y, e3 := strconv.Atoi(parts[2])
					if e1 == nil && e2 == nil && e3 == nil && x > 0 && y > 0 {
						emit(keyEvent{Mouse: &mouseEvent{button, x, y, strings.HasSuffix(sequence, "m")}})
					}
				}
				continue
			}
			if key := map[string]string{"[A": "<Up>", "[B": "<Down>", "[C": "<Right>", "[D": "<Left>", "OA": "<Up>", "OB": "<Down>", "OC": "<Right>", "OD": "<Left>", "[H": "<Home>", "[F": "<End>", "OH": "<Home>", "OF": "<End>", "[1~": "<Home>", "[4~": "<End>", "[7~": "<Home>", "[8~": "<End>", "[2~": "<Insert>", "[3~": "<Delete>", "[5~": "<PageUp>", "[6~": "<PageDown>", "[Z": "<S-Tab>", "[15~": "<F5>", "[15;5~": "<C-F5>", "[1;5D": "<C-Left>", "[1;5C": "<C-Right>"}[sequence]; key != "" {
				emit(keyEvent{Key: key})
			}
			continue
		}
		emit(keyEvent{Key: "<Esc>"})
		p.pending = b[1:]
	}
}
func (p *inputParser) appendPaste(data []byte) {
	if p.discardPaste {
		return
	}
	if len(p.paste)+len(data) > maxBufferBytes {
		p.paste = nil
		p.discardPaste = true
		return
	}
	p.paste = append(p.paste, data...)
}
func (p *inputParser) expire(now time.Time, emit func(keyEvent)) {
	if p.inPaste || len(p.pending) == 0 || now.Sub(p.since) < 75*time.Millisecond {
		return
	}
	if string(p.pending) == "\x1b" {
		emit(keyEvent{Key: "<Esc>"})
	}
	p.pending = nil
	p.discardControl = false
}
func eventsText(text string) []keyEvent {
	var events []keyEvent
	for _, r := range text {
		key := string(r)
		switch r {
		case 0x1b:
			key = "<Esc>"
		case '\r', '\n':
			key = "<Enter>"
		case '\t':
			key = "<Tab>"
		case 0x7f:
			key = "<BS>"
		}
		events = append(events, keyEvent{Key: key})
	}
	return events
}
func keysText(events []keyEvent) string {
	var text strings.Builder
	for _, event := range events {
		if event.Key == "<Paste>" {
			text.WriteString(event.Paste)
			continue
		}
		switch event.Key {
		case "<Esc>":
			text.WriteByte(0x1b)
		case "<Enter>":
			text.WriteByte('\r')
		case "<Tab>":
			text.WriteByte('\t')
		case "<BS>":
			text.WriteByte(0x7f)
		default:
			text.WriteString(event.Key)
		}
	}
	return text.String()
}
func parseKeyNotation(text string) []keyEvent {
	var events []keyEvent
	for len(text) > 0 {
		if text[0] == '<' {
			if end := strings.IndexByte(text, '>'); end > 0 {
				key := text[:end+1]
				if len(key) == 5 && strings.HasPrefix(key, "<C-") {
					c := key[3]
					if c >= 'a' && c <= 'z' {
						c -= 32
					}
					events = append(events, keyEvent{Key: string([]byte{c & 31})})
				} else if key == "<CR>" {
					events = append(events, keyEvent{Key: "<Enter>"})
				} else {
					events = append(events, keyEvent{Key: key})
				}
				text = text[end+1:]
				continue
			}
		}
		r, n := utf8.DecodeRuneInString(text)
		events = append(events, eventsText(string(r))...)
		text = text[n:]
	}
	return events
}
func (e *Editor) mouse(event mouseEvent) {
	if e.prompt != "" || e.substitute != nil {
		return
	}
	var target *Window
	for _, w := range e.windows() {
		if event.x-1 >= w.rect.x && event.x-1 < w.rect.x+w.rect.w && event.y-1 >= w.rect.y && event.y-1 < w.rect.y+w.rect.h {
			target = w
			break
		}
	}
	if target == nil {
		return
	}
	e.tabs[e.activeTab].active = target
	if event.button&64 != 0 {
		delta := 3
		if event.button&1 == 0 {
			delta = -3
		}
		e.scroll(delta, false)
		e.dirty = true
		return
	}
	row := event.y - 1 - target.rect.y
	if row < 0 || row >= len(target.rows) {
		return
	}
	screen := target.rows[row]
	if screen.line < 0 {
		return
	}
	column := max(0, event.x-1-target.rect.x-numberWidth(target.buffer)-1)
	pos := screen.end
	for _, cell := range screen.cells {
		if column < cell.column+cell.width {
			pos = cell.pos
			break
		}
	}
	pos = target.buffer.normal(pos)
	if event.button&3 == 1 && !event.release {
		e.withRegister('*', func(value register) { e.setPos(pos); e.put(value, true, 1) })
		return
	}
	if event.button&32 != 0 {
		if e.mode != visualMode {
			e.anchor = e.clickAnchor
			e.mode = visualMode
		}
		e.setPos(pos)
		return
	}
	if event.release {
		return
	}
	if event.button&3 == 2 {
		if !e.visual() {
			e.anchor = e.pos()
			e.mode = visualMode
		}
		e.setPos(pos)
		return
	}
	if event.button&3 != 0 {
		return
	}
	if e.mode == insertMode || e.mode == replaceMode {
		e.finishInsert()
	}
	e.mode = normalMode
	e.setPos(pos)
	e.clickAnchor = pos
	if time.Since(e.lastClick) < 350*time.Millisecond {
		e.clickCount++
	} else {
		e.clickCount = 1
	}
	e.lastClick = time.Now()
	if e.clickCount == 2 {
		start, end, _, ok := e.textObject("w", false, 1)
		if ok {
			e.anchor = start
			e.setPos(max(start, end-1))
			e.mode = visualMode
		}
	} else if e.clickCount >= 3 {
		e.anchor = pos
		e.mode = visualLineMode
		e.clickCount = 0
	}
}
