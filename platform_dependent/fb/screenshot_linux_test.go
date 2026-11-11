//go:build linux

package fb

import (
	"bytes"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"testing"
	"time"

	"st-go/platform_neutral/api"
	"st-go/widgets"
)

func TestScreenshotSelectionConsumesClicksAndCapturesComposedCrop(t *testing.T) {
	d := memoryDriver(t, 900, 100)
	d.systemBar = &systemBar{powerSupplyPath: t.TempDir()}
	for i := range d.pixels {
		d.pixels[i] = 0xffa02030
	}
	_, err := d.NewSurface(image.Rect(4, 3, 7, 6), 0xff1060b0)
	if err != nil {
		t.Fatal(err)
	}
	d.movePointer(image.Pt(2, 1), true)

	var shot *image.RGBA
	d.screenshot.save = func(img *image.RGBA) error {
		shot = image.NewRGBA(img.Bounds())
		copy(shot.Pix, img.Pix)
		return nil
	}
	presses, releases := 0, 0
	d.Bind(api.Callbacks{
		PointerPress:   func(byte, uint16, int, int) { presses++ },
		PointerRelease: func(byte, uint16, int, int) { releases++ },
	})
	d.refreshSystemBar(time.Now())
	b := d.systemBar
	if b.screenshotLeft < 0 || !bytes.Contains([]byte(b.text), []byte{systemBarScreenshotIcon}) {
		t.Fatal("system bar did not show the screenshot button", []byte(b.text))
	}
	d.systemBarClick(b.screenshotLeft-b.bounds.Min.X+1, b.bounds.Dy()/2)
	if d.screenshot.stage != screenshotFirstPoint || !bytes.Contains([]byte(b.text), []byte{systemBarCropIcon}) {
		t.Fatal("screenshot button did not enter crop mode", d.screenshot.stage, []byte(b.text))
	}

	// Reverse corner order verifies normalization. Releases must be consumed
	// together with their presses, even after the second press leaves crop mode.
	d.callbacks.PointerPress(1, 0, 7, 6)
	d.callbacks.PointerRelease(1, 0, 7, 6)
	d.callbacks.PointerPress(1, 0, 2, 1)
	d.callbacks.PointerRelease(1, 0, 2, 1)
	if presses != 0 || releases != 0 {
		t.Fatal("crop clicks escaped to the application", presses, releases)
	}
	if d.screenshot.stage != screenshotIdle || shot == nil || shot.Bounds() != image.Rect(0, 0, 6, 6) {
		t.Fatal("crop was not completed with inclusive corners", d.screenshot.stage, shot)
	}
	if got := shot.RGBAAt(0, 0); got != (color.RGBA{0xa0, 0x20, 0x30, 0xff}) {
		t.Fatal("pointer contaminated the crop", got)
	}
	if got := shot.RGBAAt(2, 2); got != (color.RGBA{0x10, 0x60, 0xb0, 0xff}) {
		t.Fatal("top surface was missing from the crop", got)
	}
	if !bytes.Contains([]byte(b.text), []byte{systemBarScreenshotIcon}) {
		t.Fatal("button did not return to its idle icon", []byte(b.text))
	}
}

func TestScreenshotActivationReleaseDoesNotLeaveBarPointerCapture(t *testing.T) {
	d := memoryDriver(t, 900, 100)
	d.systemBar = &systemBar{powerSupplyPath: t.TempDir()}
	d.screenshot.save = func(*image.RGBA) error { return nil }
	d.refreshSystemBar(time.Now())
	root, err := widgets.Wrap(d)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	rootPresses := 0
	root.Bind(api.Callbacks{PointerPress: func(byte, uint16, int, int) { rootPresses++ }})
	b := d.systemBar
	x, y := b.screenshotLeft+1, b.bounds.Min.Y+1
	d.callbacks.PointerPress(1, 0, x, y)
	d.callbacks.PointerRelease(1, 0, x, y)
	if d.screenshot.stage != screenshotFirstPoint {
		t.Fatal("bar button did not activate screenshot selection")
	}
	for _, point := range []image.Point{{2, 2}, {4, 4}} {
		d.callbacks.PointerPress(1, 0, point.X, point.Y)
		d.callbacks.PointerRelease(1, 0, point.X, point.Y)
	}
	d.callbacks.PointerPress(1, 0, 1, 1)
	d.callbacks.PointerRelease(1, 0, 1, 1)
	if rootPresses != 1 {
		t.Fatal("activating screenshot mode left pointer capture on the bar", rootPresses)
	}
}

func TestWriteScreenshotAtomicallyReplacesSymlink(t *testing.T) {
	directory := t.TempDir()
	target := filepath.Join(directory, "target")
	path := filepath.Join(directory, "screenshot")
	if err := os.WriteFile(target, []byte("unchanged"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, path); err != nil {
		t.Fatal(err)
	}
	shot := image.NewRGBA(image.Rect(0, 0, 2, 1))
	shot.SetRGBA(0, 0, color.RGBA{1, 2, 3, 255})
	shot.SetRGBA(1, 0, color.RGBA{4, 5, 6, 255})
	if err := writeScreenshot(path, shot); err != nil {
		t.Fatal(err)
	}
	if data, err := os.ReadFile(target); err != nil || string(data) != "unchanged" {
		t.Fatal("screenshot write followed its destination symlink", string(data), err)
	}
	info, err := os.Lstat(path)
	if err != nil || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm() != 0600 {
		t.Fatal("screenshot was not a private regular replacement", info, err)
	}
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := png.Decode(f)
	closeErr := f.Close()
	if err != nil || closeErr != nil || decoded.Bounds() != shot.Bounds() {
		t.Fatal("saved screenshot is not the selected PNG", decoded, err, closeErr)
	}
}
