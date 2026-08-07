package main

import (
	"testing"
	"time"

	"st-go/config"
	"st-go/term"
)

// TestRenderToX renders a small terminal to the X server and reads back.
func TestRenderToX(t *testing.T) {
	cfg := config.Default()
	if !loadFonts(cfg.Font, int(cfg.GlyphHeight)) {
		t.Skip("font not available")
	}
	trm, err := NewTerminal(cfg)
	if err != nil {
		t.Skipf("no X server: %v", err)
	}
	defer trm.Close()
	trm.loadInputConfig(cfg)
	core := term.NewTerm(cfg, trm)
	trm.termCore = core
	core.Twrite([]byte("Hello ST-Go!"), false)
	core.Redraw()
	time.Sleep(300 * time.Millisecond)
	// read back window pixels
	rep, err := getImage(trm)
	if err != nil {
		t.Fatalf("getimage: %v", err)
	}
	// check some non-background pixels exist
	n := 0
	for _, v := range rep.Data {
		if v != 0 && v != 0xFF {
			n++
		}
	}
	t.Logf("non-bg pixels: %d", n)
	if n < 10 {
		t.Fatalf("expected rendered text, got %d non-bg pixels", n)
	}
}
