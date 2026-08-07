package main

import (
	"os"
	"strconv"

	"st-go/config"
)

// renderGlyphToFile is a debug helper: render one glyph to a PPM file.
func renderGlyphToFile(cfg *config.Config, r rune, fg, bg uint32, path string) {
	w := int(cfg.GlyphWidth) * 2
	h := int(cfg.GlyphHeight)
	used, img := makeGlyph(r, fg, bg, w, h, int(cfg.GlyphBaseline))
	_ = used
	f, err := os.Create(path)
	if err != nil {
		return
	}
	defer f.Close()
	f.WriteString("P3\n" + strconv.Itoa(w) + " " + strconv.Itoa(h) + "\n255\n")
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			p := img[y*w+x]
			f.WriteString(strconv.Itoa(int((p>>16)&0xff)) + " " +
				strconv.Itoa(int((p>>8)&0xff)) + " " +
				strconv.Itoa(int(p&0xff)) + "\n")
		}
	}
}
