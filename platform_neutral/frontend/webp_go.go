//go:build !libwebp || backend_web || !cgo || purego

package frontend

import (
	"bytes"
	"image"
	"image/draw"

	"golang.org/x/image/webp"
	"st-go/limits"
)

// Reduced profiles decode static WebP entirely in Go. Animated WebP remains a
// full-profile feature because x/image/webp exposes only a still-image decoder.
func decodeWebP(data []byte) (w, h int, rgba []byte, ok bool) {
	if len(data) == 0 || !limits.Check("encoded WebP", int64(len(data)), limits.FileBytes) {
		return 0, 0, nil, false
	}
	config, err := webp.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		return 0, 0, nil, false
	}
	size, valid := limits.ImageRGBA("decoded WebP", config.Width, config.Height)
	if !valid {
		return 0, 0, nil, false
	}
	decoded, err := webp.Decode(bytes.NewReader(data))
	if err != nil {
		return 0, 0, nil, false
	}
	bounds := decoded.Bounds()
	w, h = bounds.Dx(), bounds.Dy()
	if w != config.Width || h != config.Height {
		return 0, 0, nil, false
	}
	output := image.NewNRGBA(image.Rect(0, 0, w, h))
	draw.Draw(output, output.Bounds(), decoded, bounds.Min, draw.Src)
	if len(output.Pix) != size || output.Stride != w*4 {
		return 0, 0, nil, false
	}
	return w, h, output.Pix, true
}
