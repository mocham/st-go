package main

import (
	"image"

	"github.com/jezek/xgb/xproto"
)

type client struct {
	id, transient                    xproto.Window
	title                            string
	dialog, explicitDialog, modal    bool
	urgent, input, takeFocus         bool
	deleteWindow, fullscreen, mapped bool
	unmaps                           []uint16 // Sequences of our own UnmapWindow requests.
	original, bounds                 image.Rectangle
	border                           uint16
	requested                        image.Point
	hints                            sizeHints
}

type sizeHints struct{ min, max, base, increment image.Point }

// Fixed-size windows alone are not evidence of a dialog. Only an explicit
// EWMH dialog type or a live WM_TRANSIENT_FOR relationship floats a client.
func isDialog(explicit bool, parent, self, root xproto.Window, clients map[xproto.Window]*client) bool {
	return explicit || (parent != self && parent != root && clients[parent] != nil)
}

func family(id xproto.Window, clients map[xproto.Window]*client) xproto.Window {
	seen := make(map[xproto.Window]bool)
	for c := clients[id]; c != nil; c = clients[id] {
		if !c.dialog || c.transient == id || clients[c.transient] == nil || seen[c.transient] {
			break
		}
		seen[id] = true
		id = c.transient
	}
	return id
}

func dialogBounds(area image.Rectangle, requested image.Point, h sizeHints, border int) image.Rectangle {
	limit := image.Pt(max(1, area.Dx()-2*border), max(1, area.Dy()-2*border))
	size := requested
	if size.X <= 1 {
		size.X = min(640, limit.X*2/3)
	}
	if size.Y <= 1 {
		size.Y = min(480, limit.Y*2/3)
	}
	constrain := func(n, minimum, maximum, base, step, limit int) int {
		n = max(n, minimum)
		if maximum > 0 {
			n = min(n, maximum)
		}
		if step > 0 && n > base {
			n = base + (n-base)/step*step
		}
		return max(1, min(n, limit))
	}
	size.X = constrain(size.X, h.min.X, h.max.X, h.base.X, h.increment.X, limit.X)
	size.Y = constrain(size.Y, h.min.Y, h.max.Y, h.base.Y, h.increment.Y, limit.Y)
	x, y := area.Min.X+(area.Dx()-size.X-2*border)/2, area.Min.Y+(area.Dy()-size.Y-2*border)/2
	return image.Rect(x, y, x+size.X, y+size.Y)
}

type hit struct {
	rect   image.Rectangle
	action string
	window xproto.Window
}

func tabRange(count, active, width, height int) (start, end int, arrows bool) {
	capacity := max(1, width/max(120, height*4))
	if count <= capacity {
		return 0, count, false
	}
	capacity = max(1, (width-2*height)/max(120, height*4))
	start = max(0, active) / capacity * capacity
	return start, min(count, start+capacity), true
}
