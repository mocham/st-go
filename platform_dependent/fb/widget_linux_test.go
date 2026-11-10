//go:build linux

package fb

import (
	"image"
	"slices"
	"testing"
	"time"

	"st-go/platform_neutral/api"
	"st-go/widgets"
)

func TestSystemBarWidgetPreservesOverlayAndRefreshBehavior(t *testing.T) {
	d := memoryDriver(t, 960, 64)
	d.systemBar = &systemBar{powerSupplyPath: t.TempDir()}
	now := time.Date(2026, 11, 10, 12, 34, 56, 0, time.Local)
	d.refreshSystemBar(now)
	root, err := widgets.Wrap(d)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	bar := d.systemBar.widget
	if bar == nil || bar.Kind() != api.BarWidget || bar.ID() == root.ID() {
		t.Fatal("system bar was not registered as an independent widget")
	}
	terminal := make([]uint32, 960*64)
	for i := range terminal {
		terminal[i] = 0xff113355
	}
	root.Upload(terminal, 960, 0, 0, 960, 64)
	view, err := root.NewSurface(image.Rect(0, 0, 960, 40), 0xff557733)
	if err != nil {
		t.Fatal(err)
	}
	preview := make([]uint32, 960*40)
	for i := range preview {
		preview[i] = 0xff557733
	}
	view.Present(preview, 960, 40, 0, false)
	x, y := bar.Bounds().Min.X, bar.Bounds().Min.Y
	if pixelAt(d, x, y) != systemBarBackground&0xffffff {
		t.Fatal("preview covered system bar widget")
	}
	bar.SetVisible(false)
	if pixelAt(d, x, y) != 0x557733 {
		t.Fatal("hiding bar did not restore retained preview")
	}
	bar.SetVisible(true)
	d.refreshSystemBar(now.Add(systemBarInterval))
	if pixelAt(d, x, y) != systemBarBackground&0xffffff || !slices.Equal(terminal, d.pixels) {
		t.Fatal("widget bar contaminated terminal pixels or failed to refresh")
	}
	// Registering the passive bar must not consume one of the existing eight
	// retained-preview slots.
	for i := 1; i < 8; i++ {
		if _, err := root.NewSurface(image.Rect(0, 40, 1, 41), 0); err != nil {
			t.Fatal("bar reduced preview capacity", err)
		}
	}
}

func TestPositionedUploadClipsSourceAndDestinationTogether(t *testing.T) {
	d := memoryDriver(t, 8, 4)
	d.UploadAt([]uint32{1, 2, 3, 4, 5, 6, 7, 8}, 4, image.Rect(1, 0, 4, 2), image.Pt(-1, 1))
	if d.pixels[8] != 3 || d.pixels[9] != 4 || d.pixels[16] != 7 || d.pixels[17] != 8 {
		t.Fatal("widget upload used destination coordinates to address source pixels", d.pixels)
	}
}
