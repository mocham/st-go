//go:build !backend_web

package frontend

import (
	"log"

	"st-go/limits"
	"st-go/platform_neutral/fontraster"
)

var fonts *fontraster.Set

func loadFonts(name string, pixelHeight int) bool {
	fontMu.Lock()
	defer fontMu.Unlock()
	next, err := fontraster.Load(openFontFace, name, embeddedFont, pixelHeight)
	if err != nil {
		log.Printf("font: %v", err)
		return false
	}
	previous := fonts
	fonts = next
	invalidateGlyphsLocked()
	_ = previous.Close()
	return true
}

func closeFonts() {
	fontMu.Lock()
	defer fontMu.Unlock()
	_ = fonts.Close()
	fonts = nil
	invalidateGlyphsLocked()
}

// makeGlyph returns exactly the requested cell width, independently of font
// advances. Terminal layout has already chosen a one- or two-column cell.
func makeGlyph(r rune, fg, bg uint32, width, height, baseline int) (int, []uint32) {
	fontMu.Lock()
	defer fontMu.Unlock()
	return makeGlyphLocked(r, fg, bg, width, height, baseline)
}

func makeGlyphLocked(r rune, fg, bg uint32, width, height, baseline int) (int, []uint32) {
	if _, ok := limits.RGBA("glyph cell", width, height); !ok {
		return width, nil
	}
	return width, fontraster.Compose(fonts.Glyph(r), fg, bg, width, height, baseline)
}
