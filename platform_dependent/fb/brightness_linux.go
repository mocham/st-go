//go:build linux

package fb

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"time"
)

const systemBacklightPath = "/sys/class/backlight"

// backlightBrightness follows TEST-ASSETS/bright.go: use the first backlight
// device in stable directory order and operate on its raw kernel brightness.
func backlightBrightness(root string) (device string, level, maximum int, ok bool) {
	entries, err := os.ReadDir(root)
	if err != nil {
		return "", 0, 0, false
	}
	for _, entry := range entries {
		base := filepath.Join(root, entry.Name())
		level, err = strconv.Atoi(powerSupplyValue(filepath.Join(base, "brightness")))
		if err != nil || level < 0 {
			continue
		}
		maximum, err = strconv.Atoi(powerSupplyValue(filepath.Join(base, "max_brightness")))
		if err != nil || maximum <= 0 || level > maximum {
			continue
		}
		return entry.Name(), level, maximum, true
	}
	return "", 0, 0, false
}

func changeBacklightBrightness(root string, delta int, run func(args ...string) error) (int, bool) {
	device, level, maximum, ok := backlightBrightness(root)
	if !ok || run == nil {
		return 0, false
	}
	next := max(0, min(maximum, level+delta))
	args := []string{
		"--system",
		"--dest=org.freedesktop.login1",
		"--type=method_call",
		"--print-reply",
		"/org/freedesktop/login1/session/auto",
		"org.freedesktop.login1.Session.SetBrightness",
		"string:backlight",
		"string:" + device,
		"uint32:" + strconv.Itoa(next),
	}
	if err := run(args...); err != nil {
		return 0, false
	}
	return next, true
}

func systemBrightness() (level, maximum int, ok bool) {
	_, level, maximum, ok = backlightBrightness(systemBacklightPath)
	return level, maximum, ok
}

func setSystemBrightness(delta int) (int, bool) {
	return changeBacklightBrightness(systemBacklightPath, delta, func(args ...string) error {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, "dbus-send", args...)
		cmd.Stdout, cmd.Stderr = io.Discard, io.Discard
		if err := cmd.Run(); err != nil {
			return fmt.Errorf("set brightness: %w", err)
		}
		return nil
	})
}
