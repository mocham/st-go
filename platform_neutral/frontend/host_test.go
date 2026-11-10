package frontend

import (
	"bytes"
	"image"
	"testing"

	"st-go/config"
	"st-go/platform_neutral/api"
	term "st-go/platform_neutral/stterminal"
)

type recordingHost struct {
	api.Host
	uploads   []image.Rectangle
	clipboard string
}

func (h *recordingHost) Upload(pixels []uint32, stride, x, y, width, height int) {
	h.uploads = append(h.uploads, image.Rect(x, y, x+width, y+height))
}
func (h *recordingHost) SetClipboard(selection byte, text string) { h.clipboard = text }
func (h *recordingHost) QueryClipboard(selection byte, reply func(string, bool)) {
	reply(h.clipboard, true)
}

func TestUniformHostCallbacksKeepTerminalInputSemantics(t *testing.T) {
	trm := regionTestTerminal(t)
	host := &recordingHost{}
	trm.host = host
	// regionTestTerminal's cleanup calls Close through the injected host.
	defer func() { trm.host = nil }()
	trm.loadInputConfig(config.Default())
	var input bytes.Buffer
	trm.termCore.SetWriter(func(data []byte) { input.Write(data) })
	callbacks := trm.eventCallbacks()
	callbacks.KeyPress('a', 0)
	callbacks.KeyPress('c', ControlMask)
	callbacks.Text("café 世界")
	trm.writeTerminalOutput(trm.termCore, []byte("\x1b[?1004h\x1b[?2004h"))
	callbacks.Focus(true)
	callbacks.Focus(false)
	callbacks.Paste("paste\ntext")
	if got, want := input.String(), "a\x03café 世界\x1b[I\x1b[O\x1b[200~paste\rtext\x1b[201~"; got != want {
		t.Fatalf("input=%q want %q", got, want)
	}
	trm.SetClipboard('c', "copied")
	trm.QueryClipboard('c', func(text string, ok bool) {
		if !ok || text != "copied" {
			t.Fatalf("clipboard reply=%q,%v", text, ok)
		}
	})
}

type noGeometryHost struct{ api.Host }

func (*noGeometryHost) SupportsWindowGeometry() bool { return false }

func TestHostWithoutDesktopGeometryDoesNotArmRestoreClick(t *testing.T) {
	trm := regionTestTerminal(t)
	trm.host = &noGeometryHost{}
	defer func() { trm.host = nil }()
	trm.WindowGeometry(term.WindowGeometryRequest{Action: term.GeometryRemember, Tag: "browser"})
	trm.WindowGeometry(term.WindowGeometryRequest{Action: term.GeometryPlace, RestoreTag: "browser"})
	if len(trm.geometryTags) != 0 || trm.restoreGeometryTag != "" {
		t.Fatal("unsupported geometry intercepted browser mouse input")
	}
}

func TestUniformPainterKeepsSeparatedDirtyRows(t *testing.T) {
	trm := regionTestTerminal(t)
	host := &recordingHost{}
	trm.host = host
	defer func() { trm.host = nil }()
	trm.writeTerminalOutput(trm.termCore, []byte("\x1b[?2026h\x1b[1;2H\x1b[48;2;17;34;51m \x1b[5;2H \x1b[1;2H\x1b[?2026l"))
	trm.paintTerminalOutput(trm.termCore)
	if len(host.uploads) == 0 {
		t.Fatal("painter received no damage")
	}
	clean := image.Rect(0, trm.borderpx+trm.ch, trm.fbW, trm.borderpx+4*trm.ch)
	for _, damage := range host.uploads {
		if !damage.Intersect(clean).Empty() {
			t.Fatalf("upload bridges clean rows: %v", damage)
		}
	}
}
