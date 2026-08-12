package main

import (
	"fmt"
	"log"
	"math"

	"github.com/BurntSushi/xgb/xproto"
	"github.com/BurntSushi/xgbutil/ewmh"

	"st-go/term"
)

type windowRect struct {
	x, y, w, h int
}

func (t *Terminal) WindowGeometry(req term.WindowGeometryRequest) {
	if t.conn == nil || t.scr == nil || t.xu == nil {
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
		r, err := resolveWindowRect(req, int(t.scr.WidthInPixels), int(t.scr.HeightInPixels), t.cw+2*t.borderpx, t.ch+2*t.borderpx)
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
	g, err := xproto.GetGeometry(t.conn, xproto.Drawable(t.win)).Reply()
	if err != nil {
		return windowRect{}, err
	}
	p, err := xproto.TranslateCoordinates(t.conn, t.win, t.scr.Root, 0, 0).Reply()
	if err != nil {
		return windowRect{}, err
	}
	return windowRect{x: int(p.DstX), y: int(p.DstY), w: int(g.Width), h: int(g.Height)}, nil
}

func (t *Terminal) requestWindowRect(r windowRect) {
	if _, err := ewmh.GetEwmhWM(t.xu); err == nil {
		if supported, err := ewmh.SupportedGet(t.xu); err == nil {
			for _, atom := range supported {
				if atom == "_NET_MOVERESIZE_WINDOW" {
					if err := ewmh.MoveresizeWindowExtra(t.xu, t.win, r.x, r.y, r.w, r.h,
						int(xproto.GravityStatic), 1, true, true); err == nil {
						return
					}
					break
				}
			}
		}
	}
	mask := uint16(xproto.ConfigWindowX | xproto.ConfigWindowY |
		xproto.ConfigWindowWidth | xproto.ConfigWindowHeight)
	xproto.ConfigureWindow(t.conn, t.win, mask, []uint32{
		uint32(int32(r.x)), uint32(int32(r.y)), uint32(r.w), uint32(r.h),
	})
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

func (t *Terminal) restoreGeometryOnPress(e xproto.ButtonPressEvent) bool {
	if e.Detail != 1 || t.restoreGeometryTag == "" {
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
