package x11

import (
	"github.com/BurntSushi/xgb/xproto"
	"github.com/BurntSushi/xgbutil/ewmh"
	"st-go/platform_neutral/api"
)

func (t *Driver) WindowRect() (api.Rect, error) {
	g, err := xproto.GetGeometry(t.conn, xproto.Drawable(t.win)).Reply()
	if err != nil {
		return api.Rect{}, err
	}
	p, err := xproto.TranslateCoordinates(t.conn, t.win, t.scr.Root, 0, 0).Reply()
	if err != nil {
		return api.Rect{}, err
	}
	return api.Rect{X: int(p.DstX), Y: int(p.DstY), Width: int(g.Width), Height: int(g.Height)}, nil
}

func (t *Driver) RequestWindowRect(r api.Rect) {
	if _, err := ewmh.GetEwmhWM(t.xu); err == nil {
		if supported, err := ewmh.SupportedGet(t.xu); err == nil {
			for _, atom := range supported {
				if atom == "_NET_MOVERESIZE_WINDOW" {
					if err := ewmh.MoveresizeWindowExtra(t.xu, t.win, r.X, r.Y, r.Width, r.Height, int(xproto.GravityStatic), 1, true, true); err == nil {
						return
					}
					break
				}
			}
		}
	}
	mask := uint16(xproto.ConfigWindowX | xproto.ConfigWindowY | xproto.ConfigWindowWidth | xproto.ConfigWindowHeight)
	xproto.ConfigureWindow(t.conn, t.win, mask, []uint32{uint32(int32(r.X)), uint32(int32(r.Y)), uint32(r.Width), uint32(r.Height)})
}
