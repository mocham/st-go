package main

import (
	"bytes"
	"image"
	"image/png"
	"testing"

	"st-go/term"
)

func TestTallImageDecode(t *testing.T) {
	for _, tc := range []struct {
		name string
		w, h int
		opts term.ImageDecodeOptions
	}{
		{"native", 8, 64, term.ImageDecodeOptions{}},
		{"fit-width", 8, 64, term.ImageDecodeOptions{FitWidth: true}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			trm := &Terminal{cols: 4, rows: 3, cw: 2, ch: 2}
			trm.ImageClearAll()
			t.Cleanup(trm.ImageClearAll)
			var encoded bytes.Buffer
			if err := png.Encode(&encoded, image.NewRGBA(image.Rect(0, 0, tc.w, tc.h))); err != nil {
				t.Fatal(err)
			}
			cols, rows, glyphs, ok := trm.ImageDecode(encoded.Bytes(), tc.opts)
			if !ok || cols < 1 || rows < 1 || len(glyphs) != cols*rows {
				t.Fatalf("decode = %dx%d, %d glyphs, ok=%v", cols, rows, len(glyphs), ok)
			}
			if rows > trm.rows || len(imageAtlas) > trm.cols*trm.rows*trm.cw*trm.ch*imageCacheScreens {
				t.Fatal("image exceeded screen/atlas bounds")
			}
		})
	}
}

func TestGlyphCacheBounded(t *testing.T) {
	for _, size := range []int{2, 32} {
		trm := &Terminal{cw: size, ch: size}
		ensureFramebuffer(size, size)
		for i := 0; i <= maxGlyphCacheEntries; i++ {
			trm.drawGlyphAt(0, uint32(i), 0, 0, 0, false)
		}
		glyphMu.RLock()
		pixels := 0
		for _, img := range glyphCache {
			pixels += len(img) * 4
		}
		entries, recorded := len(glyphCache), glyphCacheBytes
		glyphMu.RUnlock()
		if pixels > maxGlyphCacheBytes || entries > maxGlyphCacheEntries || recorded != pixels {
			t.Fatalf("cache has %d bytes (%d recorded), %d entries", pixels, recorded, entries)
		}
	}
}

func TestGlyphCacheDifferentCellSizes(t *testing.T) {
	for _, size := range []int{2, 4} {
		trm := &Terminal{cw: size, ch: size}
		ensureFramebuffer(size, size)
		trm.drawGlyphAt(0, 0, 0xff112233, 0, 0, false)
		for _, pixel := range framebuf {
			if pixel != 0xff112233 {
				t.Fatalf("wrong cached glyph at size %d: %#x", size, pixel)
			}
		}
	}
}
