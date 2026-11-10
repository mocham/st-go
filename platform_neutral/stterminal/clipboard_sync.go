package stterminal

import (
	"crypto/subtle"

	"st-go/platform_neutral/api"
)

// ClipboardSyncHooks exposes only the host-level opt-in switch. Clipboard
// contents continue to use ClipboardHooks and their existing OSC 52 policy.
type ClipboardSyncHooks interface {
	SetClipboardSync(bool) bool
	ClipboardSyncState() api.ClipboardSyncState
}

func (t *Term) dslClipboardSync(args []string) {
	if len(args) != 3 || args[0] != "auth" || t.cfg.ClipboardSyncToken == "" ||
		subtle.ConstantTimeCompare([]byte(args[1]), []byte(t.cfg.ClipboardSyncToken)) != 1 {
		return
	}
	hooks, ok := t.hooks.(ClipboardSyncHooks)
	if !ok {
		return
	}
	state := hooks.ClipboardSyncState()
	switch args[2] {
	case "on":
		if !hooks.SetClipboardSync(true) {
			state = api.ClipboardSyncState{}
		} else {
			state.Enabled, state.Available = true, true
		}
	case "off":
		if hooks.SetClipboardSync(false) {
			state.Enabled = false
		}
	case "status":
	default:
		return
	}
	status := "off"
	if !state.Available {
		status = "unavailable"
	} else if state.Enabled && state.Ready {
		status = "on"
	} else if state.Enabled {
		status = "starting"
	}
	t.ttywrite([]byte("\x1bPclipboard-sync "+status+"\x1b\\"), false)
}
