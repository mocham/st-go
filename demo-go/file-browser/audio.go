package main

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"st-go/platform_neutral/api"
	audioprotocol "st-go/tui/audio"
)

func (b *Browser) startPlayback(path string) error {
	if os.Getenv("ST_GO_NATIVE_AUDIO") != "1" {
		return fmt.Errorf("terminal audio unavailable; restart the rebuilt st-go")
	}
	file, err := filepath.Abs(path)
	if err != nil {
		return err
	}
	var nonce [16]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return err
	}
	id := hex.EncodeToString(nonce[:])
	frame, err := audioprotocol.RequestFrame(os.Getenv("ST_GO_NATIVE_AUDIO_TOKEN"), id, api.NativeAudioRequest{Action: "play", File: file})
	if err != nil {
		return err
	}
	b.playback, b.playbackStarting = id, true
	b.playbackDeadline = time.Now().Add(5 * time.Second)
	fmt.Fprint(b.out, frame)
	b.flush()
	return nil
}

func (b *Browser) stopPlayback() {
	if b.playback != "" {
		frame, err := audioprotocol.RequestFrame(os.Getenv("ST_GO_NATIVE_AUDIO_TOKEN"), b.playback, api.NativeAudioRequest{Action: "stop"})
		if err == nil {
			fmt.Fprint(b.out, frame)
			b.flush()
		}
	}
	b.playback, b.playbackStarting = "", false
}

// ESC P has already been consumed. Drain every framed reply, including late
// completions after leaving the audio viewer, so its payload is never input.
func (b *Browser) readAudioReply() {
	var control []byte
	deadline := time.Now().Add(time.Second)
	for len(control) < 4096 && time.Now().Before(deadline) {
		ch, status := b.in.readByte(50 * time.Millisecond)
		if status != inData {
			return
		}
		if ch == '\\' && len(control) > 0 && control[len(control)-1] == 0x1b {
			reply, ok := audioprotocol.ParseReply(string(control[:len(control)-1]))
			if !ok || reply.ID != b.playback {
				return
			}
			b.playbackStarting = false
			switch reply.Status {
			case "done":
				b.playback = ""
			case "error":
				b.playback = ""
				b.playbackErr = fmt.Errorf("%s", reply.Message)
			}
			return
		}
		control = append(control, ch)
	}
}
