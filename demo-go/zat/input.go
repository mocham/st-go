package main

import (
	"bytes"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
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
		b := p.pending
		if b[0] != 27 {
			if !utf8.FullRune(b) {
				break
			}
			r, n := utf8.DecodeRune(b)
			key(string(r))
			p.pending = b[n:]
			continue
		}
		if len(b) < 2 {
			break
		}
		if b[1] == 'P' {
			end := bytes.Index(b[2:], []byte("\x1b\\"))
			if end < 0 {
				break
			}
			reply(string(b[2 : 2+end]))
			p.pending = b[4+end:]
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
			sequence := string(b[1 : end+1])
			if strings.HasPrefix(sequence, "[<") {
				key("mouse:" + strings.TrimPrefix(sequence, "["))
			}
			if name := map[string]string{"[A": "up", "[B": "down", "[C": "right", "[D": "left", "[H": "home", "[F": "end", "OH": "home", "OF": "end", "[1~": "home", "[4~": "end", "[5~": "pageup", "[6~": "pagedown", "[Z": "backtab", "[1;2Z": "backtab"}[sequence]; name != "" {
				key(name)
			}
			// Unknown CSI, including focus/paste delimiters, is consumed whole.
			p.pending = b[end+1:]
			continue
		}
		key("escape")
		p.pending = b[1:]
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
	// Drop an incomplete control frame rather than interpreting its tail as keys.
	p.pending = nil
}

func isLayoutReply(s string) bool {
	f := strings.Fields(s)
	if len(f) != 12 || f[0] != "region" || f[1] != "layout" || f[2] != "main" || f[3] != "1" || f[4] != "1" || f[6] != "1" || f[7] != "secondary" || f[8] != "1" || f[9] != "2" || f[5] != f[10] {
		return false
	}
	width, e1 := strconv.Atoi(f[5])
	height, e2 := strconv.Atoi(f[11])
	return e1 == nil && e2 == nil && width > 0 && height > 0
}
