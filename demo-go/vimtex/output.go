package main

import (
	"fmt"
	"io"
	"strings"
)

// outputBoundary observes bytes already forwarded to st-go. Injecting a DCS
// in the middle of Vim's CSI/OSC/UTF-8 output would corrupt both streams, even
// with a mutex around writes. Retain only parser state, never buffered output.
type outputBoundary struct {
	state byte
	utf8  int
	osc   bool
}

const (
	ground byte = iota
	escape
	intermediate
	csi
	controlString
	stringEscape
)

func (p *outputBoundary) safe() bool { return p.state == ground && p.utf8 == 0 }

func (p *outputBoundary) observe(data []byte) {
	for _, b := range data {
		if p.utf8 > 0 {
			if b&0xc0 == 0x80 {
				p.utf8--
				continue
			}
			p.utf8 = 0
		}
		if b >= 0xc2 && b <= 0xf4 {
			switch {
			case b < 0xe0:
				p.utf8 = 1
			case b < 0xf0:
				p.utf8 = 2
			default:
				p.utf8 = 3
			}
			continue
		}
		if b == 0x18 || b == 0x1a { // CAN / SUB abort a control sequence
			p.state = ground
			continue
		}
		if p.state == controlString {
			if b == 0x1b {
				p.state = stringEscape
			} else if b == 7 && p.osc {
				p.state = ground
			}
			continue
		}
		if b == 0x1b {
			p.state = escape
			continue
		}
		switch p.state {
		case stringEscape:
			if b == '\\' {
				p.state = ground
				continue
			}
			// ESC terminated the old string; interpret its following byte.
			fallthrough
		case escape:
			switch b {
			case '[':
				p.state = csi
			case ']', 'P', '_', '^', 'k':
				p.state, p.osc = controlString, b == ']' || b == 'k'
			default:
				if b >= 0x20 && b <= 0x2f {
					p.state = intermediate
				} else if b >= 0x30 {
					p.state = ground
				}
			}
		case intermediate:
			if b >= 0x30 && b <= 0x7e {
				p.state = ground
			}
		case csi:
			if b >= 0x40 && b <= 0x7e {
				p.state = ground
			}
		}
	}
}

func dcs(out io.Writer, command string) error {
	_, err := fmt.Fprintf(out, "\x1bP%s\x1b\\", command)
	return err
}

// The st-go tokenizer concatenates quoted fragments; this represents quotes
// and semicolons without depending on shell evaluation or backslash escapes.
func quoteDSL(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "'\"'\"'") + "'"
}
