package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

func videoFile(path string) bool {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".mp4", ".mkv", ".webm", ".avi", ".mov", ".mpeg", ".mpg", ".m4v", ".ogv", ".3gp", ".mts", ".m2ts", ".vob":
		return true
	}
	return fileCommandAvailable && strings.HasPrefix(fileMime(path), "video/")
}

// The helper requests a real-console handoff over DCS. Never launch DRM mpv
// directly on the PTY: that disables its VT switcher and races console ownership.
func (b *Browser) openDefaultVideo(path string) bool {
	if !videoFile(path) {
		return false
	}
	backend := browserBackend()
	b.status = "No video player for " + backend
	if backend == "fb" {
		_, err := exec.LookPath("mpv")
		if err != nil {
			b.status = "mpv not found"
		} else if os.Getenv("ST_GO_NATIVE_VIDEO") != "1" {
			b.status = "Restart st-fb for native video"
		} else if tool, err := os.Executable(); err == nil {
			b.runTerminalTool(tool, path, "--native-video")
			return true
		}
	}
	b.drawStatus()
	b.flush()
	return true
}
