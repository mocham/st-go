package main

import (
	"fmt"
	"io"
	"math"
	"path/filepath"
	"strings"
	"unicode"

	"st-go/platform_neutral/api"
	"st-go/tui/video"
)

type viewer struct {
	out                               io.Writer
	client                            *video.Client
	options                           options
	status                            api.VideoStatus
	cols, rows                        int
	ready, quit                       bool
	err                               error
	lastTop, lastBottom, lastProgress string
	pendingSpeed                      float64
	pendingVolume                     *int
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
	height := v.rows - 2
	if v.progressRow() != 0 {
		height--
	}
	return api.Rect{X: 1, Y: 2, Width: max(1, v.cols), Height: height}
}

func (v *viewer) progressRow() int {
	if v.rows < 4 || v.cols < 4 {
		return 0
	}
	return v.rows - 1
}

func (v *viewer) speed() float64 {
	if v.status.Speed <= 0 {
		return 1
	}
	return v.status.Speed
}
func (v *viewer) reply(payload string) bool {
	s, ok := v.client.Reply(payload)
	if !ok {
		return false
	}
	v.status = s
	// Control acknowledgements may carry a snapshot from before the worker
	// applied the request. Keep repeated keys cumulative until it catches up.
	if v.pendingSpeed != 0 {
		if math.Abs(s.Speed-v.pendingSpeed) < 1e-9 {
			v.pendingSpeed = 0
		} else {
			v.status.Speed = v.pendingSpeed
		}
	}
	if v.pendingVolume != nil {
		if s.Volume == *v.pendingVolume {
			v.pendingVolume = nil
		} else {
			v.status.Volume = *v.pendingVolume
		}
	}
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
	case "]":
		r.Action = "speed"
		r.Speed = min(api.MaxVideoSpeed, v.speed()*1.1)
	case "[":
		r.Action = "speed"
		r.Speed = max(api.MinVideoSpeed, v.speed()/1.1)
	case "m":
		r.Action = "mute"
		r.Muted = !v.status.Muted
	default:
		return
	}
	v.send(r)
	if v.err == nil {
		if r.Action == "speed" {
			v.pendingSpeed, v.status.Speed = r.Speed, r.Speed
		}
		if r.Action == "volume" {
			v.pendingVolume = &r.Volume
			v.status.Volume = r.Volume
		}
	}
}

func (v *viewer) mouse(event mouseEvent) {
	row := v.progressRow()
	if !v.ready || row == 0 || event.y != row || !event.press || event.button&^28 != 0 || event.x < 1 || event.x > v.cols || v.status.DurationMS <= 0 {
		return
	}
	// Brackets clamp to the endpoints; track cells map from start to end.
	column := max(0, min(v.cols-3, event.x-2))
	position := int64(column) * v.status.DurationMS / int64(v.cols-3)
	v.send(api.VideoRequest{Action: "seek", PositionMS: position})
}

func (v *viewer) progress() string {
	width := v.cols - 2
	if v.status.DurationMS <= 0 {
		return "[" + strings.Repeat("-", width) + "]"
	}
	position := max(int64(0), min(v.status.DurationMS, v.status.PositionMS))
	head := int(position * int64(width-1) / v.status.DurationMS)
	return "[" + strings.Repeat("=", head) + ">" + strings.Repeat("-", width-head-1) + "]"
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
	top := fmt.Sprintf("gov | %.2fx | %s %s / %s | %s | %s", v.speed(), v.status.State, clock(v.status.PositionMS), clock(v.status.DurationMS), sound, safeText(filepath.Base(v.options.file)))
	bottom := "Space pause  arrows seek  [/] speed  +/- vol  m mute  q quit"
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
	if row := v.progressRow(); row != 0 {
		line(row, v.progress(), &v.lastProgress)
	}
	if v.rows > 1 {
		line(v.rows, bottom, &v.lastBottom)
	}
}
