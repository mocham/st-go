package x11

import (
	"image"
	"slices"
	"testing"

	"st-go/platform_neutral/api"
)

func TestTextCursorRetainsOnlyItsTileAndRefreshesDamagedUnderlay(t *testing.T) {
	d := &Driver{} // no X connection: exercise retention without a display server
	c := api.TextCursor{Bounds: image.Rect(2, 1, 4, 3), Pixels: []uint32{9, 9, 9, 9}, Base: []uint32{1, 1, 1, 1}}
	d.SetTextCursor(c)
	c.Pixels[0], c.Base[0] = 0, 0
	if d.textCursor.Pixels[0] != 9 || d.textCursor.Base[0] != 1 {
		t.Fatal("host borrowed mutable cursor storage")
	}
	pixels := make([]uint32, 8*4)
	pixels[2*8+3] = 42
	if r := d.rememberCursorBase(pixels, 8, image.Rect(3, 2, 8, 4)); r != image.Rect(3, 2, 4, 3) {
		t.Fatal("incorrect cursor damage intersection", r)
	}
	if !slices.Equal(d.textCursor.Base, []uint32{1, 1, 1, 42}) || !slices.Equal(d.textCursor.Pixels, []uint32{9, 9, 9, 9}) {
		t.Fatal("base upload damaged the caret or stale underlay")
	}
	d.SetTextCursor(api.TextCursor{})
	if !d.textCursor.Bounds.Empty() || len(d.textCursor.Pixels) != 0 {
		t.Fatal("caret did not hide")
	}
}
