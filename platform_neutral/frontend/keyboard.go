package frontend

import term "st-go/platform_neutral/stterminal"

func (t *Terminal) setupKeys() {
	if t.host != nil {
		t.host.SetupKeys()
	}
}
func (t *Terminal) handleShortcut(ks uint, state uint) bool {
	for _, s := range t.shortcuts {
		if s.keysym == ks && match(s.mask, int(state)) {
			return t.runAction(s.action, s.arg)
		}
	}
	return false
}
func (t *Terminal) runAction(action, arg string) bool {
	switch action {
	case "clipcopy":
		t.clipcopy()
	case "clippaste":
		t.clippaste()
	case "selpaste":
		if sel := t.termCore.GetSel(); sel != "" {
			t.pasteToTTY(sel)
		}
	case "sendbreak", "zoom", "zoomreset", "printscreen", "printsel", "toggleprinter", "numlock":
	}
	return true
}
func (t *Terminal) pasteToTTY(s string) {
	if t.deferIME(imeInput{text: s, paste: true}) {
		return
	}
	if t.inputMethod.stream != nil {
		t.imeCommit(t.inputMethod.stream.Flush())
		t.paintIME()
	}
	if t.termCore.WinMode()&term.ModeBrcktPaste != 0 {
		t.termCore.WriteToTTY([]byte("\x1b[200~"), false)
	}
	t.termCore.WriteToTTY([]byte(s), false)
	if t.termCore.WinMode()&term.ModeBrcktPaste != 0 {
		t.termCore.WriteToTTY([]byte("\x1b[201~"), false)
	}
}
