package main

import (
	"fmt"
	"io"
	"path/filepath"
	"strings"
	"unicode"

	"st-go/platform_neutral/api"
	"st-go/tui/video"
)

type viewer struct {
	out                 io.Writer
	client              *video.Client
	options             options
	status              api.VideoStatus
	cols, rows          int
	ready, quit         bool
	err                 error
	lastTop, lastBottom string
}

func (v *viewer) send(r api.VideoRequest) {
	if v.err == nil {
		v.err = v.client.Send(r)
	}
}
func (v *viewer) bounds() api.Rect {
	if v.rows < 3 {
		return api.Rect{X: 1, Y: v.rows + 1, Width: 1, Height: 1}
	}
	return api.Rect{X: 1, Y: 2, Width: max(1, v.cols), Height: v.rows - 2}
}
func (v *viewer) reply(payload string) bool {
	s, ok := v.client.Reply(payload)
	if !ok {
		return false
	}
	v.status = s
	if !s.Supported {
		v.err = fmt.Errorf("MP4 playback requires the full st-fb build")
		return true
	}
	if s.State == "error" {
		v.err = fmt.Errorf("%s", s.Error)
		return true
	}
	if !v.ready {
		v.ready = true
		v.send(api.VideoRequest{Action: "open", File: v.options.file, Bounds: v.bounds(), Volume: v.options.volume, Muted: v.options.muted})
	}
	return true
}
func (v *viewer) key(key string) {
	if key == "q" || key == "escape" || key == "\x03" {
		v.quit = true
		return
	}
	if !v.ready {
		return
	}
	r := api.VideoRequest{}
	switch key {
	case " ", "k":
		r.Action = "toggle"
	case "left", "h":
		r.Action = "seek"
		r.PositionMS = max(0, v.status.PositionMS-5000)
	case "right", "l":
		r.Action = "seek"
		r.PositionMS = v.status.PositionMS + 5000
	case "0", "home":
		r.Action = "seek"
	case "+", "=", "up":
		r.Action = "volume"
		r.Volume = min(100, v.status.Volume+5)
	case "-", "down":
		r.Action = "volume"
		r.Volume = max(0, v.status.Volume-5)
	case "m":
		r.Action = "mute"
		r.Muted = !v.status.Muted
	default:
		return
	}
	v.send(r)
}
func safeText(s string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return ' '
		}
		return r
	}, s)
}
func clock(ms int64) string {
	seconds := max(0, ms/1000)
	return fmt.Sprintf("%02d:%02d:%02d", seconds/3600, seconds/60%60, seconds%60)
}
func (v *viewer) draw() {
	sound := fmt.Sprintf("vol %d%%", v.status.Volume)
	if v.status.Muted {
		sound = "muted"
	}
	top := fmt.Sprintf("gov | %s | %s %s / %s | %s", safeText(filepath.Base(v.options.file)), v.status.State, clock(v.status.PositionMS), clock(v.status.DurationMS), sound)
	bottom := "Space pause  arrows seek  0 restart  +/- volume  m mute  q quit"
	if v.status.Warning != "" {
		bottom = "Audio: " + safeText(v.status.Warning) + " | m mute / q quit"
	}
	line := func(row int, s string, last *string) {
		runes := []rune(s)
		if len(runes) > v.cols {
			s = string(runes[:v.cols])
		}
		if s == *last {
			return
		}
		*last = s
		if v.err == nil {
			_, v.err = fmt.Fprintf(v.out, "\x1b[%d;1H\x1b[0m\x1b[%dX%s", row, v.cols, s)
		}
	}
	line(1, top, &v.lastTop)
	if v.rows > 1 {
		line(v.rows, bottom, &v.lastBottom)
	}
}
