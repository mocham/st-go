package web

import (
	"image"
	"testing"
)

// A screenful of antialiased, colored cell rasters. It deliberately contains
// hundreds of distinct glyphs; benchmarking one repeated cell hides the cost
// of warming the browser cache and fragmenting ordinary PNG damage.
func benchmarkGlyphFrame() frame {
	const width, height = 80 * 16, 24 * 28
	f := frame{width, height, image.Rect(0, 0, width, height), make([]uint32, width*height)}
	for y := range height {
		for x := range width {
			cell := ((y/28)*80 + x/16) % 256
			shade := uint32((x%16*19 + y%28*7 + cell*31) & 255)
			f.pixels[y*width+x] = 0xff000000 | uint32(cell)<<16 | shade<<8 | shade
		}
	}
	return f
}

func BenchmarkWebDamage(b *testing.B) {
	f := benchmarkGlyphFrame()
	for _, mode := range []string{"PNG", "ColdGlyphs", "WarmGlyphs"} {
		b.Run(mode, func(b *testing.B) {
			e := newGlyphEncoder(16, 28, 0)
			if mode == "WarmGlyphs" {
				if _, err := e.encode(f); err != nil {
					b.Fatal(err)
				}
			}
			b.ReportAllocs()
			b.ResetTimer()
			for range b.N {
				if mode == "PNG" {
					if _, err := encodeFrame(f); err != nil {
						b.Fatal(err)
					}
					continue
				}
				if mode == "ColdGlyphs" {
					e = newGlyphEncoder(16, 28, 0)
				}
				if _, err := e.encode(f); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
