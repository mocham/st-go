package main

import (
	"bytes"
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
	if len(p.pending)+len(data) > 32768 {
		p.pending = nil
		return
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
			name := map[string]string{"[A": "up", "[B": "down", "[C": "right", "[D": "left", "[H": "home", "[1~": "home", "OH": "home"}[string(b[1:end+1])]
			if name != "" {
				key(name)
			}
			p.pending = b[end+1:]
			continue
		}
		key("escape")
		p.pending = b[1:]
	}
}
func (p *inputParser) expire(now time.Time, key func(string)) {
	if len(p.pending) == 0 {
		return
	}
	if string(p.pending) == "\x1b" && now.Sub(p.since) > 150*time.Millisecond {
		key("escape")
		p.pending = nil
	}
	if now.Sub(p.since) > 2*time.Second {
		p.pending = nil
	}
}
