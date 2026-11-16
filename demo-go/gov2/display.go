package main

import (
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"strings"
	"sync"
	"unsafe"

	"golang.org/x/image/font"
	"golang.org/x/image/font/basicfont"
	"golang.org/x/image/math/fixed"
	"st-go/platform_neutral/api"
	"st-go/platform_neutral/video"
)

const topHeight, bottomHeight = 20, 40

type scanout interface {
	back() (pixels []uint32, width, height, stride int, err error)
	flip() error
	suspend() error
	resume() error
	close() error
}

// Serializes buffer ownership, page-flip completion and VT transitions. Only
// the decoder worker draws; the console loop may suspend/resume the device.
type display struct {
	mu     sync.Mutex
	device scanout
	active bool
}

func (d *display) Present(render video.FrameRenderer, status api.VideoStatus) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if !d.active || d.device == nil {
		return nil
	}
	pixels, w, h, stride, err := d.device.back()
	if err != nil {
		return err
	}
	if err := render(pixels[topHeight*stride:(h-bottomHeight)*stride], w, h-topHeight-bottomHeight, stride); err != nil {
		return err
	}
	drawStatus(pixels, w, h, stride, status)
	return d.device.flip()
}
func (d *display) suspend() error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if !d.active {
		return nil
	}
	if err := d.device.suspend(); err != nil {
		return err
	}
	d.active = false
	return nil
}
func (d *display) resume() error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.active {
		return nil
	}
	if err := d.device.resume(); err != nil {
		return err
	}
	d.active = true
	return nil
}
func (d *display) close() error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.device == nil {
		return nil
	}
	err := d.device.close()
	d.device, d.active = nil, false
	return err
}

func clock(ms int64) string {
	s := max(0, ms/1000)
	return fmt.Sprintf("%02d:%02d:%02d", s/3600, s/60%60, s%60)
}
func drawStatus(pixels []uint32, w, h, stride int, s api.VideoStatus) {
	// KMS XRGB8888 is little endian. Grayscale OSD colors have identical RGB
	// and BGR bytes, so the standard bitmap font can draw directly into it.
	img := &image.RGBA{Pix: unsafe.Slice((*byte)(unsafe.Pointer(&pixels[0])), len(pixels)*4), Stride: stride * 4, Rect: image.Rect(0, 0, w, h)}
	draw.Draw(img, image.Rect(0, 0, w, topHeight), image.Black, image.Point{}, draw.Src)
	draw.Draw(img, image.Rect(0, h-bottomHeight, w, h), image.Black, image.Point{}, draw.Src)
	text := func(y int, value string) {
		value = strings.Map(func(r rune) rune {
			if r < 32 || r > 126 {
				return '?'
			}
			return r
		}, value)
		value = value[:min(len(value), max(0, (w-8)/7))]
		drawer := font.Drawer{Dst: img, Src: image.White, Face: basicfont.Face7x13, Dot: fixed.P(4, y)}
		drawer.DrawString(value)
	}
	sound := fmt.Sprintf("vol %d%%", s.Volume)
	if s.Muted {
		sound = "muted"
	}
	text(14, fmt.Sprintf("gov2 DRM | %.2fx | %s %s / %s | %s", s.Speed, s.State, clock(s.PositionMS), clock(s.DurationMS), sound))
	text(h-7, "Space pause  arrows seek  [/] speed  +/- vol  m mute  q quit")
	if s.Warning != "" {
		text(h-23, "Audio: "+s.Warning)
	}
	track := image.Rect(4, h-bottomHeight+1, w-4, h-bottomHeight+4)
	draw.Draw(img, track, image.NewUniform(color.Gray{Y: 64}), image.Point{}, draw.Src)
	if s.DurationMS > 0 {
		track.Max.X = track.Min.X + int(int64(track.Dx())*max(0, min(s.PositionMS, s.DurationMS))/s.DurationMS)
		draw.Draw(img, track, image.White, image.Point{}, draw.Src)
	}
}
