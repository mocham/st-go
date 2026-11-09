package term

import (
	"encoding/base64"
	"fmt"
	"strings"
	"unicode/utf8"

	"st-go/limits"
)

// ClipboardHooks is an optional, backend-neutral frontend capability. Replies
// must be delivered on the frontend's serialized terminal/event loop.
type ClipboardHooks interface {
	SetClipboard(selection byte, text string)
	QueryClipboard(selection byte, reply func(text string, ok bool))
}

const maxClipboardControl = (limits.ClipboardBytes+2)/3*4 + 64

func (t *Term) oscClipboard() {
	if t.strescseq.narg != 3 {
		return
	}
	selection := t.strescseq.args[1]
	if selection == "" {
		selection = "c"
	}
	if len(selection) > 2 || strings.Trim(selection, "cp") != "" {
		return
	}
	hooks, ok := t.hooks.(ClipboardHooks)
	if !ok {
		return
	}
	data := t.strescseq.args[2]
	if data == "?" {
		if !t.cfg.AllowClipboardRead {
			return
		}
		sel := selection[0]
		hooks.QueryClipboard(sel, func(text string, ok bool) {
			if !ok || len(text) > limits.ClipboardBytes || !utf8.ValidString(text) {
				text = ""
			}
			t.ttywrite([]byte(fmt.Sprintf("\x1b]52;%c;%s\x1b\\", sel, base64.StdEncoding.EncodeToString([]byte(text)))), false)
		})
		return
	}
	if !t.cfg.AllowClipboardWrite || len(data) > base64.StdEncoding.EncodedLen(limits.ClipboardBytes) {
		return
	}
	decoded, err := base64.StdEncoding.DecodeString(data)
	if err != nil || len(decoded) > limits.ClipboardBytes || !utf8.Valid(decoded) {
		return
	}
	for _, sel := range []byte(selection) {
		hooks.SetClipboard(sel, string(decoded))
	}
}
