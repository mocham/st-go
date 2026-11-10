//go:build linux

package fb

import (
	"bytes"
	"image"
	"slices"
	"testing"

	"st-go/platform_neutral/api"
)

func TestTextCursorOverlayRestoresLatestContentAndIndependentDamage(t *testing.T) {
	d := memoryDriver(t, 40, 40)
	pixels := make([]uint32, 1600)
	for i := range pixels {
		pixels[i] = 0xff224466
	}
	d.Upload(pixels, 40, 0, 0, 40, 40)
	cursor := api.TextCursor{Bounds: image.Rect(2, 2, 4, 4), Pixels: []uint32{0xffffffff, 0xffffffff, 0xffffffff, 0xffffffff}, Base: []uint32{0xff224466, 0xff224466, 0xff224466, 0xff224466}}
	d.SetTextCursor(cursor)
	cursor.Pixels[0] = 0 // host must own its retained copy
	if pixelAt(d, 2, 2) != 0xffffff || !slices.Equal(pixels, d.pixels) {
		t.Fatal("caret contaminated base or borrowed mutable pixels")
	}
	d.movePointer(image.Pt(2, 2), true)
	if pixelAt(d, 2, 2) != 0 {
		t.Fatal("pointer was not above caret")
	}
	d.movePointer(image.Pt(20, 20), false)
	if pixelAt(d, 2, 2) != 0xffffff {
		t.Fatal("pointer movement erased caret")
	}
	pixels[2*40+2] = 0xffaabbcc
	d.Upload(pixels, 40, 2, 2, 1, 1)
	if pixelAt(d, 2, 2) != 0xffffff {
		t.Fatal("base update erased stationary caret")
	}
	d.screen.layout.pixel(d.screen.memory, 10, 15, 0xff554433)
	cursor.Bounds = image.Rect(30, 30, 32, 32)
	cursor.Pixels[0] = 0xffffffff
	d.SetTextCursor(cursor)
	if pixelAt(d, 2, 2) != 0xaabbcc || pixelAt(d, 10, 15) != 0x554433 {
		t.Fatal("caret movement failed to restore content or bridged clean rows")
	}
	d.SetTextCursor(api.TextCursor{})
	if pixelAt(d, 30, 30) != 0x224466 {
		t.Fatal("hidden caret left pixels behind")
	}
	before := bytes.Clone(d.screen.memory)
	d.active = false
	d.SetTextCursor(cursor)
	if !bytes.Equal(before, d.screen.memory) {
		t.Fatal("overlay painted into inactive VT")
	}
	d.active = true
	d.invalidate(image.Rect(0, 0, 40, 40))
	if pixelAt(d, 30, 30) != 0xffffff {
		t.Fatal("overlay did not survive VT return")
	}
}

func TestBulkBlitMatchesPixelConverterForFormatsAndClipping(t *testing.T) {
	for _, alpha := range []bitfield{{}, {Offset: 24, Length: 8}} {
		for _, depth := range []uint32{16, 24, 32} {
			f, v := info(5, 4)
			v.BitsPerPixel, v.VirtualWidth, v.VirtualHeight, v.XOffset, v.YOffset = depth, 7, 6, 1, 1
			v.Alpha = alpha
			if depth != 32 {
				v.Alpha = bitfield{}
			}
			if depth == 16 {
				v.Red, v.Green, v.Blue = bitfield{Offset: 11, Length: 5}, bitfield{Offset: 5, Length: 6}, bitfield{Length: 5}
			}
			f.LineLength = 7*(depth/8) + 8
			f.MemoryLength = f.LineLength * 6
			l, err := screenLayout(f, v)
			if err != nil {
				t.Fatal(err)
			}
			got, want := bytes.Repeat([]byte{0xcc}, l.length), bytes.Repeat([]byte{0xcc}, l.length)
			r, origin := image.Rect(2, 1, 5, 3), image.Pt(1, 0)
			pixels := []uint32{0, 1, 2, 3, 0x115588aa, 0xff113355, 0xffeeccaa, 0xffaabbcc, 0xff12ab78, 0xff3254ff, 0xff654321, 0xffffffff}
			l.blit(got, r, origin, pixels, 4)
			for y := r.Min.Y; y < r.Max.Y; y++ {
				for x := r.Min.X; x < r.Max.X; x++ {
					l.pixel(want, x, y, pixels[(y-origin.Y)*4+x-origin.X])
				}
			}
			if !bytes.Equal(got, want) {
				t.Fatal("bulk path changed pixel conversion, padding, or untouched pixels", depth, alpha)
			}
		}
	}
}
