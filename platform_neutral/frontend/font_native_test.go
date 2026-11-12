//go:build !backend_web

package frontend

import (
	"bytes"
	"slices"
	"sync"
	"testing"
)

func TestFontFacesHaveIndependentLifetimes(t *testing.T) {
	small, err := openFontFace(embeddedFont, 14)
	if err != nil {
		t.Fatal(err)
	}
	large, err := openFontFace(embeddedFont, 28)
	if err != nil {
		small.Close()
		t.Fatal(err)
	}
	defer small.Close()
	defer large.Close()
	g, found, err := large.Glyph('M')
	if err != nil || !found || g.Mask == nil {
		t.Fatalf("large glyph: %v, %v", found, err)
	}
	saved := bytes.Clone(g.Mask.Pix)
	smaller, _, _ := small.Glyph('M')
	if smaller.Mask == nil || smaller.Mask.Rect.Dy() >= g.Mask.Rect.Dy() {
		t.Fatal("faces shared raster size")
	}
	small.Close()
	large.Glyph('g')
	if !bytes.Equal(saved, g.Mask.Pix) {
		t.Fatal("glyph storage reused by face")
	}
	_, found, err = large.Glyph('\U0010FFFF')
	if err != nil || found {
		t.Fatalf("missing glyph: %v, %v", found, err)
	}
	if _, found, err := large.Glyph('M'); err != nil || !found {
		t.Fatal("closing one face invalidated another")
	}
}

func TestFontReloadInvalidatesCellsAndKeepsPreviousOnFailure(t *testing.T) {
	if !loadFonts("", 28) {
		t.Fatal("font load failed")
	}
	want := slices.Clone(cachedGlyphPixels('M', 0xffffffff, 0xff000000, 20, 32, 28))
	if loadFonts("", 0) {
		t.Fatal("invalid font size accepted")
	}
	if got := cachedGlyphPixels('M', 0xffffffff, 0xff000000, 20, 32, 28); !slices.Equal(got, want) {
		t.Fatal("failed load discarded current font")
	}
	if !loadFonts("", 14) {
		t.Fatal("font resize failed")
	}
	if got := cachedGlyphPixels('M', 0xffffffff, 0xff000000, 20, 32, 28); slices.Equal(got, want) {
		t.Fatal("reload reused stale colored cell")
	}
	if !loadFonts("", 28) {
		t.Fatal("font restore failed")
	}
	var workers sync.WaitGroup
	workers.Add(2)
	go func() {
		defer workers.Done()
		for i := 0; i < 12; i++ {
			loadFonts("", 28)
		}
	}()
	go func() {
		defer workers.Done()
		for i := 0; i < 100; i++ {
			if !slices.Equal(cachedGlyphPixels('M', 0xffffffff, 0xff000000, 20, 32, 28), want) {
				t.Error("reload raced with glyph rendering")
				return
			}
		}
	}()
	workers.Wait()
}
