package stterminal

import (
	"bytes"
	"testing"

	"st-go/config"
	"st-go/platform_neutral/api"
)

type clipboardSyncHooks struct {
	mockHooks
	state api.ClipboardSyncState
	sets  []bool
}

func (h *clipboardSyncHooks) SetClipboardSync(enabled bool) bool {
	if !h.state.Available {
		return false
	}
	h.state.Enabled = enabled
	h.sets = append(h.sets, enabled)
	return true
}
func (h *clipboardSyncHooks) ClipboardSyncState() api.ClipboardSyncState { return h.state }

func TestClipboardSyncRequiresPrivateCapability(t *testing.T) {
	for _, tc := range []struct {
		command string
		sets    int
		want    string
	}{
		{"clipboard-sync auth wrong on", 0, ""},
		{"clipboard-sync auth secret invalid", 0, ""},
		{"clipboard-sync auth secret status", 0, "clipboard-sync off"},
		{"clipboard-sync auth secret on", 1, "clipboard-sync starting"},
		{"clipboard-sync auth secret off", 1, "clipboard-sync off"},
	} {
		cfg := config.Default()
		cfg.ClipboardSyncToken = "secret"
		hooks := &clipboardSyncHooks{state: api.ClipboardSyncState{Available: true}}
		core := NewTerm(cfg, hooks)
		var reply bytes.Buffer
		core.SetWriter(func(data []byte) { reply.Write(data) })
		core.Twrite([]byte("\x1bP"+tc.command+"\x1b\\"), false)
		if len(hooks.sets) != tc.sets {
			t.Fatalf("%q changed synchronization without authorization", tc.command)
		}
		if tc.want == "" {
			if reply.Len() != 0 {
				t.Fatalf("%q produced an unauthorized reply", tc.command)
			}
		} else if !bytes.Contains(reply.Bytes(), []byte(tc.want)) {
			t.Fatalf("%q reply = %q, want %q", tc.command, reply.String(), tc.want)
		}
	}
}
