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
	systemBarBatteryIcon  = byte(0x80)
	systemBarSpeakerIcon  = byte(0x81)
	systemBarEarphoneIcon = byte(0x82)
	systemBarAudioIcon    = byte(0x83)
)

const (
	systemBarInterval   = 30 * time.Second
	systemBarPadding    = 8
	systemBarBackground = 0xff2d2b46 // Muted dark blue, slightly purple.
	systemBarForeground = 0xffc4dfb5 // Soft light green.
)

// Backend-owned presentation state, protected by Driver.mu. No rows are
// reserved in the terminal and no system information is sent to applications.
type systemBar struct {
	powerSupplyPath string
	nextRefresh     time.Time
	text            string
	bounds          image.Rectangle
	textLeft        int
	pixels          []uint32
	widget          api.Widget
	audioOutput     func() (mode int, known bool)
	setAudioOutput  func(mode int) bool
	audioLeft       int
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
	d.mu.Unlock()

	text := systemBarText(now, root)
	audioLeft := -1
	if output != nil {
		mode, known := output()
		switch {
		case !known || mode != 0 && mode != 1:
			audioLeft = len(text)
			text += string([]byte{' ', ' ', systemBarAudioIcon})
		case mode == 1:
			audioLeft = len(text)
			text += string([]byte{' ', ' ', systemBarEarphoneIcon})
		default:
			audioLeft = len(text)
			text += string([]byte{' ', ' ', systemBarSpeakerIcon})
		}
	}
	d.mu.Lock()
	if d.closed {
		d.mu.Unlock()
		return
	}
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
	if audioLeft >= 0 {
		b.audioLeft = b.textLeft + audioLeft*systemBarFontCellWidth
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
	if d.closed || b == nil || b.audioOutput == nil || b.audioLeft < 0 ||
		!image.Pt(x+b.bounds.Min.X, y+b.bounds.Min.Y).In(b.bounds) || x+b.bounds.Min.X < b.audioLeft {
		d.mu.Unlock()
		return
	}
	set := b.setAudioOutput
	current, known := b.audioOutput()
	d.mu.Unlock()
	if set == nil || !known || (current != 0 && current != 1) {
		return
	}
	if !set(1 - current) {
		return
	}
	// The input callback runs in the native event serialization domain. Defer
	// the repaint to the normal loop so widget callbacks cannot re-enter it.
	d.mu.Lock()
	b.nextRefresh = time.Time{}
	d.mu.Unlock()
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
