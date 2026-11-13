package frontend

import (
	"encoding/base64"
	"fmt"

	"st-go/platform_neutral/api"
)

type terminalTabManager interface {
	spawn(*Terminal, string, api.TabSpawnRequest) bool
	titleChanged(api.WidgetID, string)
	terminalClosed(api.WidgetID)
	imeChanged(api.WidgetID)
	run(*Terminal)
}

// Called by the parser with the requesting terminal's lock held. Layout and
// process startup are queued outside that lock; replies stay with this PTY.
func (t *Terminal) SpawnTab(id string, request api.TabSpawnRequest) {
	if t.tabs == nil || !t.tabs.spawn(t, id, request) {
		t.tabReply(id, 0, fmt.Errorf("terminal tabs unavailable or request queue full"))
	}
}

func (t *Terminal) tabReply(id string, tab api.WidgetID, err error) {
	if t.closed() || t.termCore == nil {
		return
	}
	if err != nil {
		message := err.Error()
		message = message[:min(len(message), 2048)]
		t.termCore.WriteToTTY([]byte("\x1bPtab-spawn "+id+" error "+base64.RawStdEncoding.EncodeToString([]byte(message))+"\x1b\\"), false)
	} else if tab == 0 {
		t.termCore.WriteToTTY([]byte("\x1bPtab-spawn "+id+" supported 1\x1b\\"), false)
	} else {
		t.termCore.WriteToTTY([]byte(fmt.Sprintf("\x1bPtab-spawn %s accepted %d\x1b\\", id, tab)), false)
	}
}
