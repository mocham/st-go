//go:build linux

package fb

import (
	"image"
	"slices"
	"strconv"
	"testing"
	"time"

	"st-go/platform_neutral/api"
	"st-go/widgets"
)

func systemBarKeyboard(t *testing.T, d *Driver) (api.Widget, func(...byte)) {
	t.Helper()
	d.refreshSystemBar(time.Now())
	root, err := widgets.Wrap(d)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(root.Close)
	d.callbacks.Lock()
	d.callbacks.Focus(true)
	d.callbacks.Unlock()
	d.keyboard.maps[0][11] = '0'
	return root, func(data ...byte) {
		d.callbacks.Lock()
		defer d.callbacks.Unlock()
		d.keyboard.feed(data, d.callbacks, func(int) { t.Fatal("unexpected VT switch") }, func() { t.Fatal("unexpected close") })
	}
}

func TestSystemBarKeyboardFocusNavigationAndActivation(t *testing.T) {
	for _, width := range []int{1280, 1600} {
		t.Run(strconv.Itoa(width), func(t *testing.T) {
			d := memoryDriver(t, width, 100)
			volume, mode, reads := 99, 0, 0
			d.systemBar = &systemBar{
				powerSupplyPath: t.TempDir(),
				volumeLevel:     volume,
				setAudioVolume:  func(next int) bool { volume = next; return true },
				audioOutput:     func() (int, bool) { reads++; return mode, true },
				setAudioOutput:  func(next int) bool { mode = next; return true },
			}
			root, feed := systemBarKeyboard(t, d)
			var keys []uint
			var text string
			root.Bind(api.Callbacks{KeyPress: func(key uint, _ uint16) { keys = append(keys, key) }, Text: func(s string) { text += s }})
			before := slices.Clone(d.pixels)
			b := d.systemBar
			feed(56, 11, 11|128, 56|128) // Alt+0
			if b.focusWindow.FocusedWidget().ID() != b.widget.ID() || !b.focused || b.focusBounds.Empty() {
				t.Fatal("Alt+0 did not focus and highlight the system bar")
			}
			if got := pixelAt(d, b.focusBounds.Min.X, b.focusBounds.Min.Y); got != systemBarForeground&0xffffff {
				t.Fatalf("focus outline was not presented: %#x", got)
			}
			oldWidth, oldReads := b.bounds.Dx(), reads
			feed(15, 15|128) // Tab: volume down -> volume up
			if reads != oldReads {
				t.Fatal("keyboard navigation polled the mixer")
			}
			feed(28, 28|128) // Enter: 99 -> 100, changing the bar width
			if volume != 100 || b.bounds.Dx() <= oldWidth || b.focusedControl != systemBarVolumeUp {
				t.Fatal("Enter lost its selected control across a layout change", volume, b.focusedControl)
			}
			feed(42, 15, 15|128, 42|128, 28, 28|128) // Shift+Tab, Enter: volume down
			if volume != 96 {
				t.Fatal("backward navigation did not activate volume down", volume)
			}
			feed(42, 15, 15|128, 42|128, 28, 28|128) // wrap backward to output selector
			if mode != 1 {
				t.Fatal("keyboard navigation did not wrap to the output selector")
			}
			feed(15, 15|128, 28, 28|128) // wrap forward to volume down
			if volume != 92 {
				t.Fatal("keyboard navigation did not wrap to the first control", volume)
			}
			feed(30, 30|128, 127, 255) // ASCII and Unicode must stay out of the PTY.
			if len(keys) != 0 || text != "" || !slices.Equal(before, d.pixels) {
				t.Fatal("system bar input or highlighting leaked into the terminal", keys, text)
			}
			// Losing native focus (VT switch) hides the outline but retains the return
			// target, and acquiring it again restores the selected control.
			d.callbacks.Lock()
			d.callbacks.Focus(false)
			if !b.focusBounds.Empty() {
				t.Error("inactive VT retained its focus outline")
			}
			d.callbacks.Focus(true)
			d.callbacks.Unlock()
			feed(1, 1|128, 15, 15|128, 28, 28|128) // Escape, then ordinary PTY Tab/Enter
			if b.focused || !b.focusBounds.Empty() || b.previousFocus != nil || b.focusWindow.FocusedWidget().ID() != root.ID() || !slices.Equal(keys, []uint{0xff09, 0xff0d}) {
				t.Fatal("Escape did not restore terminal keyboard input", keys)
			}
		})
	}
}

func TestSystemBarKeyboardNewTerminalAndPointerFocus(t *testing.T) {
	d := memoryDriver(t, 960, 100)
	d.systemBar = &systemBar{powerSupplyPath: t.TempDir()}
	root, feed := systemBarKeyboard(t, d)
	var child api.Widget
	root.(api.TerminalTabButtonHost).BindNewTerminalTab(func() {
		var err error
		child, err = root.NewWidget(api.WidgetOptions{Bounds: image.Rect(0, 0, 960, 40)})
		if err != nil {
			t.Error(err)
			return
		}
		child.FocusWidget()
	})
	feed(56, 11, 11|128, 56|128, 15, 15|128, 15, 15|128, 28, 28|128) // Alt+0, Tab past screenshot/recording, Enter
	b := d.systemBar
	if child == nil {
		t.Fatal("Enter did not activate the new-terminal button")
	}
	defer child.Close()
	if b.focused || b.focusWindow.FocusedWidget().ID() != child.ID() {
		t.Fatal("new terminal did not take keyboard focus")
	}
	feed(56, 11, 11|128, 56|128)
	root.(api.TerminalTabButtonHost).BindNewTerminalTab(nil)
	if b.focusBounds.Empty() || b.focusedControl != systemBarScreenshot {
		t.Fatal("removing the selected control did not select a remaining button")
	}
	feed(28, 28|128)
	if d.screenshot.stage != screenshotFirstPoint {
		t.Fatal("remaining screenshot control did not activate")
	}
	feed(1, 1|128)
	if b.focusWindow.FocusedWidget().ID() != child.ID() {
		t.Fatal("Escape returned to the original PTY instead of the latest one")
	}
	d.mu.Lock()
	d.screenshot.stage = screenshotIdle
	d.mu.Unlock()
	feed(56, 11, 11|128, 56|128)
	d.callbacks.Lock()
	d.callbacks.PointerPress(1, 0, 1, 1)
	d.callbacks.PointerRelease(1, 0, 1, 1)
	d.callbacks.Unlock()
	if b.focused || !b.focusBounds.Empty() || b.focusWindow.FocusedWidget().ID() != child.ID() {
		t.Fatal("clicking the terminal did not leave system-bar keyboard focus")
	}
}

func TestSystemBarKeyboardSkipsClippedButtons(t *testing.T) {
	d := memoryDriver(t, 80, 100)
	volume, mode := 33, 0
	d.systemBar = &systemBar{
		powerSupplyPath: t.TempDir(),
		volumeLevel:     volume,
		setAudioVolume:  func(next int) bool { volume = next; return true },
		audioOutput:     func() (int, bool) { return mode, true },
		setAudioOutput:  func(next int) bool { mode = next; return true },
	}
	_, feed := systemBarKeyboard(t, d)
	feed(56, 11, 11|128, 56|128, 15, 15|128, 28, 28|128)
	if mode != 1 || volume != 33 || d.systemBar.focusedControl != systemBarAudioOutput {
		t.Fatal("narrow-screen navigation selected a clipped control", mode, volume)
	}
}
