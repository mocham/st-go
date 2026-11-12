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
// need no font loader. make st-fb/st-fb-min refresh it with the Go rasterizer.
//go:generate go run generate_system_bar_font.go -font ../../Monaco_Linux.ttf -output system_bar_font_generated.go

var systemBarGlyphs = loadSystemBarGlyphs()

// These small monochrome symbols are deliberately stored as cell patterns,
// rather than relying on a font's private-use glyph coverage. They therefore
// remain available on every framebuffer image and cannot degrade to '?'.
var systemBarIcons = loadSystemBarIcons()

func loadSystemBarIcons() map[byte][]uint32 {
	icons := make(map[byte][]uint32, len(systemBarIconBitmapBase64))
	for kind, encoded := range systemBarIconBitmapBase64 {
		packed, err := base64.StdEncoding.DecodeString(encoded)
		if err != nil {
			panic(fmt.Sprintf("invalid system-bar icon %x: %v", kind, err))
		}
		zr, err := zlib.NewReader(bytes.NewReader(packed))
		if err != nil {
			panic(fmt.Sprintf("invalid system-bar icon %x: %v", kind, err))
		}
		alpha, err := io.ReadAll(io.LimitReader(zr, int64(systemBarIconBitmapWidth*systemBarIconBitmapHeight+1)))
		zr.Close()
		if err != nil || len(alpha) != systemBarIconBitmapWidth*systemBarIconBitmapHeight {
			panic(fmt.Sprintf("invalid system-bar icon %x size", kind))
		}
		glyph := make([]uint32, systemBarFontCellWidth*systemBarFontCellHeight)
		for i := range glyph {
			glyph[i] = systemBarBackground
		}
		for y := 0; y < systemBarIconBitmapHeight && y < systemBarFontCellHeight; y++ {
			for x := 0; x < systemBarIconBitmapWidth && x < systemBarFontCellWidth; x++ {
				glyph[y*systemBarFontCellWidth+x] = systemBarBlend(alpha[y*systemBarIconBitmapWidth+x])
			}
		}
		icons[kind] = glyph
	}
	icons[systemBarScreenshotIcon] = systemBarCameraGlyph()
	icons[systemBarCropIcon] = systemBarCropGlyph()
	icons[systemBarNewTerminalIcon] = systemBarNewTerminalGlyph()
	return icons
}

func blankSystemBarIcon() []uint32 {
	glyph := make([]uint32, systemBarFontCellWidth*systemBarFontCellHeight)
	for i := range glyph {
		glyph[i] = systemBarBackground
	}
	return glyph
}

func systemBarIconLine(glyph []uint32, x0, y0, x1, y1 int) {
	for y := max(0, y0); y < min(systemBarFontCellHeight, y1); y++ {
		for x := max(0, x0); x < min(systemBarFontCellWidth, x1); x++ {
			glyph[y*systemBarFontCellWidth+x] = systemBarForeground
		}
	}
}

func systemBarCameraGlyph() []uint32 {
	glyph := blankSystemBarIcon()
	// Camera body, viewfinder bump, and lens. These are kept procedural so the
	// framebuffer build does not acquire another font or icon dependency.
	systemBarIconLine(glyph, 1, 11, 20, 14)
	systemBarIconLine(glyph, 1, 14, 4, 28)
	systemBarIconLine(glyph, 17, 14, 20, 28)
	systemBarIconLine(glyph, 1, 25, 20, 28)
	systemBarIconLine(glyph, 7, 7, 14, 11)
	for y := 14; y < 25; y++ {
		for x := 5; x < 16; x++ {
			distance := (x-10)*(x-10) + (y-19)*(y-19)
			if distance >= 12 && distance <= 28 {
				glyph[y*systemBarFontCellWidth+x] = systemBarForeground
			}
		}
	}
	return glyph
}

func systemBarCropGlyph() []uint32 {
	glyph := blankSystemBarIcon()
	// Four open corners distinguish capture mode from the idle camera.
	for _, line := range [][4]int{
		{2, 7, 10, 10}, {2, 7, 5, 16},
		{12, 7, 20, 10}, {17, 7, 20, 16},
		{2, 25, 10, 28}, {2, 19, 5, 28},
		{12, 25, 20, 28}, {17, 19, 20, 28},
	} {
		systemBarIconLine(glyph, line[0], line[1], line[2], line[3])
	}
	return glyph
}

func systemBarNewTerminalGlyph() []uint32 {
	glyph := blankSystemBarIcon()
	// A terminal window with a prompt and a plus in its lower-right corner.
	for _, line := range [][4]int{
		{1, 7, 20, 9}, {1, 9, 3, 27}, {18, 9, 20, 18}, {1, 25, 10, 27},
		{5, 12, 7, 14}, {7, 14, 9, 16}, {5, 16, 7, 18},
		{15, 19, 17, 29}, {11, 23, 21, 25},
	} {
		systemBarIconLine(glyph, line[0], line[1], line[2], line[3])
	}
	return glyph
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
