package main

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"image"
	"image/color"
	"image/jpeg"
	"testing"
)

func TestNativePDFJPEGImages(t *testing.T) {
	for _, grayscale := range []bool{false, true} {
		name, space := "RGB", "DeviceRGB"
		if grayscale {
			name, space = "Gray", "DeviceGray"
		}
		t.Run(name, func(t *testing.T) {
			const size = 64
			colors := []color.RGBA{{220, 30, 40, 255}, {30, 210, 50, 255}, {40, 60, 220, 255}, {230, 230, 230, 255}}
			if grayscale {
				colors = []color.RGBA{{40, 40, 40, 255}, {100, 100, 100, 255}, {170, 170, 170, 255}, {230, 230, 230, 255}}
			}
			rgb := image.NewRGBA(image.Rect(0, 0, size, size))
			gray := image.NewGray(rgb.Bounds())
			for y := 0; y < size; y++ {
				for x := 0; x < size; x++ {
					c := colors[(y/(size/2))*2+x/(size/2)]
					rgb.SetRGBA(x, y, c)
					gray.SetGray(x, y, color.Gray{Y: c.R})
				}
			}
			var src image.Image = rgb
			if grayscale {
				src = gray
			}
			var encoded bytes.Buffer
			if err := jpeg.Encode(&encoded, src, &jpeg.Options{Quality: 95}); err != nil {
				t.Fatal(err)
			}
			stream := "q 64 0 0 64 0 0 cm /Im1 Do Q\n"
			data := pdfTestObjects([]string{
				"<</Type/Catalog/Pages 2 0 R>>",
				"<</Type/Pages/Kids[3 0 R]/Count 1>>",
				"<</Type/Page/Parent 2 0 R/MediaBox[0 0 64 64]/Resources<</XObject<</Im1 5 0 R>>>>/Contents 4 0 R>>",
				fmt.Sprintf("<</Length %d>>\nstream\n%sendstream", len(stream), stream),
				fmt.Sprintf("<</Type/XObject/Subtype/Image/Width 64/Height 64/ColorSpace/%s/BitsPerComponent 8/Filter/DCTDecode/Length %d>>\nstream\n%s\nendstream", space, encoded.Len(), encoded.String()),
			})
			// Missing image codecs can still return a successful page render.
			// Check actual color samples through both PDF rendering entry points.
			check := func(pixel func(x, y int) uint32) {
				t.Helper()
				for i, want := range colors {
					got := pixel(16+(i%2)*32, 16+(i/2)*32)
					for channel, expected := range []uint8{want.B, want.G, want.R} {
						actual := int((got >> (channel * 8)) & 255)
						if delta := actual - int(expected); delta < -12 || delta > 12 {
							t.Fatalf("JPEG quadrant %d channel %d = %d, want approximately %d", i, channel, actual, expected)
						}
					}
				}
			}
			page, _, ok := renderPDFPage(data, 0, size, size)
			if !ok {
				t.Fatal("JPEG PDF page render failed")
			}
			check(func(x, y int) uint32 { return binary.LittleEndian.Uint32(page[(y*size+x)*4:]) })
			pdf, err := openScrollPDF(data)
			if err != nil {
				t.Fatal(err)
			}
			defer pdf.close()
			bitmap, err := pdf.render(size, size, 0, 0xffffffff)
			if err != nil {
				t.Fatal(err)
			}
			check(func(x, y int) uint32 { return bitmap.pixels[y*bitmap.width+x] })
		})
	}
}
