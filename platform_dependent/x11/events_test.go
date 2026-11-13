package x11

import (
	"image"
	"testing"

	"github.com/BurntSushi/xgb/xproto"
	"st-go/platform_neutral/api"
)

func TestConfigureOnlyResizesForChangedClientDimensions(t *testing.T) {
	var sizes []image.Point
	d := &Driver{win: 10, width: 800, height: 600, callbacks: api.Callbacks{Resize: func(w, h int) { sizes = append(sizes, image.Pt(w, h)) }}}
	for _, e := range []xproto.ConfigureNotifyEvent{
		{Window: 10, X: 12, Y: 20, Width: 800, Height: 600}, // move/stack/synthetic duplicates
		{Window: 20, Width: 200, Height: 30},                // a child surface
		{Window: 10, Width: 1024, Height: 768},
		{Window: 10, Width: 1024, Height: 768},
		{Window: 10, Width: 800, Height: 600},
	} {
		d.Dispatch(e)
	}
	if len(sizes) != 2 || sizes[0] != image.Pt(1024, 768) || sizes[1] != image.Pt(800, 600) {
		t.Fatal("redundant or missing client resize", sizes)
	}
}
