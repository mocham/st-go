//go:build fastimage

package frontend

import (
	"bytes"
	"image"
	"testing"
)

func TestPreferredNativeRasterDecoders(t *testing.T) {
	for _, format := range []string{"png", "jpeg"} {
		encoded := encodedRasterImages(t)[format]
		config, decodedFormat, err := image.DecodeConfig(bytes.NewReader(encoded))
		if err != nil {
			t.Fatal(err)
		}
		rgba, ok := decodePreferredImage(encoded, decodedFormat, config.Width, config.Height, config.Width*config.Height*4)
		if !ok || len(rgba) != config.Width*config.Height*4 {
			t.Fatalf("native %s decode failed", format)
		}
	}
}
