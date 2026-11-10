//go:build linux

package fb

import (
	"bytes"
	"compress/zlib"
	"encoding/base64"
	"fmt"
	"io"
)

// Keep the generated text atlas in source control so headless/pure-Go builds
// need neither FreeType nor the TTF. make st-fb refreshes it when inputs change.
//go:generate go run generate_system_bar_font.go -font ../../Monaco_Linux.ttf -output system_bar_font_generated.go

var systemBarGlyphs = loadSystemBarGlyphs()

// These small monochrome symbols are deliberately stored as cell patterns,
// rather than relying on a font's private-use glyph coverage. They therefore
// remain available on every framebuffer image and cannot degrade to '?'.
var systemBarIcons = loadSystemBarIcons()

var systemBarIconPatterns = map[byte][]string{
	systemBarBatteryIcon: {
		"  #####  ", " ######## ", "##      #", "## #### #", "## #### #", "## #### #", "## #### #", "##      #", " ######## ", "  #####  ",
	},
	systemBarSpeakerIcon: {
		"    ##   ", "   ###   ", "  ####   ", "######## ", "######## ", "  ####   ", "   ###   ", "    ##   ", "  ##  ## ", " ##    ##",
	},
	systemBarEarphoneIcon: {
		"  ##  ## ", " ##    ##", "##      #", "##      #", "##      #", "##      #", " ##    ##", "  ###### ", "    ##   ", "    ##   ",
	},
	systemBarAudioIcon: {
		"   ####  ", "  ##  ## ", " ##    ##", "##      #", "##      #", " ##    ##", "  ##  ## ", "   ####  ", "    ##   ", "    ##   ",
	},
}

func loadSystemBarIcons() map[byte][]uint32 {
	icons := make(map[byte][]uint32, len(systemBarIconPatterns))
	for kind, pattern := range systemBarIconPatterns {
		glyph := make([]uint32, systemBarFontCellWidth*systemBarFontCellHeight)
		for i := range glyph {
			glyph[i] = systemBarBackground
		}
		for y, row := range pattern {
			if y >= systemBarFontCellHeight {
				break
			}
			for x, pixel := range row {
				if pixel != '#' || x >= systemBarFontCellWidth {
					continue
				}
				glyph[y*systemBarFontCellWidth+x] = systemBarForeground
			}
		}
		icons[kind] = glyph
	}
	return icons
}

func loadSystemBarGlyphs() [128][]uint32 {
	packed, err := base64.StdEncoding.DecodeString(systemBarFontAlphaBase64)
	if err != nil {
		panic(fmt.Sprintf("invalid built-in Monaco atlas: %v", err))
	}
	zr, err := zlib.NewReader(bytes.NewReader(packed))
	if err != nil {
		panic(fmt.Sprintf("invalid built-in Monaco atlas: %v", err))
	}
	defer zr.Close()
	const cell = systemBarFontCellWidth * systemBarFontCellHeight
	const size = len(systemBarFontAlphabet) * cell
	data, err := io.ReadAll(io.LimitReader(zr, int64(size+1)))
	if err != nil || len(data) != size {
		panic("invalid built-in Monaco atlas size")
	}
	var glyphs [128][]uint32
	for i, ch := range systemBarFontAlphabet {
		glyph := make([]uint32, cell)
		for at, alpha := range data[i*cell : (i+1)*cell] {
			glyph[at] = systemBarBlend(alpha)
		}
		glyphs[ch] = glyph
	}
	return glyphs
}

func systemBarBlend(alpha byte) uint32 {
	a := uint32(alpha)
	pixel := uint32(0xff000000)
	for shift := uint(0); shift <= 16; shift += 8 {
		fg, bg := uint32(systemBarForeground)>>shift&255, uint32(systemBarBackground)>>shift&255
		pixel |= ((fg*a + bg*(255-a) + 127) / 255) << shift
	}
	return pixel
}
