package main

import (
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"strings"
	"sync"
	"time"
	"unsafe"

	"golang.org/x/image/font"
	"golang.org/x/image/font/basicfont"
	"golang.org/x/image/math/fixed"
	"st-go/demo-go/gov2/internal/playback"
	"st-go/platform_neutral/api"
)

const topHeight, bottomHeight = 20, 40

type scanout interface {
	// back returns cacheable RAM, never a scanout mapping. flip uploads the
	// completed frame after retiring any previous page flip.
	back() (pixels []uint32, width, height, stride int, err error)
	flip() error
	suspend() error
	resume() error
	close() error
}

// Serializes buffer ownership, page-flip completion and VT transitions. Only
// the decoder worker draws; the console loop may suspend/resume the device.
type display struct {
	mu                             sync.Mutex
	device                         scanout
	active                         bool
	width, height                  int
	pointer                        image.Point
	pointerVisible, uiDirty, ready bool
	lastFlip                       time.Time
	cursor                         []cursorPixel
}

type cursorPixel struct {
	at    int
	pixel uint32
}

func (d *display) geometry() (int, int, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	_, w, h, _, err := d.device.back()
	d.width, d.height = w, h
	return w, h, err
}

func (d *display) movePointer(p image.Point, visible bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if p != d.pointer || visible != d.pointerVisible {
		d.pointer, d.pointerVisible, d.uiDirty = p, visible, true
	}
}

func (d *display) restoreCursor(pixels []uint32) {
	for _, saved := range d.cursor {
		pixels[saved.at] = saved.pixel
	}
	d.cursor = d.cursor[:0]
}

func (d *display) drawCursor(pixels []uint32, w, h, stride int) {
	if !d.pointerVisible {
		return
	}
	for y := 0; y < 16; y++ {
		for x := 0; x <= y/2; x++ {
			px, py := d.pointer.X+x, d.pointer.Y+y
			if px < 0 || py < 0 || px >= w || py >= h {
				continue
			}
			at := py*stride + px
			d.cursor = append(d.cursor, cursorPixel{at, pixels[at]})
			pixels[at] = 0xff000000
			if x == 0 || x == y/2 || y == 15 {
				pixels[at] = 0xffffffff
			}
		}
	}
}

// Refresh paused/ended frames from RAM, without decoding or scaling again.
// Motion is coalesced to at most 30 Hz; normal video presents also consume it.
func (d *display) refreshUI(status api.VideoStatus) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if !d.active || !d.ready || !d.uiDirty || time.Since(d.lastFlip) < time.Second/30 {
		return nil
	}
	pixels, w, h, stride, err := d.device.back()
	if err != nil {
		return err
	}
	d.restoreCursor(pixels)
	drawStatus(pixels, w, h, stride, status)
	d.drawCursor(pixels, w, h, stride)
	d.uiDirty, d.lastFlip = false, time.Now()
	return d.device.flip()
}

func (d *display) Present(render playback.FrameRenderer, status api.VideoStatus) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if !d.active || d.device == nil {
		return nil
	}
	pixels, w, h, stride, err := d.device.back()
	if err != nil {
		return err
	}
	d.width, d.height = w, h
	d.restoreCursor(pixels)
	if err := render(pixels[topHeight*stride:(h-bottomHeight)*stride], w, h-topHeight-bottomHeight, stride); err != nil {
		return err
	}
	drawStatus(pixels, w, h, stride, status)
	d.drawCursor(pixels, w, h, stride)
	d.ready, d.uiDirty, d.lastFlip = true, false, time.Now()
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
	// and BGR bytes, so the standard bitmap font can draw into the RAM buffer
	// without format conversion. Blending must never read mapped scanout memory.
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
	heading := fmt.Sprintf("gov2 DRM | %.2fx | %s %s / %s | %s", s.Speed, s.State, clock(s.PositionMS), clock(s.DurationMS), sound)
	if s.Warning != "" {
		heading += " | Audio: " + s.Warning
	}
	text(14, heading)
	track, buttons := statusLayout(w, h)
	for i, b := range buttons {
		if i == 0 && w >= 540 && (s.State == "paused" || s.State == "ended") {
			b.label = "Play"
		}
		if b.key == "m" && s.Muted {
			b.label = "Unmute"
			if w < 540 {
				b.label = "Unmt"
			}
		}
		draw.Draw(img, b.rect, image.NewUniform(color.Gray{Y: 48}), image.Point{}, draw.Src)
		label := b.label[:min(len(b.label), max(0, (b.rect.Dx()-2)/7))]
		drawer := font.Drawer{Dst: img, Src: image.White, Face: basicfont.Face7x13, Dot: fixed.P(b.rect.Min.X+(b.rect.Dx()-len(label)*7)/2, h-7)}
		drawer.DrawString(label)
	}
	track = track.Inset(3)
	draw.Draw(img, track, image.NewUniform(color.Gray{Y: 64}), image.Point{}, draw.Src)
	if s.DurationMS > 0 {
		track.Max.X = track.Min.X + int(int64(track.Dx())*max(0, min(s.PositionMS, s.DurationMS))/s.DurationMS)
		draw.Draw(img, track, image.White, image.Point{}, draw.Src)
	}
}
