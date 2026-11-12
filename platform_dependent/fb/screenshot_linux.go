//go:build linux

package fb

import (
	"fmt"
	"image"
	"image/png"
	"os"
	"path/filepath"
	"time"

	"st-go/limits"
	"st-go/platform_neutral/api"
)

const screenshotPath = "/tmp/screenshot.png"

type screenshotStage uint8

const (
	screenshotIdle screenshotStage = iota
	screenshotFirstPoint
	screenshotSecondPoint
)

type screenshotState struct {
	stage    screenshotStage
	first    image.Point
	consumed [12]bool
	save     func(*image.RGBA) error
}

// screenshotPress consumes only capture-mode left clicks. In particular, the
// press which activates capture is forwarded to the bar, and its matching
// release must also be forwarded so the widget router does not retain capture.
func (d *Driver) screenshotPress(button byte, point image.Point) bool {
	if button != 1 {
		return false
	}
	d.mu.Lock()
	if d.closed || d.screenshot.stage == screenshotIdle {
		d.screenshot.consumed[button] = false
		d.mu.Unlock()
		return false
	}
	d.screenshot.consumed[button] = true
	if d.screenshot.stage == screenshotFirstPoint {
		d.screenshot.first = point
		d.screenshot.stage = screenshotSecondPoint
		d.mu.Unlock()
		return true
	}

	first := d.screenshot.first
	bounds := image.Rect(min(first.X, point.X), min(first.Y, point.Y), max(first.X, point.X)+1, max(first.Y, point.Y)+1)
	shot := d.screenshotImageLocked(bounds)
	save := d.screenshot.save
	if save == nil {
		save = func(img *image.RGBA) error { return writeScreenshot(screenshotPath, img) }
	}
	d.screenshot.stage = screenshotIdle
	if d.systemBar != nil {
		d.systemBar.nextRefresh = time.Time{}
	}
	d.mu.Unlock()

	d.refreshSystemBar(time.Now())
	if shot != nil {
		if err := save(shot); err != nil {
			limits.Report("fb screenshot", "%v", err)
		}
	}
	return true
}

func (d *Driver) screenshotRelease(button byte) bool {
	if button != 1 {
		return false
	}
	d.mu.Lock()
	consumed := d.screenshot.consumed[button]
	d.screenshot.consumed[button] = false
	d.mu.Unlock()
	return consumed
}

// screenshotImageLocked snapshots the same retained layers as composeRect,
// except for the pointer: selecting the second corner must not mark the PNG.
// Caller holds d.mu.
func (d *Driver) screenshotImageLocked(bounds image.Rectangle) *image.RGBA {
	l := d.screen.layout
	bounds = bounds.Intersect(image.Rect(0, 0, l.width, l.height))
	if bounds.Empty() {
		return nil
	}
	shot := image.NewRGBA(image.Rect(0, 0, bounds.Dx(), bounds.Dy()))
	d.copyScreenshotLocked(shot, bounds)
	return shot
}

// Reuse the destination for screen recording instead of allocating each frame.
// Caller holds d.mu; bounds is clipped to the screen and matches shot's size.
func (d *Driver) copyScreenshotLocked(shot *image.RGBA, bounds image.Rectangle) {
	l := d.screen.layout
	copyScreenshotLayer(shot, bounds, image.Rect(0, 0, l.width, l.height), d.pixels, l.width)
	for _, surface := range d.surfaces {
		if !surface.hidden && surface.kind != api.BarWidget {
			copyScreenshotLayer(shot, bounds, surface.bounds, surface.pixels, surface.bounds.Dx())
		}
	}
	cursor := d.textCursor
	copyScreenshotLayer(shot, bounds, cursor.Bounds, cursor.Pixels, cursor.Bounds.Dx())
	if bar := d.systemBar; bar != nil && bar.widget == nil {
		copyScreenshotLayer(shot, bounds, bar.bounds, bar.pixels, bar.bounds.Dx())
	}
	for _, surface := range d.surfaces {
		if !surface.hidden && surface.kind == api.BarWidget {
			copyScreenshotLayer(shot, bounds, surface.bounds, surface.pixels, surface.bounds.Dx())
		}
	}
}

func copyScreenshotLayer(destination *image.RGBA, crop, layer image.Rectangle, pixels []uint32, stride int) {
	clip := crop.Intersect(layer)
	if clip.Empty() || stride <= 0 || len(pixels) < stride*layer.Dy() {
		return
	}
	for y := clip.Min.Y; y < clip.Max.Y; y++ {
		source := (y-layer.Min.Y)*stride + clip.Min.X - layer.Min.X
		target := (y-crop.Min.Y)*destination.Stride + (clip.Min.X-crop.Min.X)*4
		for _, pixel := range pixels[source : source+clip.Dx()] {
			destination.Pix[target] = byte(pixel >> 16)
			destination.Pix[target+1] = byte(pixel >> 8)
			destination.Pix[target+2] = byte(pixel)
			destination.Pix[target+3] = 0xff
			target += 4
		}
	}
}

func writeScreenshot(path string, shot image.Image) (err error) {
	directory, name := filepath.Dir(path), filepath.Base(path)
	output, err := os.CreateTemp(directory, "."+name+"-*")
	if err != nil {
		return fmt.Errorf("save %s: %w", path, err)
	}
	temporary := output.Name()
	defer func() {
		if output != nil {
			_ = output.Close()
		}
		_ = os.Remove(temporary)
	}()
	encoder := png.Encoder{CompressionLevel: png.BestSpeed}
	if err := encoder.Encode(output, shot); err != nil {
		return fmt.Errorf("encode %s: %w", path, err)
	}
	if err := output.Close(); err != nil {
		return fmt.Errorf("close %s: %w", path, err)
	}
	output = nil
	if err := os.Rename(temporary, path); err != nil {
		return fmt.Errorf("save %s: %w", path, err)
	}
	return nil
}
