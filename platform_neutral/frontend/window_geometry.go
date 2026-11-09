package frontend

import (
	"fmt"
	"log"
	"math"

	"st-go/platform_neutral/api"
	term "st-go/platform_neutral/stterminal"
)

type windowRect struct {
	x, y, w, h int
}

func (t *Terminal) WindowGeometry(req term.WindowGeometryRequest) {
	if t.host == nil {
		return
	}
	switch req.Action {
	case term.GeometryRemember:
		r, err := t.currentWindowRect()
		if err != nil {
			log.Printf("window geometry remember: %v", err)
			return
		}
		if len(t.geometryTags) >= 64 {
			if _, exists := t.geometryTags[req.Tag]; !exists {
				log.Printf("window geometry: tag limit reached")
				return
			}
		}
		t.geometryTags[req.Tag] = r
	case term.GeometryRestore:
		t.restoreGeometryTag = ""
		if r, ok := t.geometryTags[req.Tag]; ok {
			t.requestWindowRect(r)
		}
	case term.GeometryForget:
		delete(t.geometryTags, req.Tag)
		if t.restoreGeometryTag == req.Tag {
			t.restoreGeometryTag = ""
		}
	case term.GeometryPlace:
		screenW, screenH := t.host.ScreenSize()
		r, err := resolveWindowRect(req, screenW, screenH, t.cw+2*t.borderpx, t.ch+2*t.borderpx)
		if err != nil {
			log.Printf("window geometry place: %v", err)
			return
		}
		t.requestWindowRect(r)
		if req.RestoreTag != "" {
			if _, ok := t.geometryTags[req.RestoreTag]; ok {
				t.restoreGeometryTag = req.RestoreTag
			}
		}
	}
}

func (t *Terminal) currentWindowRect() (windowRect, error) {
	r, err := t.host.WindowRect()
	return windowRect{r.X, r.Y, r.Width, r.Height}, err
}

func (t *Terminal) requestWindowRect(r windowRect) {
	t.host.RequestWindowRect(api.Rect{X: r.x, Y: r.y, Width: r.w, Height: r.h})
}

func resolveWindowRect(req term.WindowGeometryRequest, screenW, screenH, minW, minH int) (windowRect, error) {
	if screenW < 1 || screenH < 1 {
		return windowRect{}, fmt.Errorf("invalid screen size")
	}
	value := func(v term.GeometryValue, extent int) int {
		if v.Unit == term.GeometryRatio {
			return int(math.Round(v.Value * float64(extent)))
		}
		return int(math.Round(v.Value))
	}
	w, h := value(req.W, screenW), value(req.H, screenH)
	if w < minW {
		w = minW
	}
	if h < minH {
		h = minH
	}
	if w > screenW {
		w = screenW
	}
	if h > screenH {
		h = screenH
	}
	ox, oy := value(req.X, screenW), value(req.Y, screenH)
	r := windowRect{w: w, h: h}
	switch req.Anchor {
	case "absolute":
		r.x, r.y = ox, oy
	case "top-left":
		r.x, r.y = ox, oy
	case "top":
		r.x, r.y = (screenW-w)/2+ox, oy
	case "top-right":
		r.x, r.y = screenW-w-ox, oy
	case "right":
		r.x, r.y = screenW-w-ox, (screenH-h)/2+oy
	case "bottom-right":
		r.x, r.y = screenW-w-ox, screenH-h-oy
	case "bottom":
		r.x, r.y = (screenW-w)/2+ox, screenH-h-oy
	case "bottom-left":
		r.x, r.y = ox, screenH-h-oy
	case "left":
		r.x, r.y = ox, (screenH-h)/2+oy
	default:
		return windowRect{}, fmt.Errorf("invalid anchor %q", req.Anchor)
	}
	return r, nil
}

func (t *Terminal) restoreGeometryOnPress(button byte) bool {
	if button != 1 || t.restoreGeometryTag == "" {
		return false
	}
	tag := t.restoreGeometryTag
	t.restoreGeometryTag = ""
	r, ok := t.geometryTags[tag]
	if !ok {
		return false
	}
	t.requestWindowRect(r)
	t.suppressRestoreButton = true
	return true
}
