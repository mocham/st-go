package main

import "log"

func logf(format string, args ...interface{}) {
	log.Printf(format, args...)
}

// encodeUTF8 writes the UTF-8 encoding of r into b, returning bytes written.
func encodeUTF8(b []byte, r rune) int {
	switch {
	case r <= 0x7F:
		b[0] = byte(r)
		return 1
	case r <= 0x7FF:
		b[0] = 0xC0 | byte(r>>6)
		b[1] = 0x80 | byte(r&0x3F)
		return 2
	case r <= 0xFFFF:
		b[0] = 0xE0 | byte(r>>12)
		b[1] = 0x80 | byte((r>>6)&0x3F)
		b[2] = 0x80 | byte(r&0x3F)
		return 3
	default:
		b[0] = 0xF0 | byte(r>>18)
		b[1] = 0x80 | byte((r>>12)&0x3F)
		b[2] = 0x80 | byte((r>>6)&0x3F)
		b[3] = 0x80 | byte(r&0x3F)
		return 4
	}
}
