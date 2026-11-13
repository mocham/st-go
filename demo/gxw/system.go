package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

type systemStatus struct {
	battery, volume, brightness int // -1 means unavailable.
	muted                       bool
	mixer, backlight            string
	maximum, rawBrightness      int
	err                         string
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

func backlight(root string) (path string, level, maximum int) {
	entries, _ := os.ReadDir(root)
	for _, e := range entries {
		base := filepath.Join(root, e.Name())
		n, e1 := strconv.Atoi(readValue(filepath.Join(base, "brightness")))
		m, e2 := strconv.Atoi(readValue(filepath.Join(base, "max_brightness")))
		if e1 == nil && e2 == nil && m > 0 && n >= 0 && n <= m {
			return base, n, m
		}
	}
	return "", 0, 0
}

func command(ctx context.Context, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, args[0], args[1:]...)
	cmd.Env = append(os.Environ(), "LC_ALL=C")
	b, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("%s: %w", args[0], err)
	}
	return strings.TrimSpace(string(b)), nil
}

var percentRE = regexp.MustCompile(`(\d+)%`)

func volume(ctx context.Context) (backend string, level int, muted bool) {
	if s, err := command(ctx, "wpctl", "get-volume", "@DEFAULT_AUDIO_SINK@"); err == nil {
		parts := strings.Fields(s)
		if len(parts) >= 2 {
			if f, err := strconv.ParseFloat(parts[1], 64); err == nil {
				return "wpctl", int(f*100 + 0.5), strings.Contains(s, "MUTED")
			}
		}
	}
	if s, err := command(ctx, "pactl", "get-sink-volume", "@DEFAULT_SINK@"); err == nil {
		if m := percentRE.FindStringSubmatch(s); len(m) > 1 {
			n, _ := strconv.Atoi(m[1])
			mute, _ := command(ctx, "pactl", "get-sink-mute", "@DEFAULT_SINK@")
			return "pactl", n, strings.Contains(mute, "yes")
		}
	}
	if s, err := command(ctx, "amixer", "get", "Master"); err == nil {
		if m := percentRE.FindStringSubmatch(s); len(m) > 1 {
			n, _ := strconv.Atoi(m[1])
			return "amixer", n, strings.Contains(s, "[off]")
		}
	}
	return "", -1, false
}

func pollSystem(ctx context.Context) systemStatus {
	s := systemStatus{battery: batteryPercent("/sys/class/power_supply"), brightness: -1}
	s.mixer, s.volume, s.muted = volume(ctx)
	s.backlight, s.rawBrightness, s.maximum = backlight("/sys/class/backlight")
	if s.maximum > 0 {
		s.brightness = (100*s.rawBrightness + s.maximum/2) / s.maximum
	}
	return s
}

func changeSystem(ctx context.Context, s systemStatus, action string) error {
	var args []string
	switch action {
	case "vol+", "vol-", "mute":
		next := s.volume + 3
		if action == "vol-" {
			next = s.volume - 4
		}
		next = max(0, min(100, next))
		switch s.mixer {
		case "wpctl":
			args = []string{"wpctl", "set-volume", "-l", "1", "@DEFAULT_AUDIO_SINK@", fmt.Sprintf("%d%%", next)}
			if action == "mute" {
				args = []string{"wpctl", "set-mute", "@DEFAULT_AUDIO_SINK@", "toggle"}
			}
		case "pactl":
			args = []string{"pactl", "set-sink-volume", "@DEFAULT_SINK@", fmt.Sprintf("%d%%", next)}
			if action == "mute" {
				args = []string{"pactl", "set-sink-mute", "@DEFAULT_SINK@", "toggle"}
			}
		case "amixer":
			args = []string{"amixer", "set", "Master", fmt.Sprintf("%d%%", next)}
			if action == "mute" {
				args = []string{"amixer", "set", "Master", "toggle"}
			}
		default:
			return fmt.Errorf("no working wpctl, pactl or amixer volume control")
		}
	case "bright+", "bright-":
		if s.backlight == "" {
			return fmt.Errorf("no backlight device")
		}
		step := max(1, s.maximum*3/100)
		if action == "bright-" {
			step = -max(1, s.maximum*4/100)
		}
		value := max(1, min(s.maximum, s.rawBrightness+step))
		if err := os.WriteFile(filepath.Join(s.backlight, "brightness"), []byte(strconv.Itoa(value)), 0644); err == nil {
			return nil
		}
		args = []string{"brightnessctl", "-d", filepath.Base(s.backlight), "set", strconv.Itoa(value)}
	default:
		return fmt.Errorf("unknown system action %q", action)
	}
	_, err := command(ctx, args...)
	return err
}

func systemWorker(ctx context.Context, jobs <-chan string, updates chan<- systemStatus) {
	s := pollSystem(ctx)
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
	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case action := <-jobs:
			err := changeSystem(ctx, s, action)
			s = pollSystem(ctx)
			if err != nil {
				s.err = err.Error()
			}
		case <-ticker.C:
			s = pollSystem(ctx)
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
