package web

import (
	"st-go/platform_neutral/api"
	"st-go/platform_neutral/sharedclipboard"
)

func (d *Driver) ClipboardSyncState() api.ClipboardSyncState {
	if d.clipboardSync == nil {
		return api.ClipboardSyncState{}
	}
	state := d.clipboardSync.State()
	return api.ClipboardSyncState{Enabled: state.Enabled, Ready: state.Ready, Available: state.Available}
}

func (d *Driver) SetClipboardSync(enabled bool) bool {
	if d.clipboardSync == nil {
		return false
	}
	d.mu.Lock()
	seed := make(map[byte]string, len(d.clipboard))
	for selection, text := range d.clipboard {
		seed[selection] = text
	}
	d.mu.Unlock()
	return d.clipboardSync.SetEnabled(enabled, seed)
}

func (d *Driver) sharedClipboardChanged(entry sharedclipboard.Entry) {
	d.mu.Lock()
	d.clipboard[entry.Selection] = entry.Text
	d.clipboardVersion++
	d.clipboardUpdate = serverMessage{Type: "clipboard", Selection: string(entry.Selection), Text: entry.Text}
	d.mu.Unlock()
	d.notify()
}

func (d *Driver) sharedClipboardStatus(state sharedclipboard.State) {
	d.mu.Lock()
	d.syncVersion++
	d.syncUpdate = serverMessage{Type: "clipboard_sync", SyncEnabled: state.Enabled, SyncReady: state.Ready, SyncAvailable: state.Available}
	d.mu.Unlock()
	d.notify()
}
