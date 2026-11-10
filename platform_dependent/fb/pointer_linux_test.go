//go:build linux

package fb

import (
	"image"
	"slices"
	"testing"
)

func TestPointerDoublesOriginalPixelsAndBounds(t *testing.T) {
	if got := pointerBounds(image.Pt(3, 5)); got != image.Rect(3, 5, 21, 37) {
		t.Fatal("pointer width and height were not doubled", got)
	}
	for y := -1; y <= 32; y++ {
		for x := -1; x <= 18; x++ {
			var want uint32
			visible := false
			if x >= 0 && x < 18 && y >= 0 && y < 32 {
				// Independent reference: each old 9x16 pixel becomes a 2x2 block.
				ox, oy := x/2, y/2
				visible = ox <= oy/2
				if visible {
					want = 0xffffffff
					if ox == 0 || ox == oy/2 || oy == 15 {
						want = 0xff000000
					}
				}
			}
			if got, ok := pointerPixel(x, y); got != want || ok != visible {
				t.Fatalf("pointer pixel %d,%d = %x/%t, want %x/%t", x, y, got, ok, want, visible)
			}
		}
	}
}

func TestLargerPointerRestoresTailAndClipsAtScreenEdges(t *testing.T) {
	d := memoryDriver(t, 80, 80)
	pixels := make([]uint32, 80*80)
	for i := range pixels {
		pixels[i] = 0xff224466
	}
	d.Upload(pixels, 80, 0, 0, 80, 80)
	d.movePointer(image.Pt(2, 2), true)
	if pixelAt(d, 15, 30) != 0xffffff || pixelAt(d, 2, 33) != 0 {
		t.Fatal("enlarged pointer was clipped to its old dimensions")
	}
	// Moving must not bridge the clean gap between the old/new overlay bounds.
	d.screen.layout.pixel(d.screen.memory, 30, 38, 0xffabcdef)
	d.movePointer(image.Pt(50, 45), true)
	if pixelAt(d, 15, 30) != 0x224466 || pixelAt(d, 2, 33) != 0x224466 || pixelAt(d, 30, 38) != 0xabcdef {
		t.Fatal("pointer movement left a trail or repainted clean pixels")
	}
	d.movePointer(image.Pt(1000, 1000), true)
	if d.pointer != image.Pt(79, 79) || pixelAt(d, 79, 79) != 0 {
		t.Fatal("pointer hotspot or screen-edge clipping changed")
	}
	d.movePointer(d.pointer, false)
	if pixelAt(d, 79, 79) != 0x224466 || !slices.Equal(d.pixels, pixels) {
		t.Fatal("pointer hide failed to restore content or contaminated retained pixels")
	}
}
