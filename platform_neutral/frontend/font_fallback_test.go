package frontend

import (
	"os"
	"path/filepath"
	"slices"
	"syscall"
	"testing"
)

func TestFontOverrideKeepsEmbeddedFallback(t *testing.T) {
	if !loadFonts("", 28) {
		t.Fatal("embedded font unavailable")
	}
	_, want := makeGlyph('M', 0xffffffff, 0xff181818, 16, 28, 24)
	dir := t.TempDir()
	corrupt := filepath.Join(dir, "corrupt.ttf")
	if err := os.WriteFile(corrupt, []byte("not a font"), 0600); err != nil {
		t.Fatal(err)
	}
	fifo := filepath.Join(dir, "fifo.ttf")
	if err := syscall.Mkfifo(fifo, 0600); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"", filepath.Join(dir, "missing.ttf"), corrupt, fifo, ""} {
		if !loadFonts(path, 28) {
			t.Fatalf("font %q left no usable fallback", path)
		}
		_, got := makeGlyph('M', 0xffffffff, 0xff181818, 16, 28, 24)
		if !slices.Equal(got, want) {
			t.Fatalf("font %q did not fall back to embedded Monaco", path)
		}
		for _, r := range "Hello, world!0123456789" {
			_, pixels := makeGlyph(r, 0xffffffff, 0xff181818, 16, 28, 24)
			ink := 0
			for _, pixel := range pixels {
				if pixel != 0xff181818 {
					ink++
				}
			}
			if r != ' ' && ink == 0 {
				t.Fatalf("font %q: blank glyph %q", path, r)
			}
		}
	}
}
