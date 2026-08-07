package main

import (
	"strconv"
	"strings"

	"st-go/config"
)

// parseHexColor parses "#rrggbb" into 0xAARRGGBB.
func parseHexColor(s string) (uint32, bool) {
	s = strings.TrimPrefix(s, "#")
	if len(s) != 6 {
		return 0, false
	}
	v, err := strconv.ParseUint(s, 16, 32)
	if err != nil {
		return 0, false
	}
	return 0xFF000000 | uint32(v), true
}

// xtermcolormap computes the 256-color cube/grayscale like st's x.c.
func xtermColorMap(argb uint32, i int) uint32 {
	if i < 16 {
		return argb
	}
	if i >= 232 {
		gray := 8 + 10*(i-232)
		return 0xFF000000 | uint32(gray)<<16 | uint32(gray)<<8 | uint32(gray)
	}
	// cube
	v := i - 16
	r := (v / 36) * 40 + 55
	g := ((v % 36) / 6) * 40 + 55
	b := (v % 6) * 40 + 55
	if r > 255 {
		r = 255
	}
	if g > 255 {
		g = 255
	}
	if b > 255 {
		b = 255
	}
	return 0xFF000000 | uint32(r)<<16 | uint32(g)<<8 | uint32(b)
}

// loadColors builds the full color table (0..255 + extended) from config.
func (t *Terminal) loadColors(cfg *config.Config) {
	// 16 base colors from config
	t.colors = make([]Color, 0, 512)
	for i := 0; i < 16; i++ {
		argb, _ := parseHexColor(cfg.Colorname[i])
		t.colors = append(t.colors, Color{argb: argb, pixel: argb & 0x00FFFFFF})
	}
	// fill 16..255 with xterm cube
	for i := 16; i < 256; i++ {
		argb := xtermColorMap(0, i)
		t.colors = append(t.colors, Color{argb: argb, pixel: argb & 0x00FFFFFF})
	}
	// extended slots (256+) from config
	for i := 0; i < len(cfg.Colorname)-16; i++ {
		argb, _ := parseHexColor(cfg.Colorname[16+i])
		t.colors = append(t.colors, Color{argb: argb, pixel: argb & 0x00FFFFFF})
	}
}

// ColorAt returns the ARGB for a palette index or truecolor value.
func (t *Terminal) colorAt(idx uint32) uint32 {
	if idx&0x01000000 != 0 {
		// truecolor
		return 0xFF000000 | (idx & 0x00FFFFFF)
	}
	if int(idx) < len(t.colors) {
		return t.colors[idx].argb
	}
	return 0xFF000000
}

// SetColorName sets palette entry idx to a hex color; returns error if invalid.
func (t *Terminal) SetColorName(idx int, name string) bool {
	if name == "" {
		return true // reset handled elsewhere
	}
	argb, ok := parseHexColor(name)
	if !ok {
		return false
	}
	if idx < 0 || idx >= len(t.colors) {
		return false
	}
	t.colors[idx] = Color{argb: argb, pixel: argb & 0x00FFFFFF}
	return false // success
}

// GetColor returns rgb bytes for a palette entry.
func (t *Terminal) GetColor(idx int) (r, g, b byte, ok bool) {
	if idx < 0 || idx >= len(t.colors) {
		return 0, 0, 0, false
	}
	argb := t.colors[idx].argb
	return byte(argb >> 16), byte(argb >> 8), byte(argb), true
}
