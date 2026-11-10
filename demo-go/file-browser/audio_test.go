package main

import (
	"encoding/base64"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"st-go/platform_neutral/api"
)

func TestMP3UsesTerminalAudioAndStopsMatchingJob(t *testing.T) {
	for _, backend := range []string{"x11", "fb", "web"} {
		t.Run(backend, func(t *testing.T) {
			dir := t.TempDir()
			writeFiles(t, dir, "song.mp3")
			token := "00112233445566778899aabbccddeeff"
			f := startBrowser(t, testEnv("ST_GO_BACKEND="+backend, "ST_GO_NATIVE_AUDIO=1", "ST_GO_NATIVE_AUDIO_TOKEN="+token), dir)
			f.send("\x1b[B\r")
			var id string
			waitFor(t, f, func() bool {
				out := f.output()
				start := strings.Index(out, "\x1bPnative-audio auth ")
				if start < 0 {
					return false
				}
				end := strings.Index(out[start:], "\x1b\\")
				if end < 0 {
					return false
				}
				fields := strings.Fields(out[start+2 : start+end])
				if len(fields) != 5 || fields[2] != token {
					t.Fatal("incorrect authenticated audio frame", fields)
				}
				id = fields[3]
				data, err := base64.RawStdEncoding.DecodeString(fields[4])
				var request api.NativeAudioRequest
				if err != nil || json.Unmarshal(data, &request) != nil || request.Action != "play" || request.File != filepath.Join(dir, "song.mp3") {
					t.Fatal("incorrect audio request", request, err)
				}
				return true
			}, "terminal-owned audio request")
			f.send("\x1bPnative-audio " + id + " accepted\x1b\\")
			waitContains(t, f, "Playing  q stop", "terminal accepted audio playback")
			before := len(f.output())
			f.send("q")
			data, _ := json.Marshal(api.NativeAudioRequest{Action: "stop"})
			waitContains(t, f, "native-audio auth "+token+" "+id+" "+base64.RawStdEncoding.EncodeToString(data), "matching stop request")
			waitContains(t, f, "Closed mp3", "audio viewer returned to browser")
			// Completion may arrive after q. It must be drained, not interpreted
			// as normal-mode keys (notably the 'o' in native-audio).
			f.send("\x1bPnative-audio " + id + " done\x1b\\r")
			waitFor(t, f, func() bool { return strings.Contains(f.output()[before:], "Refreshed") }, "late completion did not reopen a file")
		})
	}
}
