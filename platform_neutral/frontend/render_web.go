//go:build backend_web

package frontend

import "st-go/limits"

// Browser renderers consume font bytes through the resource API, not masks.
func loadFonts(path string, _ int) bool {
	fontMu.Lock()
	defer fontMu.Unlock()
	if path != "" {
		data, err := limits.ReadFile(path)
		if err != nil {
			return false
		}
		embeddedFont = data
	}
	invalidateGlyphsLocked()
	return len(embeddedFont) > 0
}

func closeFonts() {}

// Live web surfaces select the semantic path before reaching pixel entry points.
func makeGlyph(r rune, fg, bg uint32, w, h, baseline int) (int, []uint32)       { return 0, nil }
func makeGlyphLocked(r rune, fg, bg uint32, w, h, baseline int) (int, []uint32) { return 0, nil }
