package frontend

import (
	"encoding/base64"
	"fmt"

	"st-go/platform_neutral/api"
)

type nativeAudioPlayer interface {
	Start(id, file string, completed func(error)) error
	Stop(id string)
	Close()
}

// Called by the parser with t.mu held. Audio is owned by this terminal session,
// not by its display backend or a TUI client. Completion serializes PTY replies.
func (t *Terminal) NativeAudio(id string, request api.NativeAudioRequest) {
	if t.closed() {
		return
	}
	if t.audio == nil {
		return
	}
	if request.Action == "stop" {
		t.audio.Stop(id)
		return
	}
	reply := func(status string, err error) {
		message := ""
		if err != nil {
			message = " " + base64.RawStdEncoding.EncodeToString([]byte(err.Error()[:min(len(err.Error()), 2048)]))
		}
		t.termCore.WriteToTTY([]byte(fmt.Sprintf("\x1bPnative-audio %s %s%s\x1b\\", id, status, message)), false)
	}
	err := t.audio.Start(id, request.File, func(err error) {
		t.mu.Lock()
		defer t.mu.Unlock()
		if !t.closed() {
			if err != nil {
				reply("error", err)
			} else {
				reply("done", nil)
			}
		}
	})
	if err != nil {
		reply("error", err)
	} else {
		reply("accepted", nil)
	}
}
