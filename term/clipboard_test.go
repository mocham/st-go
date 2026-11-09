package term

import (
	"encoding/base64"
	"strings"
	"testing"

	"st-go/config"
)

type clipboardHooks struct {
	mockHooks
	selection     byte
	text          string
	writes, reads int
}

func (h *clipboardHooks) SetClipboard(selection byte, text string) {
	h.selection, h.text = selection, text
	h.writes++
}
func (h *clipboardHooks) QueryClipboard(selection byte, reply func(string, bool)) {
	h.reads++
	reply(h.text, true)
}
func TestOSC52FrontendBoundary(t *testing.T) {
	cfg := config.Default()
	h := &clipboardHooks{}
	core := NewTerm(cfg, h)
	text := "café 世界\n"
	data := base64.StdEncoding.EncodeToString([]byte(text))
	core.Twrite([]byte("\x1b]52;c;"+data+"\x1b\\"), false)
	if h.writes != 1 || h.selection != 'c' || h.text != text {
		t.Fatal("clipboard write did not reach optional frontend")
	}
	core.Twrite([]byte("\x1b]52;p;\x07"), false)
	if h.writes != 2 || h.selection != 'p' || h.text != "" {
		t.Fatal("empty PRIMARY copy failed")
	}
	var reply strings.Builder
	core.SetWriter(func(b []byte) { reply.Write(b) })
	core.Twrite([]byte("\x1b]52;c;?\x07"), false)
	if h.reads != 0 || reply.Len() != 0 {
		t.Fatal("clipboard read enabled without permission")
	}
	cfg.AllowClipboardRead = true
	h.text = text
	core.Twrite([]byte("\x1b]52;c;?\x07"), false)
	if h.reads != 1 || reply.String() != "\x1b]52;c;"+data+"\x1b\\" {
		t.Fatalf("query response=%q", reply.String())
	}
	cfg.AllowClipboardWrite = false
	core.Twrite([]byte("\x1b]52;c;"+data+"\x07"), false)
	if h.writes != 2 {
		t.Fatal("clipboard write permission ignored")
	}
}
