package main

import "st-go/platform_neutral/api"

type controls struct {
	volume  int
	muted   bool
	speed   float64
	pending []api.VideoRequest
}

func (c *controls) queue(r api.VideoRequest) {
	if r.Action == "" {
		return
	}
	// Keep only the latest absolute adjustment, but preserve pause transitions.
	if r.Action != "toggle" {
		for i := len(c.pending) - 1; i >= 0; i-- {
			if c.pending[i].Action == "toggle" {
				break
			}
			if c.pending[i].Action == r.Action {
				c.pending[i] = r
				return
			}
		}
	}
	if len(c.pending) < 64 {
		c.pending = append(c.pending, r)
	}
}

// Keep adjustments cumulative even while the decoder is applying earlier keys.
func (c *controls) key(key string, status api.VideoStatus) (api.VideoRequest, bool) {
	r := api.VideoRequest{}
	switch key {
	case "q", "escape", "\x03":
		return r, true
	case " ", "k":
		r.Action = "toggle"
	case "left", "h":
		r.Action, r.PositionMS = "seek", max(0, status.PositionMS-5000)
	case "right", "l":
		r.Action, r.PositionMS = "seek", status.PositionMS+5000
	case "0", "home":
		r.Action = "seek"
	case "+", "=", "up":
		c.volume = min(100, c.volume+5)
		r.Action, r.Volume = "volume", c.volume
	case "-", "down":
		c.volume = max(0, c.volume-5)
		r.Action, r.Volume = "volume", c.volume
	case "]":
		c.speed = min(api.MaxVideoSpeed, c.speed*1.1)
		r.Action, r.Speed = "speed", c.speed
	case "[":
		c.speed = max(api.MinVideoSpeed, c.speed/1.1)
		r.Action, r.Speed = "speed", c.speed
	case "m":
		c.muted = !c.muted
		r.Action, r.Muted = "mute", c.muted
	}
	return r, false
}
