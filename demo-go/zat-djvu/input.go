package main

import (
	"bytes"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"st-go/tui/readline"
)

type inputParser struct {
	pending []byte
	since   time.Time
}

func (p *inputParser) feed(data []byte, now time.Time, key func(string), reply func(string)) {
	if len(p.pending) == 0 {
		p.since = now
	}
	p.pending = append(p.pending, data...)
	for len(p.pending) > 0 {
		buffer := p.pending
		if buffer[0] != 27 {
			if !utf8.FullRune(buffer) {
				break
			}
			r, n := utf8.DecodeRune(buffer)
			key(string(r))
			p.pending = buffer[n:]
			continue
		}
		if len(buffer) < 2 {
			break
		}
		if buffer[1] == 'P' {
			end := bytes.Index(buffer[2:], []byte("\x1b\\"))
			if end < 0 {
				break
			}
			reply(string(buffer[2 : 2+end]))
			p.pending = buffer[4+end:]
			continue
		}
		if buffer[1] == '[' || buffer[1] == 'O' {
			end := 2
			for end < len(buffer) && (buffer[end] < 0x40 || buffer[end] > 0x7e) {
				end++
			}
			if end == len(buffer) {
				break
			}
			sequence := string(buffer[1 : end+1])
			if strings.HasPrefix(sequence, "[<") {
				key("mouse:" + strings.TrimPrefix(sequence, "["))
			}
			if name := map[string]string{"[A": "up", "[B": "down", "[C": "right", "[D": "left", "[H": "home", "[F": "end", "OH": "home", "OF": "end", "[1~": "home", "[4~": "end", "[5~": "pageup", "[6~": "pagedown", "[Z": "backtab", "[1;2Z": "backtab"}[sequence]; name != "" {
				key(name)
			} else if buffer[1] == '[' {
				if name := readline.CSI(sequence[1:]); name != "" {
					key(name)
				}
			}
			p.pending = buffer[end+1:]
			continue
		}
		key("escape")
		p.pending = buffer[1:]
	}
	if len(p.pending) > 8192 {
		p.pending = nil
	}
}

func (p *inputParser) expire(now time.Time, key func(string)) {
	if len(p.pending) == 0 || now.Sub(p.since) < 150*time.Millisecond {
		return
	}
	if string(p.pending) == "\x1b" {
		key("escape")
	}
	p.pending = nil
}

func isLayoutReply(reply string) bool {
	fields := strings.Fields(reply)
	if len(fields) != 12 || fields[0] != "region" || fields[1] != "layout" || fields[2] != "main" || fields[3] != "1" || fields[4] != "1" || fields[6] != "1" || fields[7] != "secondary" || fields[8] != "1" || fields[9] != "2" || fields[5] != fields[10] {
		return false
	}
	width, widthErr := strconv.Atoi(fields[5])
	height, heightErr := strconv.Atoi(fields[11])
	return widthErr == nil && heightErr == nil && width > 0 && height > 0
}
