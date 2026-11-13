package main

import (
	"bytes"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

const maxPasteBytes = 1 << 20

type regionLayout struct {
	mainColumns, mainRows int
}

type inputEvent struct {
	bus    string
	key    string
	paste  string
	region *regionLayout
	err    string
	mouse  *mouseEvent
}

type mouseEvent struct {
	button, x, y int
	release      bool
}

func parseMouse(sequence string) *mouseEvent {
	if len(sequence) < 8 || len(sequence) > 64 || !strings.HasPrefix(sequence, "[<") {
		return nil
	}
	final := sequence[len(sequence)-1]
	if final != 'M' && final != 'm' {
		return nil
	}
	fields := strings.Split(sequence[2:len(sequence)-1], ";")
	if len(fields) != 3 {
		return nil
	}
	var values [3]int
	for index, field := range fields {
		for _, r := range field {
			if r < '0' || r > '9' {
				return nil
			}
		}
		value, err := strconv.Atoi(field)
		if err != nil || value < 0 || value > 100000 {
			return nil
		}
		values[index] = value
	}
	if values[0] > 255 || values[1] == 0 || values[2] == 0 {
		return nil
	}
	return &mouseEvent{button: values[0], x: values[1], y: values[2], release: final == 'm'}
}

type inputParser struct {
	pending       []byte
	since         time.Time
	controlSince  time.Time
	paste         []byte
	inPaste       bool
	discardPaste  bool
	dcs           []byte
	inDCS         bool
	dcsEscape     bool
	discardDCS    bool
	discardEscape bool
}

func (parser *inputParser) feed(data []byte, now time.Time, emit func(inputEvent)) {
	if len(parser.pending) == 0 {
		parser.since = now
	}
	parser.pending = append(parser.pending, data...)
	for len(parser.pending) > 0 {
		if parser.inDCS {
			character := parser.pending[0]
			parser.pending = parser.pending[1:]
			if character == 0x18 || character == 0x1a || character == 7 || parser.dcsEscape && character == '\\' {
				if character == '\\' && !parser.discardDCS {
					if strings.HasPrefix(string(parser.dcs), "app-bus ") {
						emit(inputEvent{bus: string(parser.dcs)})
					} else if layout := parseRegionLayout(string(parser.dcs)); layout != nil {
						emit(inputEvent{region: layout})
					}
				}
				parser.dcs = nil
				parser.inDCS, parser.dcsEscape, parser.discardDCS = false, false, false
				parser.controlSince = time.Time{}
				continue
			}
			if parser.dcsEscape && !parser.discardDCS {
				parser.dcs = append(parser.dcs, 0x1b)
			}
			parser.dcsEscape = character == 0x1b
			if !parser.dcsEscape && !parser.discardDCS {
				if len(parser.dcs) >= 8192 {
					parser.dcs = nil
					parser.discardDCS = true
				} else {
					parser.dcs = append(parser.dcs, character)
				}
			}
			continue
		}
		if parser.inPaste {
			end := bytes.Index(parser.pending, []byte("\x1b[201~"))
			if end < 0 {
				keep := min(5, len(parser.pending))
				parser.appendPaste(parser.pending[:len(parser.pending)-keep])
				parser.pending = append([]byte(nil), parser.pending[len(parser.pending)-keep:]...)
				return
			}
			parser.appendPaste(parser.pending[:end])
			parser.pending = parser.pending[end+6:]
			if parser.discardPaste {
				emit(inputEvent{err: "paste exceeds 1 MiB; discarded"})
			} else if !utf8.Valid(parser.paste) {
				emit(inputEvent{err: "paste is not valid UTF-8; discarded"})
			} else {
				emit(inputEvent{paste: string(parser.paste)})
			}
			parser.inPaste, parser.discardPaste, parser.paste = false, false, nil
			parser.controlSince = time.Time{}
			continue
		}
		if parser.discardEscape {
			end := 0
			for end < len(parser.pending) && (parser.pending[end] < 0x40 || parser.pending[end] > 0x7e) {
				end++
			}
			if end == len(parser.pending) {
				parser.pending = nil
				return
			}
			parser.pending = parser.pending[end+1:]
			parser.discardEscape = false
			continue
		}
		data := parser.pending
		if data[0] != 0x1b {
			if !utf8.FullRune(data) {
				return
			}
			r, size := utf8.DecodeRune(data)
			parser.pending = data[size:]
			key := string(r)
			switch r {
			case utf8.RuneError:
				if size == 1 {
					emit(inputEvent{err: "invalid UTF-8 input ignored"})
					continue
				}
			case 0x7f:
				key = "<BS>"
			case '\r', '\n':
				key = "<Enter>"
			case '\t':
				key = "<Tab>"
			}
			emit(inputEvent{key: key})
			continue
		}
		if len(data) < 2 {
			return
		}
		if key := map[byte]string{'b': "<A-b>", 'f': "<A-f>", 'd': "<A-d>", 0x7f: "<A-BS>"}[data[1]]; key != "" {
			emit(inputEvent{key: key})
			parser.pending = data[2:]
			continue
		}
		if data[1] == 'P' {
			parser.pending = data[2:]
			parser.inDCS = true
			parser.controlSince = now
			continue
		}
		if data[1] == '[' || data[1] == 'O' {
			if bytes.HasPrefix(data, []byte("\x1b[200~")) {
				parser.pending = data[6:]
				parser.inPaste = true
				parser.controlSince = now
				continue
			}
			end := 2
			for end < len(data) && (data[end] < 0x40 || data[end] > 0x7e) {
				end++
			}
			if end == len(data) {
				if len(data) > 128 {
					parser.pending = nil
					parser.discardEscape = true
				}
				return
			}
			sequence := string(data[1 : end+1])
			parser.pending = data[end+1:]
			if mouse := parseMouse(sequence); mouse != nil {
				emit(inputEvent{mouse: mouse})
				continue
			}
			if key := map[string]string{
				"[A": "<Up>", "[B": "<Down>", "[C": "<Right>", "[D": "<Left>",
				"OA": "<Up>", "OB": "<Down>", "OC": "<Right>", "OD": "<Left>",
				"[H": "<Home>", "[F": "<End>", "[1~": "<Home>", "[4~": "<End>",
				"[3~": "<Delete>", "[5~": "<PageUp>", "[6~": "<PageDown>",
				"OQ": "<F2>", "[12~": "<F2>", "[1;5D": "<C-Left>", "[1;5C": "<C-Right>",
				"OR": "<F3>", "[13~": "<F3>",
				"OS": "<F4>", "[14~": "<F4>",
				"[1;3A": "<A-Up>", "[1;3B": "<A-Down>",
				"[5;3~": "<A-PageUp>", "[6;3~": "<A-PageDown>",
			}[sequence]; key != "" {
				emit(inputEvent{key: key})
			}
			continue
		}
		emit(inputEvent{key: "<Esc>"})
		parser.pending = data[1:]
	}
}

func (parser *inputParser) appendPaste(data []byte) {
	if parser.discardPaste {
		return
	}
	if len(parser.paste)+len(data) > maxPasteBytes {
		parser.paste = nil
		parser.discardPaste = true
		return
	}
	parser.paste = append(parser.paste, data...)
}

func (parser *inputParser) expire(now time.Time, emit func(inputEvent)) {
	if parser.inDCS && !parser.controlSince.IsZero() && now.Sub(parser.controlSince) >= 2*time.Second {
		parser.inDCS, parser.dcsEscape, parser.discardDCS = false, false, false
		parser.dcs, parser.pending = nil, nil
		parser.controlSince = time.Time{}
		emit(inputEvent{err: "incomplete terminal reply discarded"})
		return
	}
	if parser.inPaste && !parser.controlSince.IsZero() && now.Sub(parser.controlSince) >= 10*time.Second {
		parser.inPaste, parser.discardPaste = false, false
		parser.paste, parser.pending = nil, nil
		parser.controlSince = time.Time{}
		emit(inputEvent{err: "incomplete bracketed paste discarded"})
		return
	}
	if parser.inPaste || parser.inDCS || len(parser.pending) == 0 || now.Sub(parser.since) < 75*time.Millisecond {
		return
	}
	if string(parser.pending) == "\x1b" {
		emit(inputEvent{key: "<Esc>"})
	}
	parser.pending = nil
	parser.discardEscape = false
}

func parseRegionLayout(control string) *regionLayout {
	fields := strings.Fields(control)
	if len(fields) != 12 || fields[0] != "region" || fields[1] != "layout" || fields[2] != "main" || fields[7] != "secondary" {
		return nil
	}
	values := make([]int, 8)
	for index, field := range []int{3, 4, 5, 6, 8, 9, 10, 11} {
		value, err := strconv.Atoi(fields[field])
		if err != nil || value < 1 || value > 100000 {
			return nil
		}
		values[index] = value
	}
	if values[0] != 1 || values[1] != 1 || values[4] != values[2]+1 || values[5] != 1 || values[3] != values[7] {
		return nil
	}
	return &regionLayout{mainColumns: min(values[2], 1000), mainRows: min(values[3], 500)}
}
