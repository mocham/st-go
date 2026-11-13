//go:build linux

package fb

import (
	"slices"
	"testing"

	"st-go/platform_neutral/api"
)

func TestSystemMediaKeysFromConsoleToHardware(t *testing.T) {
	// Controls are clipped on this display: hardware keys must still work.
	d := memoryDriver(t, 80, 100)
	volume, brightness := 33, 20
	var volumes, deltas []int
	d.systemBar = &systemBar{
		powerSupplyPath: t.TempDir(), volumeLevel: volume,
		setAudioVolume: func(next int) bool {
			d.mu.Lock() // Hardware access must not hold the drawing lock.
			defer d.mu.Unlock()
			volume = next
			volumes = append(volumes, next)
			return true
		},
		brightness: func() (int, int, bool) { return brightness, 100, true },
		changeBrightness: func(delta int) (int, bool) {
			d.mu.Lock()
			defer d.mu.Unlock()
			brightness = max(0, min(100, brightness+delta))
			deltas = append(deltas, delta)
			return brightness, true
		},
	}
	root, feed := systemBarKeyboard(t, d)
	var keys []uint
	root.Bind(api.Callbacks{KeyPress: func(key uint, _ uint16) { keys = append(keys, key) }, Text: func(string) { t.Fatal("media key emitted PTY text") }})
	feed(58, 58|128, 69, 69|128)          // Caps Lock and Num Lock do not affect shortcuts.
	feed(115, 115, 115|128, 114, 114|128) // Up, repeat up, release, down.
	// Extended medium-raw brightness records, split across reads.
	feed(0, 0x81)
	feed(0xe1, 0x80, 0x81, 0xe1, 0, 0x81, 0xe0, 0x80, 0x81, 0xe0)
	if volume != 35 || brightness != 21 || !slices.Equal(volumes, []int{36, 39, 35}) || !slices.Equal(deltas, []int{5, -4}) || len(keys) != 0 {
		t.Fatal("console media-key actions/repeats leaked or used incorrect steps", volume, brightness, volumes, deltas, keys)
	}
	if d.keyboard.pressed[224] || d.keyboard.pressed[225] || d.systemBar.nextRefresh.IsZero() {
		t.Fatal("extended release or immediate bar refresh was lost")
	}
	for _, modifier := range []byte{42, 29, 56, 100} { // Shift, Ctrl, Alt, AltGr.
		feed(modifier, 115, 115|128, modifier|128)
	}
	if len(volumes) != 3 {
		t.Fatal("modified media key changed volume")
	}
	keys = nil
	d.systemBar.widget.SetVisible(false)
	feed(115, 115|128)
	if volume != 38 || len(keys) != 0 {
		t.Fatal("hidden bar disabled global media shortcuts")
	}
}
