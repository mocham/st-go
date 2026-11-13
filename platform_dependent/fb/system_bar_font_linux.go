//go:build linux

package fb

import (
	"bytes"
	"compress/zlib"
	"encoding/base64"
	"fmt"
	"image"
	"io"

	"golang.org/x/image/draw"
	"st-go/widgets/systembar"
)

// Keep the generated text atlas in source control so headless/pure-Go builds
// need no font loader. make st-fb/st-fb-min refresh it with the Go rasterizer.
//go:generate go run generate_system_bar_font.go -font ../../Monaco_Linux.ttf -output system_bar_font_generated.go

var systemBarGlyphs = loadSystemBarGlyphs()

// These small monochrome symbols are deliberately stored as cell patterns,
// rather than relying on a font's private-use glyph coverage. They therefore
// remain available on every framebuffer image and cannot degrade to '?'.
var systemBarIcons = loadSystemBarIcons()

type systemBarStyle struct {
	cellWidth, cellHeight int
	padding, focusWidth   int
	glyphs                [256][]uint32
}

// Build the scaled atlas once per bar, using the same visible-resolution factor
// as terminal text. Refreshes and focus changes only copy the cached glyphs.
func newSystemBarStyle(scale float64) *systemBarStyle {
	scaled := func(size int) int { return max(1, int(float64(size)*scale)) }
	s := &systemBarStyle{
		cellWidth: scaled(systemBarFontCellWidth), cellHeight: scaled(systemBarFontCellHeight),
		padding: scaled(systemBarPadding), focusWidth: scaled(2),
	}
	copy(s.glyphs[:], systemBarGlyphs[:])
	for ch, glyph := range systemBarIcons {
		s.glyphs[ch] = glyph
	}
	if s.cellWidth != systemBarFontCellWidth || s.cellHeight != systemBarFontCellHeight {
		for ch, glyph := range s.glyphs {
			if glyph != nil {
				s.glyphs[ch] = scaleSystemBarGlyph(glyph, s.cellWidth, s.cellHeight)
			}
		}
	}
	return s
}

func scaleSystemBarGlyph(glyph []uint32, width, height int) []uint32 {
	src := image.NewRGBA(image.Rect(0, 0, systemBarFontCellWidth, systemBarFontCellHeight))
	for i, pixel := range glyph {
		src.Pix[4*i], src.Pix[4*i+1], src.Pix[4*i+2], src.Pix[4*i+3] = byte(pixel>>16), byte(pixel>>8), byte(pixel), 255
	}
	dst := image.NewRGBA(image.Rect(0, 0, width, height))
	// Filter both text and icons to retain antialiased strokes at the smaller size.
	draw.BiLinear.Scale(dst, dst.Bounds(), src, src.Bounds(), draw.Src, nil)
	pixels := make([]uint32, width*height)
	for i := range pixels {
		pixels[i] = 0xff000000 | uint32(dst.Pix[4*i])<<16 | uint32(dst.Pix[4*i+1])<<8 | uint32(dst.Pix[4*i+2])
	}
	return pixels
}

func loadSystemBarIcons() map[byte][]uint32 {
	icons := make(map[byte][]uint32)
	for kind := systembar.BatteryIcon; kind <= systembar.NewTerminalIcon; kind++ {
		mask := systembar.Icon(kind)
		glyph := make([]uint32, systemBarFontCellWidth*systemBarFontCellHeight)
		for i := range glyph {
			glyph[i] = systemBarBackground
		}
		for y := 0; y < mask.Rect.Dy() && y < systemBarFontCellHeight; y++ {
			for x := 0; x < mask.Rect.Dx() && x < systemBarFontCellWidth; x++ {
				glyph[y*systemBarFontCellWidth+x] = systemBarBlend(mask.AlphaAt(x, y).A)
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
