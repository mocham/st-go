package web

import (
	"image"
	"testing"

	"st-go/platform_neutral/api"
)

func TestTextCursorOverlayKeepsBaseCleanAndDamagesOnlyCells(t *testing.T) {
	d := testDriver(t)
	pixels := make([]uint32, d.width*d.height)
	for i := range pixels {
		pixels[i] = 0xff224466
	}
	d.Upload(pixels, d.width, 0, 0, d.width, d.height)
	d.frames()
	c := api.TextCursor{Bounds: image.Rect(1, 1, 2, 2), Pixels: []uint32{0xffffffff}, Base: []uint32{0xff224466}}
	d.SetTextCursor(c)
	c.Pixels[0] = 0
	frames := d.frames()
	if len(frames) != 1 || frames[0].rect != c.Bounds || frames[0].pixels[0] != 0xffffffff || d.pixels[d.width+1] != 0xff224466 {
		t.Fatal("cursor contaminated base or damaged outside its cell", frames)
	}
	pixels[d.width+1] = 0xffaabbcc
	d.Upload(pixels, d.width, 1, 1, 1, 1)
	if frames = d.frames(); len(frames) != 1 || frames[0].pixels[0] != 0xffffffff {
		t.Fatal("base upload erased stationary caret")
	}
	c.Pixels[0] = 0xffffffff
	c.Bounds = image.Rect(12, 6, 13, 7)
	d.SetTextCursor(c)
	frames = d.frames()
	if len(frames) != 2 {
		t.Fatal("cursor damage bridged clean pixels", frames)
	}
	if frames[0].rect != image.Rect(1, 1, 2, 2) || frames[0].pixels[0] != 0xffaabbcc {
		t.Fatal("old caret did not restore latest base")
	}
	d.SetTextCursor(api.TextCursor{})
	if frames = d.frames(); len(frames) != 1 || frames[0].pixels[0] != 0xff224466 {
		t.Fatal("hiding caret left stale pixels")
	}
}
