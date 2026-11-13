package main

import (
	"fmt"
	"os"

	"github.com/golang/freetype/truetype"
	"golang.org/x/image/font"
	"golang.org/x/image/font/gofont/gomono"
)

// The native Go FreeType port rasterizes the bars locally. X11 receives only
// completed pixels: no CGO, X fonts, font server, or installed font is needed.
func newFont(path string, pixels int) (font.Face, error) {
	data := gomono.TTF
	if path != "" {
		var err error
		data, err = os.ReadFile(path)
		if err != nil {
			return nil, err
		}
	}
	ttf, err := truetype.Parse(data)
	if err != nil {
		return nil, fmt.Errorf("load font: %w", err)
	}
	return truetype.NewFace(ttf, &truetype.Options{Size: float64(pixels), DPI: 72, Hinting: font.HintingFull}), nil
}
