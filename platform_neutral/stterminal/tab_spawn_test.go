package stterminal

import (
	"encoding/base64"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"st-go/config"
	"st-go/platform_neutral/api"
)

type tabTestHooks struct {
	mockHooks
	requests []api.TabSpawnRequest
}

func (h *tabTestHooks) SpawnTab(_ string, r api.TabSpawnRequest) { h.requests = append(h.requests, r) }

func TestTabSpawnAuthenticationAndFraming(t *testing.T) {
	id := "00112233445566778899aabbccddeeff"
	request := api.TabSpawnRequest{Command: []string{"/bin/tool", "/safe/a ';\n世界.txt"}, Directory: "/safe", Title: "document", Detached: true}
	data, _ := json.Marshal(request)
	payload := base64.RawStdEncoding.EncodeToString(data)
	for _, tc := range []struct {
		name, token, command string
		accepted             bool
	}{
		{"valid", "secret", "tab-spawn auth secret " + id + " " + payload, true},
		{"disabled", "", "tab-spawn auth secret " + id + " " + payload, false},
		{"wrong token", "secret", "tab-spawn auth wrong " + id + " " + payload, false},
		{"bad id", "secret", "tab-spawn auth secret not-an-id " + payload, false},
		{"unknown field", "secret", "tab-spawn auth secret " + id + " " + base64.RawStdEncoding.EncodeToString([]byte(`{"command":["tool"],"directory":"/","exec":true}`)), false},
		{"relative cwd", "secret", "tab-spawn auth secret " + id + " " + base64.RawStdEncoding.EncodeToString([]byte(`{"command":["tool"],"directory":"relative"}`)), false},
		{"extra JSON", "secret", "tab-spawn auth secret " + id + " " + base64.RawStdEncoding.EncodeToString(append(data, []byte(` {}`)...)), false},
		{"oversize", "secret", "tab-spawn auth secret " + id + " " + strings.Repeat("A", api.TabRequestLimit+1), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := config.Default()
			cfg.TabSpawnToken = tc.token
			hooks := &tabTestHooks{}
			core := NewTerm(cfg, hooks)
			frame := []byte("\x1bP" + tc.command + "\x1b\\")
			// Include split escape/base64/UTF-8 boundaries rather than one Twrite.
			for len(frame) > 0 {
				n := min(7, len(frame))
				core.Twrite(frame[:n], false)
				frame = frame[n:]
			}
			if (len(hooks.requests) == 1) != tc.accepted {
				t.Fatalf("authorization = %d requests", len(hooks.requests))
			}
			if tc.accepted && !reflect.DeepEqual(request, hooks.requests[0]) {
				t.Fatal("literal argv changed", hooks.requests[0])
			}
		})
	}
}
