package frontend

import (
	"bytes"
	"image"
	"image/color"
	"image/gif"
	"image/jpeg"
	"image/png"
	"testing"

	"golang.org/x/image/bmp"
	"golang.org/x/image/tiff"
)

func encodedRasterImages(t *testing.T) map[string][]byte {
	t.Helper()
	img := image.NewNRGBA(image.Rect(0, 0, 3, 2))
	for y := 0; y < 2; y++ {
		for x := 0; x < 3; x++ {
			img.SetNRGBA(x, y, color.NRGBA{R: uint8(40 + x*50), G: uint8(30 + y*80), B: 20, A: 255})
		}
	}
	encode := func(name string, fn func(*bytes.Buffer) error) []byte {
		t.Helper()
		var output bytes.Buffer
		if err := fn(&output); err != nil {
			t.Fatalf("encode %s: %v", name, err)
		}
		return output.Bytes()
	}
	palette := color.Palette{color.Black, color.RGBA{R: 200, G: 40, B: 20, A: 255}}
	indexed := image.NewPaletted(img.Bounds(), palette)
	for i := range indexed.Pix {
		indexed.Pix[i] = 1
	}
	return map[string][]byte{
		"png":  encode("PNG", func(output *bytes.Buffer) error { return png.Encode(output, img) }),
		"jpeg": encode("JPEG", func(output *bytes.Buffer) error { return jpeg.Encode(output, img, nil) }),
		"gif":  encode("GIF", func(output *bytes.Buffer) error { return gif.Encode(output, indexed, nil) }),
		"bmp":  encode("BMP", func(output *bytes.Buffer) error { return bmp.Encode(output, img) }),
		"tiff": encode("TIFF", func(output *bytes.Buffer) error { return tiff.Encode(output, img, nil) }),
		"webp": {
			0x52, 0x49, 0x46, 0x46, 0x1c, 0x00, 0x00, 0x00, 0x57, 0x45, 0x42, 0x50,
			0x56, 0x50, 0x38, 0x4c, 0x0f, 0x00, 0x00, 0x00, 0x2f, 0x01, 0x40, 0x00,
			0x00, 0x07, 0x10, 0xfd, 0x8f, 0xfe, 0x07, 0x22, 0xa2, 0xff, 0x01, 0x00,
		},
	}
}

func TestRasterImageCodecs(t *testing.T) {
	for name, encoded := range encodedRasterImages(t) {
		t.Run(name, func(t *testing.T) {
			width, height, rgba, failed := decodeImage(encoded)
			if failed {
				t.Fatal("decode failed")
			}
			wantWidth, wantHeight := 3, 2
			if name == "webp" {
				wantWidth, wantHeight = 2, 2
			}
			if width != wantWidth || height != wantHeight || len(rgba) != wantWidth*wantHeight*4 {
				t.Fatalf("decoded %dx%d with %d bytes, want %dx%d with %d", width, height, len(rgba), wantWidth, wantHeight, wantWidth*wantHeight*4)
			}
		})
	}
}

func TestRasterImagePreservesUnpremultipliedRGBA(t *testing.T) {
	img := image.NewNRGBA(image.Rect(0, 0, 1, 1))
	img.SetNRGBA(0, 0, color.NRGBA{R: 200, G: 100, B: 50, A: 128})
	var encoded bytes.Buffer
	if err := png.Encode(&encoded, img); err != nil {
		t.Fatal(err)
	}
	_, _, rgba, failed := decodeImage(encoded.Bytes())
	if failed {
		t.Fatal("decode failed")
	}
	want := []byte{200, 100, 50, 128}
	if !bytes.Equal(rgba, want) {
		t.Fatalf("RGBA = %v, want unpremultiplied %v", rgba, want)
	}
}
