package api

import (
	"fmt"
	"path/filepath"
	"strings"
	"unicode/utf8"
)

// NativeVideoRequest is deliberately not a general shell/exec request. The
// backend selects a known player and its fixed options; clients supply data.
type NativeVideoRequest struct {
	Player      string `json:"player,omitempty"` // empty/mpv, or the sibling gov2 executable
	File        string `json:"file"`
	ALSAConfig  string `json:"alsa_config"`
	AudioDevice string `json:"audio_device"`
	Volume      int    `json:"volume"` // Per-player attenuation; never a global mixer change.
	Muted       bool   `json:"muted,omitempty"`
	DRMDevice   string `json:"drm_device,omitempty"`
	Connector   uint32 `json:"connector,omitempty"`
}

// NativeVideoHost queues a console handoff. Completion is called without the
// terminal or painter lock held, after native resources have been reclaimed.
type NativeVideoHost interface {
	StartNativeVideo(NativeVideoRequest, func(error)) error
}

func (r NativeVideoRequest) Validate() error {
	if r.Volume < 0 || r.Volume > 100 {
		return fmt.Errorf("native video volume must be 0..100")
	}
	paths := []string{r.File}
	switch r.Player {
	case "", "mpv":
		paths = append(paths, r.ALSAConfig)
		if r.DRMDevice != "" || r.Connector != 0 || r.Muted {
			return fmt.Errorf("gov2 options require the gov2 player")
		}
	case "gov2":
		if r.ALSAConfig != "" || r.AudioDevice != "" {
			return fmt.Errorf("gov2 uses direct ALSA output")
		}
		if r.DRMDevice != "" {
			const prefix = "/dev/dri/card"
			if !strings.HasPrefix(r.DRMDevice, prefix) || len(r.DRMDevice) <= len(prefix) || len(r.DRMDevice) > len(prefix)+3 {
				return fmt.Errorf("DRM device must be /dev/dri/cardN")
			}
			for _, ch := range r.DRMDevice[len(prefix):] {
				if ch < '0' || ch > '9' {
					return fmt.Errorf("DRM device must be /dev/dri/cardN")
				}
			}
		}
	default:
		return fmt.Errorf("unknown native video player")
	}
	for _, path := range paths {
		if !filepath.IsAbs(path) || len(path) > 8192 || !utf8.ValidString(path) || strings.ContainsRune(path, 0) {
			return fmt.Errorf("native video requires bounded absolute file/config paths")
		}
	}
	if r.Player == "gov2" {
		return nil
	}
	if !strings.HasPrefix(r.AudioDevice, "alsa/") || len(r.AudioDevice) < 6 || len(r.AudioDevice) > 256 || !utf8.ValidString(r.AudioDevice) {
		return fmt.Errorf("native video requires an ALSA audio device")
	}
	for _, ch := range r.AudioDevice {
		if ch < 32 || ch == 127 {
			return fmt.Errorf("invalid ALSA device")
		}
	}
	return nil
}
