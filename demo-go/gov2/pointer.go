package main

import (
	"image"

	"st-go/platform_neutral/api"
)

type statusButton struct {
	rect       image.Rectangle
	label, key string
}

// Drawing and hit testing use the same pixel rectangles, including small modes.
func statusLayout(w, h int) (image.Rectangle, []statusButton) {
	track := image.Rect(4, h-bottomHeight, w-4, h-bottomHeight+16)
	labels := []string{"Pause", "-5s", "+5s", "Slower", "Faster", "Vol -", "Vol +", "Mute", "Quit"}
	if w < 540 {
		labels = []string{"P/P", "-5s", "+5s", "[", "]", "-", "+", "Mute", "Quit"}
	}
	keys := []string{" ", "left", "right", "[", "]", "-", "+", "m", "q"}
	buttons := make([]statusButton, len(keys))
	for i := range buttons {
		buttons[i] = statusButton{image.Rect(4+(w-8)*i/len(keys), h-22, 4+(w-8)*(i+1)/len(keys)-1, h-1), labels[i], keys[i]}
	}
	return track, buttons
}

type pointerEvent struct {
	dx, dy, wheel, horizontal int
	button                    byte
	down, cancel              bool
}

type pointerControls struct {
	position          image.Point
	visible, dragging bool
	pressed           string
	pendingSeek       *api.VideoRequest
}

func (c *pointerControls) reset() {
	c.dragging, c.visible, c.pressed, c.pendingSeek = false, false, "", nil
}

// Seeks are replaceable within an input batch: high-rate dragging must not fill
// the decoder command queue with obsolete positions.
func (c *pointerControls) event(e pointerEvent, w, h int, s api.VideoStatus, key func(string)) {
	if e.cancel {
		c.reset()
		return
	}
	c.visible = true
	c.position.X = max(0, min(w-1, c.position.X+e.dx))
	c.position.Y = max(0, min(h-1, c.position.Y+e.dy))
	track, buttons := statusLayout(w, h)
	hit := ""
	for _, b := range buttons {
		if c.position.In(b.rect) {
			hit = b.key
			break
		}
	}
	seek := func() {
		if s.DurationMS > 0 && track.Dx() > 1 {
			x := max(0, min(track.Dx()-1, c.position.X-track.Min.X))
			c.pendingSeek = &api.VideoRequest{Action: "seek", PositionMS: int64(float64(s.DurationMS) * float64(x) / float64(track.Dx()-1))}
		}
	}
	if e.button == 1 {
		if e.down {
			c.dragging = c.position.In(track)
			c.pressed = hit
			if c.dragging {
				seek()
			}
		} else {
			if c.dragging {
				seek()
			} else if c.pressed != "" && c.pressed == hit {
				key(hit)
			}
			c.dragging, c.pressed = false, ""
		}
	} else if c.dragging && (e.dx != 0 || e.dy != 0) {
		seek()
	}
	if e.wheel != 0 || e.horizontal != 0 {
		up, down := "", ""
		switch {
		case c.position.In(track):
			up, down = "right", "left"
		case hit == "[" || hit == "]":
			up, down = "]", "["
		case hit == "-" || hit == "+" || hit == "m":
			up, down = "+", "-"
		}
		steps := e.wheel + e.horizontal
		if steps < 0 {
			up, steps = down, -steps
		}
		if up != "" {
			for range min(32, steps) {
				key(up)
			}
		}
	}
}
