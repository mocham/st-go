package stterminal

import (
	"strings"
	"testing"

	"st-go/config"
)

type busTestHooks struct {
	mockHooks
	requests [][]string
}

func (h *busTestHooks) AppBus(args []string) {
	h.requests = append(h.requests, append([]string(nil), args...))
}
func TestAppBusAuthenticationAndFragmentedFraming(t *testing.T) {
	for _, token := range []string{"secret", "wrong", ""} {
		cfg := config.Default()
		cfg.AppBusToken = "secret"
		hooks := &busTestHooks{}
		core := NewTerm(cfg, hooks)
		frame := []byte("\x1bPapp-bus auth " + token + " " + strings.Repeat("a", 32) + " subscribe project\x1b\\")
		for _, value := range frame {
			core.Twrite([]byte{value}, false)
		}
		if (len(hooks.requests) == 1) != (token == "secret") {
			t.Fatalf("authentication accepted %q: %v", token, hooks.requests)
		}
	}
}
