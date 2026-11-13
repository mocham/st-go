package api

import (
	"fmt"
	"path/filepath"
	"strings"
	"unicode/utf8"
)

// VideoRequest controls terminal-owned, composited playback. Bounds uses
// one-based terminal cells; media bytes and device handles never cross the PTY.
type VideoRequest struct {
	Action     string `json:"action"`
	File       string `json:"file,omitempty"`
	Bounds     Rect   `json:"bounds,omitempty"`
	PositionMS int64  `json:"position_ms,omitempty"`
	Volume     int    `json:"volume,omitempty"`
	Muted      bool   `json:"muted,omitempty"`
}

type VideoStatus struct {
	Supported  bool   `json:"supported"`
	State      string `json:"state"`
	PositionMS int64  `json:"position_ms"`
	DurationMS int64  `json:"duration_ms"`
	Width      int    `json:"width"`
	Height     int    `json:"height"`
	Audio      bool   `json:"audio"`
	Muted      bool   `json:"muted"`
	Volume     int    `json:"volume"`
	Error      string `json:"error,omitempty"`
	Warning    string `json:"warning,omitempty"`
}

func (r VideoRequest) Validate() error {
	switch r.Action {
	case "query", "status", "toggle", "close", "mute":
	case "seek":
		if r.PositionMS < 0 || r.PositionMS > 31*24*60*60*1000 {
			return fmt.Errorf("invalid video position")
		}
	case "open":
		if !filepath.IsAbs(r.File) || len(r.File) > 8192 || !utf8.ValidString(r.File) || strings.ContainsRune(r.File, 0) {
			return fmt.Errorf("video requires an absolute local filename")
		}
		fallthrough
	case "resize":
		b := r.Bounds
		if b.X < 1 || b.Y < 1 || b.Width < 1 || b.Height < 1 || b.X > 65535 || b.Y > 65535 || b.Width > 65535 || b.Height > 65535 {
			return fmt.Errorf("invalid video bounds")
		}
	case "volume":
	default:
		return fmt.Errorf("unknown video action")
	}
	if r.Volume < 0 || r.Volume > 100 {
		return fmt.Errorf("video volume must be 0..100")
	}
	return nil
}
