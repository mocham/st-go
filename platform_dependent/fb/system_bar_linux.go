//go:build linux

package fb

import (
	"image"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"st-go/platform_neutral/api"
)

const (
	systemBarBatteryIcon        = byte(0x80)
	systemBarCheckedIcon        = byte(0x81)
	systemBarUncheckedIcon      = byte(0x82)
	systemBarUnknownIcon        = byte(0x83)
	systemBarVolumeDownIcon     = byte(0x84)
	systemBarVolumeUpIcon       = byte(0x85)
	systemBarBrightnessDownIcon = byte(0x86)
	systemBarBrightnessUpIcon   = byte(0x87)
	systemBarScreenshotIcon     = byte(0x88)
	systemBarCropIcon           = byte(0x89)
	systemBarNewTerminalIcon    = byte(0x8a)
)

const (
	systemBarInterval           = 30 * time.Second
	systemBarPadding            = 8
	systemBarBackground         = 0xff2d2b46 // Muted dark blue, slightly purple.
	systemBarForeground         = 0xffc4dfb5 // Soft light green.
	systemBarMaxInitialVolume   = 50
	systemBarMaxVolume          = 100
	systemBarVolumeDownStep     = 4
	systemBarVolumeUpStep       = 3
	systemBarBrightnessDownStep = 4
	systemBarBrightnessUpStep   = 5
)

// Backend-owned presentation state, protected by Driver.mu. No rows are
// reserved in the terminal and no system information is sent to applications.
type systemBar struct {
	powerSupplyPath       string
	nextRefresh           time.Time
	text                  string
	bounds                image.Rectangle
	textLeft              int
	pixels                []uint32
	widget                api.Widget
	audioOutput           func() (mode int, known bool)
	setAudioOutput        func(mode int) bool
	setAudioVolume        func(percent int) bool
	audioLeft             int
	audioMode             int
	audioModeValid        bool
	displayAudioMode      int
	displayAudioModeValid bool
	volumeLevel           int
	volumeDownLeft        int
	volumeUpLeft          int
	brightness            func() (level, maximum int, known bool)
	changeBrightness      func(delta int) (level int, ok bool)
	brightnessLevel       int
	brightnessMaximum     int
	brightnessValid       bool
	brightnessOptimistic  bool
	brightnessDownLeft    int
	brightnessUpLeft      int
	screenshotLeft        int
	screenshotRight       int
	clipboardLeft         int
	clipboardRight        int
	newTerminalTab        func()
	newTerminalLeft       int
	newTerminalRight      int
}

func (d *Driver) BindNewTerminalTab(handler func()) {
	d.mu.Lock()
	if d.closed || d.systemBar == nil {
		d.mu.Unlock()
		return
	}
	d.systemBar.newTerminalTab = handler
	d.systemBar.nextRefresh = time.Time{}
	d.mu.Unlock()
	d.refreshSystemBar(time.Now())
}

// AttachWidgets gives the passive bar the same identity, local buffer, and
// lifetime boundary as a terminal. Optional policies can share its bottom band
// through WindowBarLayout without changing the initial full-terminal layout.
func (d *Driver) AttachWidgets(container api.WidgetContainer) {
	d.mu.Lock()
	b := d.systemBar
	if b == nil || b.bounds.Empty() {
		d.mu.Unlock()
		return
	}
	bounds := b.bounds
	pixels := append([]uint32(nil), b.pixels...)
	d.mu.Unlock()
	widget, err := container.NewWidget(api.WidgetOptions{Kind: api.BarWidget, Bounds: bounds, Passive: true})
	if err != nil {
		return
	}
	widget.Bind(api.Callbacks{PointerPress: func(button byte, _ uint16, x, y int) {
		if button == 1 {
			d.systemBarClick(x, y)
		}
	}})
	d.mu.Lock()
	b.widget = widget
	d.mu.Unlock()
	widget.Upload(pixels, bounds.Dx(), 0, 0, bounds.Dx(), bounds.Dy())
}

