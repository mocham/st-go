package api

import (
	"fmt"
	"path/filepath"
	"strings"
	"unicode/utf8"
)

// NativeVideoRequest is deliberately not a general shell/exec request. The
// backend selects mpv and its fixed DRM/decoder options; clients supply data.
type NativeVideoRequest struct {
	File        string `json:"file"`
	ALSAConfig  string `json:"alsa_config"`
	AudioDevice string `json:"audio_device"`
	Volume      int    `json:"volume"` // mpv's own volume; never a global mixer change.
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
	for _, path := range []string{r.File, r.ALSAConfig} {
		if !filepath.IsAbs(path) || len(path) > 8192 || !utf8.ValidString(path) || strings.ContainsRune(path, 0) {
			return fmt.Errorf("native video requires bounded absolute file/config paths")
		}
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
