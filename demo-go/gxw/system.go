package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"st-go/audio"
	"st-go/brightness"
	"st-go/widgets/systembar"
)

type systemStatus struct {
	sampledAt                   time.Time
	battery, volume, brightness int // -1 means unavailable.
	muted                       bool
	err                         string
}

type systemControls struct {
	volume                                     func() (int, bool)
	setVolume                                  func(int) bool
	brightness                                 func() (int, int, bool)
	changeBrightness                           func(int) (int, bool)
	volumeKnown                                bool
	volumeLevel, volumeReadback, restoreVolume int
}

func newSystemControls() *systemControls {
	return &systemControls{volume: audio.Volume, setVolume: audio.SetVolume, brightness: brightness.Read, changeBrightness: brightness.Change}
}

func (c *systemControls) readVolume() (int, bool) {
	actual, ok := c.volume()
	if !ok {
		c.volumeKnown = false
		return 0, false
	}
	// Keep successful requested levels across quantized hardware readbacks,
	// like the framebuffer bar, so small repeated steps can accumulate.
	if !c.volumeKnown || actual != c.volumeReadback {
		c.volumeLevel = actual
	}
	c.volumeKnown, c.volumeReadback = true, actual
	return c.volumeLevel, true
}

func readValue(path string) string { b, _ := os.ReadFile(path); return strings.TrimSpace(string(b)) }

func batteryPercent(root string) int {
	entries, _ := os.ReadDir(root)
	for _, e := range entries {
		base := filepath.Join(root, e.Name())
		if readValue(filepath.Join(base, "type")) != "Battery" || readValue(filepath.Join(base, "scope")) == "Device" || readValue(filepath.Join(base, "present")) == "0" {
			continue
		}
		if n, err := strconv.Atoi(readValue(filepath.Join(base, "capacity"))); err == nil && n >= 0 && n <= 100 {
			return n
		}
	}
	return -1
}

func (c *systemControls) poll() systemStatus {
	s := systemStatus{sampledAt: time.Now(), battery: batteryPercent("/sys/class/power_supply"), volume: -1, brightness: -1}
	if level, ok := c.readVolume(); ok {
		s.volume = level
		s.muted = level == 0
	}
	if level, _, ok := c.brightness(); ok {
		s.brightness = level
	}
	return s
}

func (c *systemControls) change(action string) error {
	switch action {
	case "vol+", "vol-", "mute":
		level, ok := c.readVolume()
		if !ok {
			return fmt.Errorf("selected ALSA output volume unavailable")
		}
		next := level + systembar.VolumeUpStep
		if action == "vol-" {
			next = level - systembar.VolumeDownStep
		}
		if action == "mute" {
			next = 0
			if level == 0 {
				if c.restoreVolume == 0 {
					return fmt.Errorf("no previous volume to restore; use volume up")
				}
				next = c.restoreVolume
			}
		}
		next = max(0, min(systembar.MaxVolume, next))
		if !c.setVolume(next) {
			return fmt.Errorf("set ALSA output volume failed")
		}
		if next == 0 && level > 0 {
			c.restoreVolume = level
		} else if next > 0 {
			c.restoreVolume = next
		}
		c.volumeLevel = next
		c.volumeReadback, c.volumeKnown = c.volume()
		return nil
	case "bright+", "bright-":
		step := systembar.BrightnessUpStep
		if action == "bright-" {
			step = -systembar.BrightnessDownStep
		}
		if _, ok := c.changeBrightness(step); !ok {
			return fmt.Errorf("set logind backlight brightness failed")
		}
		return nil
	default:
		return fmt.Errorf("unknown system action %q", action)
	}
}

func systemWorker(ctx context.Context, jobs <-chan string, updates chan<- systemStatus) {
	controls := newSystemControls()
	s := controls.poll()
	send := func() bool {
		select {
		case updates <- s:
			return true
		case <-ctx.Done():
			return false
		}
	}
	if !send() {
		return
	}
	ticker := time.NewTicker(systembar.PollInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case action := <-jobs:
			err := controls.change(action)
			s = controls.poll()
			if err != nil {
				s.err = err.Error()
			}
		case <-ticker.C:
			s = controls.poll()
		}
		if !send() {
			return
		}
	}
}

func (w *wm) systemAction(action string) {
	select {
	case w.jobs <- action:
	default:
		w.notice("System controls busy")
	}
}