// Called by New and the input loop, not by a separate timer/goroutine. Sysfs
// reads happen outside the drawing lock and never on application redraws.
func (d *Driver) refreshSystemBar(now time.Time) {
	d.mu.Lock()
	b := d.systemBar
	if d.closed || b == nil || now.Before(b.nextRefresh) {
		d.mu.Unlock()
		return
	}
	root := b.powerSupplyPath
	output := b.audioOutput
	setVolume, volumeLevel := b.setAudioVolume, b.volumeLevel
	readBrightness, changeBrightness := b.brightness, b.changeBrightness
	brightnessLevel, brightnessMaximum := b.brightnessLevel, b.brightnessMaximum
	brightnessKnown, brightnessOptimistic := b.brightnessValid, b.brightnessOptimistic
	overrideMode, overrideValid := b.audioMode, b.audioModeValid
	screenshotActive := d.screenshot.stage != screenshotIdle
	showNewTerminal := b.newTerminalTab != nil
	d.mu.Unlock()
	if !brightnessOptimistic && readBrightness != nil {
		brightnessLevel, brightnessMaximum, brightnessKnown = readBrightness()
	}

	text := systemBarText(now, root)
	clipboardLeft, clipboardRight := -1, -1
	if d.clipboardSync != nil {
		state := d.ClipboardSyncState()
		text += "  "
		clipboardLeft = len(text)
		icon := systemBarUncheckedIcon
		if !state.Available {
			icon = systemBarUnknownIcon
		} else if state.Enabled {
			icon = systemBarCheckedIcon
		}
		text += "S " + string([]byte{icon})
		clipboardRight = len(text)
	}
	volumeDownLeft, volumeUpLeft := -1, -1
	if setVolume != nil {
		text += "  "
		volumeDownLeft = len(text)
		text += string([]byte{systemBarVolumeDownIcon, ' '}) + strconv.Itoa(volumeLevel) + "% "
		volumeUpLeft = len(text)
		text += string([]byte{systemBarVolumeUpIcon})
	}
	brightnessDownLeft, brightnessUpLeft := -1, -1
	if changeBrightness != nil && brightnessKnown {
		text += "  "
		brightnessDownLeft = len(text)
		text += string([]byte{systemBarBrightnessDownIcon, ' '}) + strconv.Itoa(brightnessLevel) + " "
		brightnessUpLeft = len(text)
		text += string([]byte{systemBarBrightnessUpIcon})
	}
	text += "  "
	screenshotLeft := len(text)
	if screenshotActive {
		text += string([]byte{systemBarCropIcon})
	} else {
		text += string([]byte{systemBarScreenshotIcon})
	}
	screenshotRight := len(text)
	newTerminalLeft, newTerminalRight := -1, -1
	if showNewTerminal {
		text += "  "
		newTerminalLeft = len(text)
		text += string([]byte{systemBarNewTerminalIcon})
		newTerminalRight = len(text)
	}
	audioLeft := -1
	displayMode, displayKnown := 0, false
	if output != nil {
		mode, known := output()
		if overrideValid {
			mode, known = overrideMode, true
		}
		displayMode, displayKnown = mode, known && (mode == 0 || mode == 1)
		switch {
		case !known || mode != 0 && mode != 1:
			audioLeft = len(text)
			text += string([]byte{' ', ' ', systemBarUnknownIcon})
		case mode == 1:
			audioLeft = len(text)
			text += string([]byte{' ', ' ', systemBarCheckedIcon})
		default:
			audioLeft = len(text)
			text += string([]byte{' ', ' ', systemBarUncheckedIcon})
		}
	}
	d.mu.Lock()
	if d.closed || d.systemBar != b {
		d.mu.Unlock()
		return
	}
	b.displayAudioMode, b.displayAudioModeValid = displayMode, displayKnown
	b.brightnessLevel, b.brightnessMaximum, b.brightnessValid = brightnessLevel, brightnessMaximum, brightnessKnown
	b.brightnessOptimistic = false
	b.nextRefresh = now.Add(systemBarInterval)
	if b.text == text {
		d.mu.Unlock()
		return
	}
	old := b.bounds
	b.text = text
	width := 2*systemBarPadding + len(text)*systemBarFontCellWidth
	l := d.screen.layout
	b.bounds = image.Rect(max(0, l.width-width), max(0, l.height-2*systemBarPadding-systemBarFontCellHeight), l.width, l.height)
	// Keep the right edge (including audio output) visible on a narrow display.
	b.textLeft = l.width - width + systemBarPadding
	b.audioLeft = -1
	b.volumeDownLeft = -1
	b.volumeUpLeft = -1
	b.brightnessDownLeft = -1
	b.brightnessUpLeft = -1
	b.screenshotLeft = -1
	b.screenshotRight = -1
	b.clipboardLeft = -1
	b.clipboardRight = -1
	b.newTerminalLeft, b.newTerminalRight = -1, -1
	if audioLeft >= 0 {
		b.audioLeft = b.textLeft + audioLeft*systemBarFontCellWidth
	}
	if volumeDownLeft >= 0 {
		b.volumeDownLeft = b.textLeft + volumeDownLeft*systemBarFontCellWidth
		b.volumeUpLeft = b.textLeft + volumeUpLeft*systemBarFontCellWidth
	}
	if brightnessDownLeft >= 0 {
		b.brightnessDownLeft = b.textLeft + brightnessDownLeft*systemBarFontCellWidth
		b.brightnessUpLeft = b.textLeft + brightnessUpLeft*systemBarFontCellWidth
	}
	b.screenshotLeft = b.textLeft + screenshotLeft*systemBarFontCellWidth
	b.screenshotRight = b.textLeft + screenshotRight*systemBarFontCellWidth
	if clipboardLeft >= 0 {
		b.clipboardLeft = b.textLeft + clipboardLeft*systemBarFontCellWidth
		b.clipboardRight = b.textLeft + clipboardRight*systemBarFontCellWidth
	}
	if newTerminalLeft >= 0 {
		b.newTerminalLeft = b.textLeft + newTerminalLeft*systemBarFontCellWidth
		b.newTerminalRight = b.textLeft + newTerminalRight*systemBarFontCellWidth
	}
	b.pixels = make([]uint32, b.bounds.Dx()*b.bounds.Dy())
	for y := b.bounds.Min.Y; y < b.bounds.Max.Y; y++ {
		for x := b.bounds.Min.X; x < b.bounds.Max.X; x++ {
			b.pixels[(y-b.bounds.Min.Y)*b.bounds.Dx()+x-b.bounds.Min.X], _ = b.pixel(x, y)
		}
	}
	// If the battery disappears or its percentage shrinks, restore the uncovered
	// area from retained application pixels. Inactive VTs retain damage without
	// presenting; acquire composes the most recently cached bar on return.
	d.invalidate(old.Union(b.bounds))
	widget := b.widget
	bounds := b.bounds
	pixels := b.pixels
	d.mu.Unlock()
	if widget != nil {
		d.BeginPaint()
		widget.SetBounds(bounds)
		widget.Upload(pixels, bounds.Dx(), 0, 0, bounds.Dx(), bounds.Dy())
		d.EndPaint()
	}
}

