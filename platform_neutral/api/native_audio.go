package api

import (
	"fmt"
	"path/filepath"
	"strings"
	"unicode/utf8"
)

// NativeAudioRequest controls terminal-owned MP3 playback, never a shell command
// or a global mixer. Stop applies only to the matching authenticated job ID.
type NativeAudioRequest struct {
	Action string `json:"action"`
	File   string `json:"file,omitempty"`
}

func (r NativeAudioRequest) Validate() error {
	switch r.Action {
	case "play":
		if !filepath.IsAbs(r.File) || len(r.File) > 8192 || !utf8.ValidString(r.File) || strings.ContainsRune(r.File, 0) {
			return fmt.Errorf("audio playback requires a bounded absolute file path")
		}
	case "stop":
		if r.File != "" {
			return fmt.Errorf("audio stop does not take a file")
		}
	default:
		return fmt.Errorf("unsupported audio action")
	}
	return nil
}
