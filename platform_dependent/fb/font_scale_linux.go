//go:build linux

package fb

import (
	"fmt"
	"unsafe"

	"golang.org/x/sys/unix"
)

// FontScale reads the visible mode before the frontend loads fonts or computes
// cell geometry. This probe neither maps the framebuffer nor takes over a VT.
func (o *Options) FontScale() (float64, error) {
	device := o.Device
	if device == "" {
		device = "/dev/fb0"
	}
	fd, err := unix.Open(device, unix.O_RDONLY|unix.O_CLOEXEC, 0)
	if err != nil {
		return 0, fmt.Errorf("fb %s: %w", device, err)
	}
	defer unix.Close(fd)
	var mode variableInfo
	if err := ioctlPointer(fd, fbGetVariable, unsafe.Pointer(&mode)); err != nil {
		return 0, fmt.Errorf("fb %s: FBIOGET_VSCREENINFO: %w", device, err)
	}
	if mode.Width == 0 || mode.Height == 0 {
		return 0, fmt.Errorf("fb %s: invalid visible resolution", device)
	}
	return resolutionFontScale(mode.Width, mode.Height), nil
}

func resolutionFontScale(width, height uint32) float64 {
	if width <= 1280 && height <= 800 {
		return 0.8
	}
	return 1
}