func systemBarText(now time.Time, powerSupplyPath string) string {
	text := now.Format("2006-01-02 15:04:05")
	if percent, ok := batteryPercentage(powerSupplyPath); ok {
		text += "  " + string([]byte{systemBarBatteryIcon}) + " " + strconv.Itoa(percent) + "%"
	}
	return text
}

func (d *Driver) systemBarClick(x, y int) {
	d.mu.Lock()
	b := d.systemBar
	if d.closed || b == nil {
		d.mu.Unlock()
		return
	}
	point := image.Pt(x+b.bounds.Min.X, y+b.bounds.Min.Y)
	if !point.In(b.bounds) {
		d.mu.Unlock()
		return
	}
	if b.newTerminalTab != nil && b.newTerminalLeft >= 0 && point.X >= b.newTerminalLeft && point.X < b.newTerminalRight {
		open := b.newTerminalTab
		d.mu.Unlock()
		open()
		return
	}
	screenshot := b.screenshotLeft >= 0 && point.X >= b.screenshotLeft && point.X < b.screenshotRight
	if screenshot {
		d.screenshot.stage = screenshotFirstPoint
		d.screenshot.first = image.Point{}
		b.nextRefresh = time.Time{}
		d.mu.Unlock()
		d.refreshSystemBar(time.Now())
		return
	}
	down := b.volumeDownLeft >= 0 && point.X >= b.volumeDownLeft && point.X < b.volumeDownLeft+systemBarFontCellWidth
	up := b.volumeUpLeft >= 0 && point.X >= b.volumeUpLeft && point.X < b.volumeUpLeft+systemBarFontCellWidth
	if b.setAudioVolume != nil && (down || up) {
		next := b.volumeLevel
		if down {
			next = max(0, next-systemBarVolumeDownStep)
		} else {
			next = min(systemBarMaxVolume, next+systemBarVolumeUpStep)
		}
		b.volumeLevel = next
		b.nextRefresh = time.Time{}
		setVolume := b.setAudioVolume
		d.mu.Unlock()
		setVolume(next)
		return
	}
	brightnessDown := b.brightnessDownLeft >= 0 && point.X >= b.brightnessDownLeft && point.X < b.brightnessDownLeft+systemBarFontCellWidth
	brightnessUp := b.brightnessUpLeft >= 0 && point.X >= b.brightnessUpLeft && point.X < b.brightnessUpLeft+systemBarFontCellWidth
	if b.changeBrightness != nil && b.brightnessValid && (brightnessDown || brightnessUp) {
		delta := systemBarBrightnessUpStep
		if brightnessDown {
			delta = -systemBarBrightnessDownStep
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
		return
	}
	clipboard := b.clipboardLeft >= 0 && point.X >= b.clipboardLeft && point.X < b.clipboardRight
	if clipboard && d.clipboardSync != nil {
		state := d.ClipboardSyncState()
		if state.Available {
			b.nextRefresh = time.Time{}
			d.mu.Unlock()
			d.SetClipboardSync(!state.Enabled)
			return
		}
	}
	if b.audioOutput == nil || b.audioLeft < 0 {
		d.mu.Unlock()
		return
	}
	setOutput, setVolume := b.setAudioOutput, b.setAudioVolume
	current, known := b.displayAudioMode, b.displayAudioModeValid
	next := 0 // Unknown state starts at the explicit speaker position.
	if known && (current == 0 || current == 1) {
		next = 1 - current
	}
	// Flip the UI state before touching ALSA. The native event callback must
	// never make the visual checker depend on mixer permissions or latency.
	b.audioMode, b.audioModeValid = next, true
	b.displayAudioMode, b.displayAudioModeValid = next, true
	volume := b.volumeLevel
	b.nextRefresh = time.Time{}
	d.mu.Unlock()
	if setOutput == nil {
		return
	}
	if !setOutput(next) {
		// Keep the optimistic checker state visible; a later successful mixer
		// read can reconcile it without losing the user's click feedback.
		return
	}
	// Speaker and headphone elements may have independent levels. Apply the
	// displayed level after a successful route change so switching cannot jump.
	if setVolume != nil {
		setVolume(volume)
	}
}

// Use the first readable, present system battery in stable name order. Supply
// names need not be BAT0; sysfs entries are commonly symlinks. Device-scoped
// batteries (e.g. wireless mice) and AC/USB adapters are not the system battery.
func batteryPercentage(root string) (int, bool) {
	entries, err := os.ReadDir(root)
	if err != nil {
		return 0, false
	}
	for _, entry := range entries {
		base := filepath.Join(root, entry.Name())
		if powerSupplyValue(filepath.Join(base, "type")) != "Battery" ||
			powerSupplyValue(filepath.Join(base, "present")) == "0" ||
			powerSupplyValue(filepath.Join(base, "scope")) == "Device" {
			continue
		}
		percent, err := strconv.Atoi(powerSupplyValue(filepath.Join(base, "capacity")))
		if err == nil && percent >= 0 && percent <= 100 {
			return percent, true
		}
	}
	return 0, false
}

func powerSupplyValue(path string) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	// Kernel power-supply attributes are tiny ASCII values. Bound reads even
	// when a test/deployment supplies an unexpected filesystem at this path.
	data, err := io.ReadAll(io.LimitReader(f, 128))
	if err != nil || len(data) == 128 {
		return ""
	}
	return strings.TrimSpace(string(data))
}

func (b *systemBar) pixel(x, y int) (uint32, bool) {
	if b == nil || !image.Pt(x, y).In(b.bounds) {
		return 0, false
	}
	x, y = x-b.textLeft, y-b.bounds.Min.Y-systemBarPadding
	if x >= 0 && y >= 0 && y < systemBarFontCellHeight {
		at, col := x/systemBarFontCellWidth, x%systemBarFontCellWidth
		if at < len(b.text) {
			ch := b.text[at]
			if ch < 128 && systemBarGlyphs[ch] != nil {
				return systemBarGlyphs[ch][y*systemBarFontCellWidth+col], true
			}
			if glyph := systemBarIcons[ch]; glyph != nil {
				return glyph[y*systemBarFontCellWidth+col], true
			}
		}
	}
	return systemBarBackground, true
}
