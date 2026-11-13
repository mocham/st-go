//go:build linux

package fb

import "time"

// XF86 keysyms match gxw's unmodified hardware-key bindings. Console keymaps
// commonly leave these keys unmapped, so translate Linux input keycodes first.
const (
	systemVolumeDownKey     = 0x1008ff11
	systemVolumeUpKey       = 0x1008ff13
	systemBrightnessUpKey   = 0x1008ff02
	systemBrightnessDownKey = 0x1008ff03
)

func systemMediaKey(code int) uint {
	switch code {
	case 114: // KEY_VOLUMEDOWN
		return systemVolumeDownKey
	case 115: // KEY_VOLUMEUP
		return systemVolumeUpKey
	case 224: // KEY_BRIGHTNESSDOWN
		return systemBrightnessDownKey
	case 225: // KEY_BRIGHTNESSUP
		return systemBrightnessUpKey
	}
	return 0
}

func (d *Driver) systemBarMediaKey(key uint, modifiers uint16) bool {
	if modifiers != 0 {
		return false
	}
	switch key {
	case systemVolumeDownKey:
		d.changeSystemBarVolume(-systemBarVolumeDownStep)
	case systemVolumeUpKey:
		d.changeSystemBarVolume(systemBarVolumeUpStep)
	case systemBrightnessDownKey:
		d.changeSystemBarBrightness(-systemBarBrightnessDownStep)
	case systemBrightnessUpKey:
		d.changeSystemBarBrightness(systemBarBrightnessUpStep)
	default:
		return false
	}
	d.refreshSystemBar(time.Now())
	return true
}

// Pointer and keyboard actions share state, steps and clamping. Hardware calls
// run outside the drawing lock, and work even when their bar icons are clipped.
func (d *Driver) changeSystemBarVolume(delta int) {
	d.mu.Lock()
	b := d.systemBar
	if d.closed || b == nil || b.setAudioVolume == nil {
		d.mu.Unlock()
		return
	}
	next := max(0, min(systemBarMaxVolume, b.volumeLevel+delta))
	b.volumeLevel = next
	b.nextRefresh = time.Time{}
	setVolume := b.setAudioVolume
	d.mu.Unlock()
	setVolume(next)
}

func (d *Driver) changeSystemBarBrightness(delta int) {
	d.mu.Lock()
	b := d.systemBar
	if d.closed || b == nil || b.changeBrightness == nil || !b.brightnessValid {
		d.mu.Unlock()
		return
	}
	next := max(0, min(b.brightnessMaximum, b.brightnessLevel+delta))
	b.brightnessLevel = next
	b.brightnessOptimistic = true
	b.nextRefresh = time.Time{}
	change := b.changeBrightness
	d.mu.Unlock()
	actual, ok := change(delta)
	if ok && actual != next {
		d.mu.Lock()
		if !d.closed && d.systemBar == b {
			b.brightnessLevel = actual
			b.brightnessOptimistic = true
			b.nextRefresh = time.Time{}
		}
		d.mu.Unlock()
	}
}
