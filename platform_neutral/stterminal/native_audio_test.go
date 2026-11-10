package stterminal

import (
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"

	"st-go/config"
	"st-go/platform_neutral/api"
)

type audioTestHooks struct {
	mockHooks
	requests []api.NativeAudioRequest
}

func (h *audioTestHooks) NativeAudio(_ string, r api.NativeAudioRequest) {
	h.requests = append(h.requests, r)
}

func TestNativeAudioAuthenticationAndRequestValidation(t *testing.T) {
	id := "00112233445566778899aabbccddeeff"
	r := api.NativeAudioRequest{Action: "play", File: "/safe/song ';\n世界.mp3"}
	data, _ := json.Marshal(r)
	valid := base64.RawStdEncoding.EncodeToString(data)
	for _, tc := range []struct {
		command  string
		accepted bool
	}{
		{"native-audio auth secret " + id + " " + valid, true},
		{"native-audio auth wrong " + id + " " + valid, false},
		{"native-audio auth secret invalid " + valid, false},
		{"native-audio auth secret " + id + " " + base64.RawStdEncoding.EncodeToString([]byte(`{"action":"exec","file":"/bin/sh"}`)), false},
		{"native-audio auth secret " + id + " " + base64.RawStdEncoding.EncodeToString([]byte(`{"action":"play","file":"relative"}`)), false},
		{"native-audio auth secret " + id + " " + base64.RawStdEncoding.EncodeToString([]byte(`{"action":"stop"}`)), true},
		{"native-audio auth secret " + id + " " + strings.Repeat("A", 16385), false},
	} {
		cfg := config.Default()
		cfg.NativeAudioToken = "secret"
		h := &audioTestHooks{}
		core := NewTerm(cfg, h)
		core.Twrite([]byte("\x1bP"+tc.command+"\x1b\\"), false)
		if (len(h.requests) == 1) != tc.accepted {
			t.Fatalf("incorrect authorization for %q", tc.command[:min(len(tc.command), 100)])
		}
		if len(h.requests) == 1 && h.requests[0].Action == "play" && h.requests[0] != r {
			t.Fatal("filename did not round-trip exactly")
		}
	}
}
