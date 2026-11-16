package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
)

func videoExtension(path string) bool {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".mp4", ".mkv", ".webm", ".avi", ".mov", ".mpeg", ".mpg", ".m4v", ".ogv", ".3gp", ".3g2", ".mts", ".m2ts", ".vob",
		".asf", ".divx", ".dv", ".f4v", ".flv", ".m2v", ".mpe", ".mpv", ".mxf", ".ogm", ".rm", ".rmvb", ".wmv", ".y4m":
		return true
	}
	return false
}

func videoFile(path string) bool {
	if videoExtension(path) {
		return true
	}
	return fileCommandAvailable && strings.HasPrefix(fileMime(path), "video/")
}

// gov2 requests the real-TTY handoff from st-fb. The browser gives its client
// the foreground PTY and restores the browser after the native player exits.
func (b *Browser) openDefaultVideo(path string) bool {
	if !videoFile(path) {
		return false
	}
	backend := browserBackend()
	b.status = "No video player for " + backend
	if backend == "fb" {
		tool, err := exec.LookPath("gov2")
		if err != nil {
			b.status = "gov2 not found"
		} else if os.Getenv("ST_GO_NATIVE_VIDEO_TOKEN") == "" {
			b.status = "Restart st-fb for video playback"
		} else {
			tool, err = filepath.Abs(tool)
			volume, volumeErr := playbackVolume(os.Getenv("ST_FILE_BROWSER_VOLUME"))
			if err != nil {
				b.status = "gov2: " + displayText(err.Error())
			} else if volumeErr != nil {
				b.status = displayText(volumeErr.Error())
			} else {
				b.runTerminalTool(tool, path, "-volume", strconv.Itoa(volume))
				return true
			}
		}
	}
	b.drawStatus()
	b.flush()
	return true
}
