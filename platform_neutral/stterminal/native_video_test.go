package stterminal

import (
	"encoding/base64"
	"encoding/json"
	"testing"

	"st-go/config"
	"st-go/platform_neutral/api"
)

type nativeVideoTestHooks struct {
	mockHooks
	requests []api.NativeVideoRequest
}

func (h *nativeVideoTestHooks) NativeVideo(_ string, request api.NativeVideoRequest) {
	h.requests = append(h.requests, request)
}

func TestNativeVideoRequiresCapabilityAndBoundedData(t *testing.T) {
	request := api.NativeVideoRequest{File: "/safe/movie ';\n世界.mp4", ALSAConfig: "/safe/alsa.conf", AudioDevice: "alsa/plughw:CARD=1,DEV=0"}
	data, _ := json.Marshal(request)
	payload := base64.RawStdEncoding.EncodeToString(data)
	id := "00112233445566778899aabbccddeeff"
	for _, tc := range []struct {
		name, command string
		accepted      bool
	}{
		{"valid", "native-video auth secret " + id + " " + payload, true},
		{"wrong token", "native-video auth wrong " + id + " " + payload, false},
		{"missing auth", "native-video secret " + id + " " + payload, false},
		{"bad id", "native-video auth secret ../bad " + payload, false},
		{"bad encoding", "native-video auth secret " + id + " not-base64", false},
		{"unknown fields", "native-video auth secret " + id + " " + base64.RawStdEncoding.EncodeToString([]byte(`{"exec":"sh"}`)), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := config.Default()
			cfg.NativeVideoToken = "secret"
			cfg.AllowGeometryOps = false
			h := &nativeVideoTestHooks{}
			core := NewTerm(cfg, h)
			core.Twrite([]byte("\x1bP"+tc.command+"\x1b\\"), false)
			if (len(h.requests) == 1) != tc.accepted {
				t.Fatal("incorrect native video authorization", h.requests)
			}
			if tc.accepted && h.requests[0] != request {
				t.Fatal("filename did not round-trip exactly")
			}
		})
	}
}
