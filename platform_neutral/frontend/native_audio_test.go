package frontend

import (
	"fmt"
	"strings"
	"testing"

	"st-go/platform_neutral/api"
)

type audioTestPlayer struct {
	id, file, stopped string
	done              func(error)
	closed            bool
}

func (p *audioTestPlayer) Start(id, file string, done func(error)) error {
	p.id, p.file, p.done = id, file, done
	return nil
}
func (p *audioTestPlayer) Stop(id string) { p.stopped = id }
func (p *audioTestPlayer) Close()         { p.closed = true }

func TestNativeAudioDispatchStopAndSessionCleanup(t *testing.T) {
	trm := regionTestTerminal(t)
	player := &audioTestPlayer{}
	trm.audio = player
	var output strings.Builder
	trm.termCore.SetWriter(func(data []byte) { output.Write(data) })
	id := "00112233445566778899aabbccddeeff"
	trm.mu.Lock()
	trm.NativeAudio(id, api.NativeAudioRequest{Action: "play", File: "/safe/song.mp3"})
	trm.mu.Unlock()
	if player.file != "/safe/song.mp3" || !strings.Contains(output.String(), id+" accepted") {
		t.Fatal(output.String())
	}
	trm.NativeAudio(id, api.NativeAudioRequest{Action: "stop"})
	if player.stopped != id {
		t.Fatal("stop lost its job ID")
	}
	player.done(fmt.Errorf("synthetic output failure"))
	if !strings.Contains(output.String(), id+" error ") {
		t.Fatal("audio failure was not reported")
	}
	trm.Close()
	if !player.closed {
		t.Fatal("terminal close left its audio player running")
	}
}
