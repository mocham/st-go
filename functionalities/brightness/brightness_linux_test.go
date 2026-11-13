//go:build linux

package brightness

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
)

func writeBacklight(t *testing.T, root, name, level, maximum string) {
	t.Helper()
	base := filepath.Join(root, name)
	if err := os.MkdirAll(base, 0700); err != nil {
		t.Fatal(err)
	}
	for file, value := range map[string]string{"brightness": level, "max_brightness": maximum} {
		if err := os.WriteFile(filepath.Join(base, file), []byte(value), 0600); err != nil {
			t.Fatal(err)
		}
	}
}

func TestBacklightBrightnessAndLogin1Change(t *testing.T) {
	root := t.TempDir()
	writeBacklight(t, root, "panel", "3\n", "100\n")
	device, level, maximum, ok := backlightBrightness(root)
	if !ok || device != "panel" || level != 3 || maximum != 100 {
		t.Fatal("backlight was not discovered", device, level, maximum, ok)
	}
	var args []string
	run := func(got ...string) error { args = slices.Clone(got); return nil }
	if next, ok := changeBacklightBrightness(root, -4, run); !ok || next != 0 {
		t.Fatal("brightness-down did not clamp at zero", next, ok)
	}
	wantSuffix := []string{"string:backlight", "string:panel", "uint32:0"}
	if len(args) < len(wantSuffix) || !slices.Equal(args[len(args)-len(wantSuffix):], wantSuffix) {
		t.Fatal("login1 brightness arguments are wrong", args)
	}
	writeBacklight(t, root, "panel", "98\n", "100\n")
	if next, ok := changeBacklightBrightness(root, 5, run); !ok || next != 100 || args[len(args)-1] != "uint32:100" {
		t.Fatal("brightness-up did not clamp at maximum", next, ok, args)
	}
}

func TestBacklightRejectsUnavailableOrInvalidDevices(t *testing.T) {
	root := t.TempDir()
	if _, _, _, ok := backlightBrightness(root); ok {
		t.Fatal("empty backlight tree was accepted")
	}
	writeBacklight(t, root, "panel", "101", "100")
	if _, _, _, ok := backlightBrightness(root); ok {
		t.Fatal("out-of-range backlight value was accepted")
	}
	if _, ok := changeBacklightBrightness(root, 5, func(...string) error { return nil }); ok {
		t.Fatal("invalid backlight was changed")
	}
}
