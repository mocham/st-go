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

// gov uses the terminal's bundled decoder and retained surface. Give it the
// foreground PTY using the same handoff/restoration as other terminal viewers.
func (b *Browser) openDefaultVideo(path string) bool {
	if !videoFile(path) {
		return false
	}
	backend := browserBackend()
	b.status = "No video player for " + backend
	if backend == "fb" {
		tool, err := exec.LookPath("gov")
		if err != nil {
			b.status = "gov not found"
		} else if os.Getenv("ST_GO_NATIVE_VIDEO_TOKEN") == "" {
			b.status = "Restart st-fb for video playback"
		} else {
			tool, err = filepath.Abs(tool)
			volume, volumeErr := playbackVolume(os.Getenv("ST_FILE_BROWSER_VOLUME"))
			if err != nil {
				b.status = "gov: " + displayText(err.Error())
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
