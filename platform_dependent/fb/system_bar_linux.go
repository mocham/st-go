//go:build linux

package fb

import (
	"fmt"
	"image"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
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
	d.mu.Unlock()

	text := systemBarText(now, root)
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.closed {
		return
	}
	b.nextRefresh = now.Add(systemBarInterval)
	if b.text == text {
		return
	}
	old := b.bounds
	b.text = text
	width := 2*systemBarPadding + len(text)*systemBarFontCellWidth
	l := d.screen.layout
	b.bounds = image.Rect(max(0, l.width-width), 0, l.width, min(l.height, 2*systemBarPadding+systemBarFontCellHeight))
	// Keep the right edge (including battery) visible even on a narrow display.
	b.textLeft = l.width - width + systemBarPadding
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
}

func systemBarText(now time.Time, powerSupplyPath string) string {
	text := now.Format("2006-01-02 15:04:05")
	if percent, ok := batteryPercentage(powerSupplyPath); ok {
		text += fmt.Sprintf("  BAT %d%%", percent)
	}
	return text
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
	x, y = x-b.textLeft, y-systemBarPadding
	if x >= 0 && y >= 0 && y < systemBarFontCellHeight {
		at, col := x/systemBarFontCellWidth, x%systemBarFontCellWidth
		if at < len(b.text) {
			ch := b.text[at]
			if ch < 128 && systemBarGlyphs[ch] != nil {
				return systemBarGlyphs[ch][y*systemBarFontCellWidth+col], true
			}
		}
	}
	return systemBarBackground, true
}
