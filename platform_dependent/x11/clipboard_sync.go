package x11

import (
	"github.com/BurntSushi/xgb/xfixes"
	"github.com/BurntSushi/xgb/xproto"
	"st-go/platform_neutral/api"
	"st-go/platform_neutral/sharedclipboard"
)

const clipboardSelectionMask = xfixes.SelectionEventMaskSetSelectionOwner |
	xfixes.SelectionEventMaskSelectionWindowDestroy |
	xfixes.SelectionEventMaskSelectionClientClose

func (t *Driver) ClipboardSyncState() api.ClipboardSyncState {
	if t.clipboardSync == nil {
		return api.ClipboardSyncState{}
	}
	state := t.clipboardSync.State()
	available := state.Available && t.xfixesAvailable && t.clipboardManager != 0
	return api.ClipboardSyncState{Enabled: state.Enabled, Ready: state.Ready, Available: available}
}

func (t *Driver) SetClipboardSync(enabled bool) bool {
	if t.clipboardSync == nil || t.conn == nil || t.closed() {
		return false
	}
	if enabled && !t.ClipboardSyncState().Available {
		return false
	}
	if !enabled {
		t.clipboardSync.SetWatching(false)
		t.clipboardBridge = false
		if t.clipboardManager != 0 {
			if owner, err := xproto.GetSelectionOwner(t.conn, t.clipboardManager).Reply(); err == nil && owner.Owner == t.win {
				xproto.SetSelectionOwner(t.conn, 0, t.clipboardManager, xproto.TimeCurrentTime)
			}
		}
		if t.xfixesAvailable {
			for _, selection := range []xproto.Atom{t.clipboardManager, t.atoms["CLIPBOARD"], t.atoms["PRIMARY"]} {
				if selection != 0 {
					xfixes.SelectSelectionInput(t.conn, t.win, selection, 0)
				}
			}
		}
		return t.clipboardSync.SetEnabled(false, nil)
	}

	seed := make(map[byte]string, 2)
	for _, selection := range []byte{'c', 'p'} {
		atom := t.clipboardAtom(selection)
		owner, err := xproto.GetSelectionOwner(t.conn, atom).Reply()
		if err != nil || owner.Owner != 0 && owner.Owner != t.win {
			continue
		}
		if selection == 'c' && t.clipboardSet {
			seed[selection] = t.clipboardText
		} else if selection == 'p' && t.primarySet {
			seed[selection] = t.selectionText
		}
	}
	if !t.clipboardSync.SetEnabled(true, seed) {
		return false
	}
	if t.xfixesAvailable {
		for _, selection := range []xproto.Atom{t.clipboardManager, t.atoms["CLIPBOARD"], t.atoms["PRIMARY"]} {
			if selection != 0 {
				xfixes.SelectSelectionInput(t.conn, t.win, selection, clipboardSelectionMask)
			}
		}
	}
	t.tryClipboardBridge()
	return true
}

func (t *Driver) clipboardAtom(selection byte) xproto.Atom {
	if selection == 'p' {
		return t.atoms["PRIMARY"]
	}
	return t.atoms["CLIPBOARD"]
}

func (t *Driver) clipboardByte(selection xproto.Atom) (byte, bool) {
	if selection == t.atoms["CLIPBOARD"] {
		return 'c', true
	}
	if selection == t.atoms["PRIMARY"] {
		return 'p', true
	}
	return 0, false
}

func (t *Driver) tryClipboardBridge() {
	if !t.ClipboardSyncState().Enabled || t.clipboardManager == 0 {
		return
	}
	owner, err := xproto.GetSelectionOwner(t.conn, t.clipboardManager).Reply()
	if err != nil {
		return
	}
	if owner.Owner == 0 {
		xproto.SetSelectionOwner(t.conn, t.win, t.clipboardManager, xproto.TimeCurrentTime)
		t.conn.Sync()
		owner, err = xproto.GetSelectionOwner(t.conn, t.clipboardManager).Reply()
		if err != nil {
			return
		}
	}
	t.setClipboardBridge(owner.Owner == t.win)
}

