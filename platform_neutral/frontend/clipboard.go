package frontend

import "st-go/platform_neutral/api"

func (t *Terminal) clipcopy() {
	if sel := t.termCore.GetSel(); sel != "" {
		t.SetClipboard('c', sel)
	}
}
func (t *Terminal) clippaste() {
	if t.host != nil {
		t.host.RequestPaste()
	}
}
func (t *Terminal) setSelection(text string) { t.SetClipboard('p', text) }
func (t *Terminal) SetClipboard(selection byte, text string) {
	if t.host != nil && !t.closed() {
		t.host.SetClipboard(selection, text)
	}
}
func (t *Terminal) QueryClipboard(selection byte, reply func(string, bool)) {
	if t.host == nil || t.closed() {
		reply("", false)
		return
	}
	t.host.QueryClipboard(selection, reply)
}
func (t *Terminal) SetClipboardSync(enabled bool) bool {
	identity, ok := t.host.(api.BackendIdentity)
	if !ok || identity.BackendName() != "x11" {
		return false
	}
	control, ok := t.host.(api.ClipboardSyncControl)
	return ok && control.SetClipboardSync(enabled)
}
func (t *Terminal) ClipboardSyncState() api.ClipboardSyncState {
	identity, ok := t.host.(api.BackendIdentity)
	if !ok || identity.BackendName() != "x11" {
		return api.ClipboardSyncState{}
	}
	if control, ok := t.host.(api.ClipboardSyncControl); ok {
		return control.ClipboardSyncState()
	}
	return api.ClipboardSyncState{}
}
func (t *Terminal) SetSel(text string)   { t.setSelection(text) }
func (t *Terminal) SetTitle(text string) { t.setTitle(text) }
func (t *Terminal) SetIconTitle(text string) {
	if text == "" || t.lockTitle {
		return
	}
	t.iconTitle = text
	t.host.SetIconTitle(text)
}

func replaceNewlines(s string) string {
	out := make([]byte, 0, len(s))
	for i := 0; i < len(s); i++ {
		if s[i] == '\n' {
			out = append(out, '\r')
		} else {
			out = append(out, s[i])
		}
	}
	return string(out)
}
