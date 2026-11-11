package frontend

import (
	"bytes"
	"image"
	"image/draw"
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"

	_ "golang.org/x/image/bmp"
	_ "golang.org/x/image/tiff"
	_ "golang.org/x/image/webp"
	"st-go/limits"
)

// decodeImage decodes an encoded raster image into tightly packed,
// unpremultiplied RGBA. Full builds first try libpng/libjpeg-turbo for PNG and
// JPEG; every profile retains the bounded native-Go decoder as a fallback.
func decodeImage(data []byte) (w, h int, rgba []byte, failed bool) {
	if len(data) == 0 || !limits.Check("encoded image", int64(len(data)), limits.FileBytes) {
		return 0, 0, nil, true
	}
	// Full profiles use libwebp; reduced profiles provide this function through
	// the x/image/webp implementation.
	if isWebP(data) {
		if w, h, rgba, ok := decodeWebP(data); ok {
			return w, h, rgba, false
		}
	}

	config, format, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		return 0, 0, nil, true
	}
	size, ok := limits.ImageRGBA("decoded image", config.Width, config.Height)
	if !ok {
		return 0, 0, nil, true
	}
	if rgba, ok := decodePreferredImage(data, format, config.Width, config.Height, size); ok {
		return config.Width, config.Height, rgba, false
	}

	decoded, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return 0, 0, nil, true
	}
	bounds := decoded.Bounds()
	w, h = bounds.Dx(), bounds.Dy()
	size, ok = limits.ImageRGBA("decoded image", w, h)
	if !ok || w != config.Width || h != config.Height {
		return 0, 0, nil, true
	}
	output := image.NewNRGBA(image.Rect(0, 0, w, h))
	draw.Draw(output, output.Bounds(), decoded, bounds.Min, draw.Src)
	if len(output.Pix) != size || output.Stride != w*4 {
		return 0, 0, nil, true
	}
	return w, h, output.Pix, false
}

// isWebP reports whether the bytes are a RIFF/WEBP container.
func isWebP(data []byte) bool {
	return len(data) >= 12 &&
		bytes.Equal(data[:4], []byte("RIFF")) &&
		bytes.Equal(data[8:12], []byte("WEBP"))
}
