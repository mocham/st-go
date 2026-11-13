package frontend

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"image"
	"sync"

	"st-go/platform_neutral/api"
	"st-go/platform_neutral/video"
)

// Terminal.mu owns the session; mu serializes its surface with decoder uploads.
// Workers never acquire Terminal.mu and closure never waits on native decoding.
type videoPresentation struct {
	mu      sync.Mutex
	id      string
	cells   api.Rect
	bounds  image.Rectangle
	surface api.Surface
	player  *video.Player
}

func (t *Terminal) Video(id string, r api.VideoRequest) {
	if t.closed() || t.termCore == nil {
		return
	}
	status := api.VideoStatus{Supported: video.Supported(), State: "idle"}
	reply := func() {
		data, _ := json.Marshal(status)
		t.termCore.WriteToTTY([]byte(fmt.Sprintf("\x1bPvideo %s %s\x1b\\", id, base64.RawStdEncoding.EncodeToString(data))), false)
	}
	if r.Action == "query" {
		reply()
		return
	}
	if !video.Supported() {
		status.State = "error"
		status.Error = "MP4 playback requires the full st-fb build"
		reply()
		return
	}
	if r.Action == "open" {
		if t.video != nil {
			status.State = "error"
			status.Error = "video is already open"
			reply()
			return
		}
		v := &videoPresentation{id: id, cells: r.Bounds}
		t.video = v
		if err := t.configureVideo(); err != nil {
			t.closeVideo()
			status.State = "error"
			status.Error = err.Error()
			reply()
			return
		}
		v.player = video.Start(r.File, v.bounds.Dx(), v.bounds.Dy(), r.Volume, r.Muted, v.present)
	} else if t.video == nil || t.video.id != id {
		status.State = "error"
		status.Error = "video session is no longer active"
		reply()
		return
	} else {
		switch r.Action {
		case "close":
			t.closeVideo()
			status.State = "closed"
			reply()
			return
		case "resize":
			t.video.cells = r.Bounds
			if err := t.configureVideo(); err != nil {
				status.State = "error"
				status.Error = err.Error()
				reply()
				return
			}
		case "status":
		default:
			if err := t.video.player.Control(r); err != nil {
				status.State = "error"
				status.Error = err.Error()
				reply()
				return
			}
		}
	}
	status = t.video.player.Status()
	reply()
}
func (v *videoPresentation) present(pixels []uint32, w, h int) {
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.surface != nil && w == v.bounds.Dx() && h == v.bounds.Dy() {
		v.surface.Present(pixels, w, h, 0, false)
	}
}
func (t *Terminal) configureVideo() error {
	v := t.video
	if v == nil {
		return nil
	}
	v.mu.Lock()
	defer v.mu.Unlock()
	b := v.cells
	r := image.Rect(t.borderpx+(b.X-1)*t.cw, t.borderpx+(b.Y-1)*t.ch, t.borderpx+(b.X-1+b.Width)*t.cw, t.borderpx+(b.Y-1+b.Height)*t.ch)
	r = r.Intersect(image.Rect(0, 0, t.fbW, t.fbH))
	v.bounds = r
	if r.Empty() {
		if v.surface != nil {
			v.surface.Close()
			v.surface = nil
		}
	} else if v.surface == nil {
		var err error
		v.surface, err = t.host.NewSurface(r, 0xff000000)
		if err != nil {
			return err
		}
	} else {
		v.surface.Configure(r, 0xff000000)
	}
	if v.player != nil {
		v.player.Resize(r.Dx(), r.Dy())
	}
	return nil
}
func (t *Terminal) closeVideo() {
	v := t.video
	if v == nil {
		return
	}
	t.video = nil
	if v.player != nil {
		v.player.Close()
	}
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.surface != nil {
		v.surface.Close()
		v.surface = nil
	}
}
func (t *Terminal) AlternateScreen(active bool) {
	if !active {
		t.closeVideo()
	}
}
