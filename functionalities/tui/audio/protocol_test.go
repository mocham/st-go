package audio

import (
	"encoding/base64"
	"strings"
	"testing"

	"st-go/platform_neutral/api"
)

func TestFramesAndReplies(t *testing.T) {
	id := "00112233445566778899aabbccddeeff"
	frame, err := RequestFrame(id, id, api.NativeAudioRequest{Action: "play", File: "/song ';\n世界.mp3"})
	if err != nil || !strings.HasPrefix(frame, "\x1bPnative-audio auth "+id+" "+id+" ") || !strings.HasSuffix(frame, "\x1b\\") {
		t.Fatal(frame, err)
	}
	if _, err := RequestFrame("forged", id, api.NativeAudioRequest{Action: "stop"}); err == nil {
		t.Fatal("accepted malformed capability")
	}
	if _, err := RequestFrame(id, id, api.NativeAudioRequest{Action: "play", File: "/" + strings.Repeat("\n", 8000)}); err == nil {
		t.Fatal("encoded request exceeded parser's payload limit")
	}
	for _, status := range []string{"accepted", "done", "error " + base64.RawStdEncoding.EncodeToString([]byte("no audio"))} {
		reply, ok := ParseReply("native-audio " + id + " " + status)
		if !ok || reply.ID != id {
			t.Fatal(reply, ok)
		}
	}
	for _, text := range []string{"native-audio invalid done", "native-audio " + id + " accepted extra", "native-audio " + id + " error %", "native-audio " + id + " unknown"} {
		if _, ok := ParseReply(text); ok {
			t.Fatal("accepted malformed reply", text)
		}
	}
}
