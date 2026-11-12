//go:build linux

package fb

import (
	"image"
	"time"
)

type systemBarControl uint8

const (
	systemBarNoControl systemBarControl = iota
	systemBarClipboard
	systemBarVolumeDown
	systemBarVolumeUp
	systemBarBrightnessDown
	systemBarBrightnessUp
	systemBarScreenshot
	systemBarNewTerminal
	systemBarAudioOutput
)

type systemBarButton struct {
	control systemBarControl
	bounds  image.Rectangle
}

// Use stable control identities so clock/volume changes and optional controls
// cannot move keyboard focus to a different action during a refresh.
func (b *systemBar) buttons() []systemBarButton {
	var buttons []systemBarButton
	add := func(control systemBarControl, left, right int) {
		if left < 0 {
			return
		}
		r := image.Rect(left, b.bounds.Min.Y+systemBarPadding, right, b.bounds.Min.Y+systemBarPadding+systemBarFontCellHeight).Intersect(b.bounds)
		if !r.Empty() {
			buttons = append(buttons, systemBarButton{control, r})
		}
	}
	add(systemBarClipboard, b.clipboardLeft, b.clipboardRight)
	add(systemBarVolumeDown, b.volumeDownLeft, b.volumeDownLeft+systemBarFontCellWidth)
	add(systemBarVolumeUp, b.volumeUpLeft, b.volumeUpLeft+systemBarFontCellWidth)
	add(systemBarBrightnessDown, b.brightnessDownLeft, b.brightnessDownLeft+systemBarFontCellWidth)
	add(systemBarBrightnessUp, b.brightnessUpLeft, b.brightnessUpLeft+systemBarFontCellWidth)
	add(systemBarScreenshot, b.screenshotLeft, b.screenshotRight)
	add(systemBarNewTerminal, b.newTerminalLeft, b.newTerminalRight)
	if b.audioLeft >= 0 {
		add(systemBarAudioOutput, b.audioLeft+2*systemBarFontCellWidth, b.audioLeft+3*systemBarFontCellWidth)
	}
	return buttons
}

func (b *systemBar) selectedButton(buttons []systemBarButton) int {
	for i, button := range buttons {
		if button.control == b.focusedControl {
			return i
		}
	}
	return 0
}

// Caller holds Driver.mu. Each upload owns its pixels after the lock is released.
func (b *systemBar) rasterize() {
	b.focusBounds = image.Rectangle{}
	if buttons := b.buttons(); b.focused && len(buttons) > 0 {
		button := buttons[b.selectedButton(buttons)]
		b.focusedControl = button.control
		b.focusBounds = button.bounds.Inset(-2).Intersect(b.bounds)
	}
	b.pixels = make([]uint32, b.bounds.Dx()*b.bounds.Dy())
	for y := b.bounds.Min.Y; y < b.bounds.Max.Y; y++ {
		for x := b.bounds.Min.X; x < b.bounds.Max.X; x++ {
			b.pixels[(y-b.bounds.Min.Y)*b.bounds.Dx()+x-b.bounds.Min.X], _ = b.pixel(x, y)
		}
	}
}

// Focus changes repaint cached information immediately, without polling sysfs
// or the mixer on every Tab press.
func (d *Driver) repaintSystemBar() {
	d.mu.Lock()
	b := d.systemBar
	if d.closed || b == nil {
		d.mu.Unlock()
		return
	}
	b.rasterize()
	d.invalidate(b.bounds)
	widget, bounds, pixels := b.widget, b.bounds, b.pixels
	d.mu.Unlock()
	if widget != nil {
		widget.Upload(pixels, bounds.Dx(), 0, 0, bounds.Dx(), bounds.Dy())
	}
}

func (d *Driver) focusSystemBar() bool {
	d.mu.Lock()
	b := d.systemBar
	if d.closed || b == nil || b.widget == nil || len(b.buttons()) == 0 {
		d.mu.Unlock()
		return false
	}
	widget, window := b.widget, b.focusWindow
	d.mu.Unlock()
	if !widget.Visible() {
		return false
	}
	if window != nil {
		previous := window.FocusedWidget()
		if previous != nil && previous.ID() != widget.ID() {
			d.mu.Lock()
			b.previousFocus = previous
			d.mu.Unlock()
		}
	}
	widget.FocusWidget()
	return true
}

func (d *Driver) systemBarFocus(focused bool) {
	d.mu.Lock()
	b := d.systemBar
	if d.closed || b == nil {
		d.mu.Unlock()
		return
	}
	b.focused = focused
	window, widget := b.focusWindow, b.widget
	d.mu.Unlock()
	if !focused && window != nil {
		current := window.FocusedWidget()
		// VT release keeps widget focus and its return target; switching back
		// to a terminal releases the old target rather than retaining its PTY.
		if current == nil || current.ID() != widget.ID() {
			d.mu.Lock()
			b.previousFocus = nil
			d.mu.Unlock()
		}
	}
	d.repaintSystemBar()
}

func (d *Driver) systemBarKeyPress(key uint, mods uint16) {
	d.mu.Lock()
	b := d.systemBar
	if d.closed || b == nil || !b.focused {
		d.mu.Unlock()
		return
	}
	if key == 0xff1b && mods == 0 { // Escape returns to the previous terminal.
		previous := b.previousFocus
		d.mu.Unlock()
		if previous != nil {
			previous.FocusWidget()
		}
		return
	}
	buttons := b.buttons()
	if len(buttons) == 0 {
		d.mu.Unlock()
		return
	}
	index := b.selectedButton(buttons)
	switch {
	case key == 0xff09 && mods == 0: // Tab
		index = (index + 1) % len(buttons)
	case (key == 0xfe20 || key == 0xff09) && mods == shiftMask: // Shift+Tab
		index = (index + len(buttons) - 1) % len(buttons)
	case (key == 0xff0d || key == 0xff8d) && mods == 0: // Return / keypad Enter
		r := buttons[index].bounds
		point := r.Min.Add(image.Pt(r.Dx()/2, r.Dy()/2)).Sub(b.bounds.Min)
		d.mu.Unlock()
		d.systemBarClick(point.X, point.Y)
		d.refreshSystemBar(time.Now())
		return
	default:
		d.mu.Unlock()
		return
	}
	b.focusedControl = buttons[index].control
	d.mu.Unlock()
	d.repaintSystemBar()
}
