package stterminal

import (
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"

	"st-go/config"
	"st-go/platform_neutral/api"
)

type videoTestHooks struct {
	mockHooks
	requests []api.VideoRequest
	screens  []bool
}

func (h *videoTestHooks) Video(_ string, r api.VideoRequest) { h.requests = append(h.requests, r) }
func (h *videoTestHooks) AlternateScreen(active bool)        { h.screens = append(h.screens, active) }
func TestCompositedVideoProtocolAndScreenLifetime(t *testing.T) {
	cfg := config.Default()
	cfg.NativeVideoToken = "secret"
	h := &videoTestHooks{}
	term := NewTerm(cfg, h)
	h.screens = nil
	request := api.VideoRequest{Action: "open", File: "/safe/';\n世界.mp4", Bounds: api.Rect{X: 1, Y: 2, Width: 80, Height: 22}, Volume: 90}
	data, _ := json.Marshal(request)
	command := "\x1bPvideo auth secret 00112233445566778899aabbccddeeff " + base64.RawStdEncoding.EncodeToString(data) + "\x1b\\"
	for _, c := range []byte(command) {
		term.Twrite([]byte{c}, false)
	}
	if len(h.requests) != 1 || h.requests[0] != request {
		t.Fatal("fragmented video request failed", h.requests)
	}
	for _, bad := range []string{strings.Replace(command, "secret", "wrong", 1), strings.Replace(command, "00112233445566778899aabbccddeeff", "../bad", 1)} {
		term.Twrite([]byte(bad), false)
	}
	if len(h.requests) != 1 {
		t.Fatal("unauthorized request was accepted")
	}
	term.Twrite([]byte("\x1b[?1049h\x1b[?1049l"), false)
	if len(h.screens) != 2 || !h.screens[0] || h.screens[1] {
		t.Fatal("missing media lifetime events", h.screens)
	}
}