func (t *Driver) setClipboardBridge(active bool) {
	if t.clipboardBridge == active {
		return
	}
	t.clipboardBridge = active
	t.clipboardSync.SetWatching(active)
	if !active {
		return
	}
	for _, selection := range []byte{'c', 'p'} {
		atom := t.clipboardAtom(selection)
		owner, err := xproto.GetSelectionOwner(t.conn, atom).Reply()
		if err != nil {
			continue
		}
		t.clipboardOwners[selection] = owner.Owner
		switch {
		case owner.Owner != 0 && owner.Owner != t.win:
			t.queueExternalClipboard(selection, owner.Owner)
		}
	}
}

func (t *Driver) sharedClipboardChanged(entry sharedclipboard.Entry) {
	if t.callbacks.Lock == nil {
		return
	}
	t.callbacks.Lock()
	defer t.callbacks.Unlock()
	if t.closed() || !t.ClipboardSyncState().Enabled {
		return
	}
	t.sharedClipboard[entry.Selection] = entry
	if entry.Source == sharedclipboard.SourceXExternal && entry.Display == t.displayID {
		delete(t.externalImport, entry.Selection)
	}
	if entry.Selection == 'c' {
		t.clipboardText, t.clipboardSet = entry.Text, true
	} else {
		t.selectionText, t.primarySet = entry.Text, true
	}
	if t.clipboardBridge && t.externalPending[entry.Selection] == 0 && t.externalImport[entry.Selection] == 0 {
		t.claimSharedClipboard(entry)
	}
}

func (t *Driver) claimSharedClipboard(entry sharedclipboard.Entry) {
	if entry.Source == sharedclipboard.SourceXExternal && entry.Display == t.displayID {
		return
	}
	atom := t.clipboardAtom(entry.Selection)
	xproto.SetSelectionOwner(t.conn, t.win, atom, xproto.TimeCurrentTime)
	t.clipboardOwners[entry.Selection] = t.win
}

func (t *Driver) XFixesSelectionNotify(event xfixes.SelectionNotifyEvent) {
	if !t.ClipboardSyncState().Enabled {
		return
	}
	if event.Selection == t.clipboardManager {
		if event.Owner == 0 {
			t.setClipboardBridge(false)
			t.tryClipboardBridge()
		} else {
			t.setClipboardBridge(event.Owner == t.win)
		}
		return
	}
	selection, ok := t.clipboardByte(event.Selection)
	if !ok || !t.clipboardBridge {
		return
	}
	t.clipboardOwners[selection] = event.Owner
	if event.Owner != 0 && event.Owner != t.win {
		t.queueExternalClipboard(selection, event.Owner)
	} else if event.Owner == 0 {
		delete(t.externalPending, selection)
		delete(t.externalImport, selection)
		if entry, found := t.sharedClipboard[selection]; found {
			t.claimSharedClipboard(entry)
		}
	} else {
		delete(t.externalPending, selection)
		delete(t.externalImport, selection)
	}
}

func (t *Driver) queueExternalClipboard(selection byte, owner xproto.Window) {
	t.externalPending[selection] = owner
	t.tryExternalClipboard()
}

func (t *Driver) tryExternalClipboard() {
	if t.clipboardQuery != nil || !t.clipboardBridge {
		return
	}
	for _, selection := range []byte{'c', 'p'} {
		owner := t.externalPending[selection]
		if owner == 0 {
			continue
		}
		delete(t.externalPending, selection)
		t.externalImport[selection] = owner
		if t.queryNativeClipboard(selection, func(text string, ok bool) {
			if ok && t.clipboardBridge && t.clipboardOwners[selection] == owner {
				t.clipboardSync.Publish(selection, text, sharedclipboard.SourceXExternal, t.displayID)
			} else {
				delete(t.externalImport, selection)
			}
			t.tryExternalClipboard()
		}) {
			return
		}
		delete(t.externalImport, selection)
		t.externalPending[selection] = owner
		return
	}
}
