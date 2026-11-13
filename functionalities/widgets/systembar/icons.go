// Package systembar shares the framebuffer system bar's icons and control policy.
package systembar

import (
	"bytes"
	"compress/zlib"
	"encoding/base64"
	"fmt"
	"image"
	"io"
	"time"
)

const (
	PollInterval       = 30 * time.Second
	Background         = 0xff2d2b46
	Foreground         = 0xffc4dfb5
	MaxVolume          = 100
	VolumeDownStep     = 4
	VolumeUpStep       = 3
	BrightnessDownStep = 4
	BrightnessUpStep   = 5
	IconWidth          = systemBarIconBitmapWidth
	IconHeight         = systemBarIconBitmapHeight
)

const (
	BatteryIcon byte = 0x80 + iota
	CheckedIcon
	UncheckedIcon
	UnknownIcon
	VolumeDownIcon
	VolumeUpIcon
	BrightnessDownIcon
	BrightnessUpIcon
	ScreenshotIcon
	CropIcon
	NewTerminalIcon
)

// Names retained by the original generated bitmap table.
const (
	systemBarBatteryIcon        = BatteryIcon
	systemBarCheckedIcon        = CheckedIcon
	systemBarUncheckedIcon      = UncheckedIcon
	systemBarUnknownIcon        = UnknownIcon
	systemBarVolumeDownIcon     = VolumeDownIcon
	systemBarVolumeUpIcon       = VolumeUpIcon
	systemBarBrightnessDownIcon = BrightnessDownIcon
	systemBarBrightnessUpIcon   = BrightnessUpIcon
)

var icons = loadIcons()

// Icon returns a shared, read-only alpha mask, or nil for an unknown icon.
func Icon(kind byte) *image.Alpha { return icons[kind] }

func loadIcons() map[byte]*image.Alpha {
	icons := make(map[byte]*image.Alpha)
	for kind, encoded := range systemBarIconBitmapBase64 {
		packed, err := base64.StdEncoding.DecodeString(encoded)
		if err != nil {
			panic(fmt.Sprintf("invalid system-bar icon %x: %v", kind, err))
		}
		zr, err := zlib.NewReader(bytes.NewReader(packed))
		if err != nil {
			panic(fmt.Sprintf("invalid system-bar icon %x: %v", kind, err))
		}
		data, err := io.ReadAll(io.LimitReader(zr, int64(IconWidth*IconHeight+1)))
		zr.Close()
		if err != nil || len(data) != IconWidth*IconHeight {
			panic(fmt.Sprintf("invalid system-bar icon %x size", kind))
		}
		icons[kind] = &image.Alpha{Pix: data, Stride: IconWidth, Rect: image.Rect(0, 0, IconWidth, IconHeight)}
	}
	blank := func() *image.Alpha { return image.NewAlpha(image.Rect(0, 0, IconWidth, IconHeight)) }
	line := func(mask *image.Alpha, x0, y0, x1, y1 int) {
		for y := max(0, y0); y < min(IconHeight, y1); y++ {
			for x := max(0, x0); x < min(IconWidth, x1); x++ {
				mask.Pix[y*mask.Stride+x] = 255
			}
		}
	}
	// Original framebuffer camera body, viewfinder bump, and lens.
	camera := blank()
	for _, r := range [][4]int{{1, 11, 20, 14}, {1, 14, 4, 28}, {17, 14, 20, 28}, {1, 25, 20, 28}, {7, 7, 14, 11}} {
		line(camera, r[0], r[1], r[2], r[3])
	}
	for y := 14; y < 25; y++ {
		for x := 5; x < 16; x++ {
			d := (x-10)*(x-10) + (y-19)*(y-19)
			if d >= 12 && d <= 28 {
				camera.Pix[y*camera.Stride+x] = 255
			}
		}
	}
	icons[ScreenshotIcon] = camera
	crop := blank()
	for _, r := range [][4]int{{2, 7, 10, 10}, {2, 7, 5, 16}, {12, 7, 20, 10}, {17, 7, 20, 16}, {2, 25, 10, 28}, {2, 19, 5, 28}, {12, 25, 20, 28}, {17, 19, 20, 28}} {
		line(crop, r[0], r[1], r[2], r[3])
	}
	icons[CropIcon] = crop
	terminal := blank()
	for _, r := range [][4]int{{1, 7, 20, 9}, {1, 9, 3, 27}, {18, 9, 20, 18}, {1, 25, 10, 27}, {5, 12, 7, 14}, {7, 14, 9, 16}, {5, 16, 7, 18}, {15, 19, 17, 29}, {11, 23, 21, 25}} {
		line(terminal, r[0], r[1], r[2], r[3])
	}
	icons[NewTerminalIcon] = terminal
	return icons
}
