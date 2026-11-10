package frontend

import (
	"image"
	"slices"
	"testing"

	"st-go/platform_neutral/api"
	term "st-go/platform_neutral/stterminal"
)

type cursorRecordingHost struct {
	recordingHost
	cursor api.TextCursor
}

func (h *cursorRecordingHost) SetTextCursor(c api.TextCursor) {
	h.cursor = api.TextCursor{Bounds: c.Bounds, Pixels: slices.Clone(c.Pixels), Base: slices.Clone(c.Base)}
}

func TestTextCursorMovementAndFocusDoNotRepaintGlyphs(t *testing.T) {
	trm := regionTestTerminal(t)
	host := &cursorRecordingHost{}
	trm.host = host
	defer func() { trm.host = nil }()
	trm.cursorShape = 5
	trm.winMode |= term.ModeFocused
	trm.writeTerminalOutput(trm.termCore, []byte("abc"))
	trm.paint()
	before := slices.Clone(framebuf)
	host.uploads = nil
	trm.writeTerminalOutput(trm.termCore, []byte("\x1b[4;5H"))
	trm.paint()
	if !slices.Equal(before, framebuf) || len(host.uploads) != 0 {
		t.Fatal("cursor-only movement changed/uploaded glyph content", host.uploads)
	}
	if want := image.Rect(trm.borderpx+4*trm.cw, trm.borderpx+3*trm.ch, trm.borderpx+5*trm.cw, trm.borderpx+4*trm.ch); host.cursor.Bounds != want || !host.cursor.Valid() {
		t.Fatal("caret did not move independently", host.cursor.Bounds)
	}
	trm.focus(false)
	if len(host.uploads) != 0 || !slices.Equal(before, framebuf) {
		t.Fatal("caret focus change repainted glyphs")
	}
	trm.writeTerminalOutput(trm.termCore, []byte("\x1b[?25l"))
	trm.paint()
	if !host.cursor.Bounds.Empty() || len(host.uploads) != 0 || !slices.Equal(before, framebuf) {
		t.Fatal("hiding caret repainted glyphs")
	}
}

func TestTextCursorShapesKeepContentPristineAndClipWideCells(t *testing.T) {
	trm := regionTestTerminal(t)
	trm.cursorThick = 2
	before := slices.Clone(framebuf)
	for shape := 0; shape <= 7; shape++ {
		for _, focused := range []bool{false, true} {
			trm.cursorShape = shape
			if focused {
				trm.winMode |= term.ModeFocused
			} else {
				trm.winMode &^= term.ModeFocused
			}
			trm.DrawCursor(trm.cols-1, 0, term.Glyph{U: ' ', Mode: term.ATTRWide}, 0, 0, term.Glyph{})
			if !trm.textCursor.Valid() || !trm.textCursor.Bounds.In(trm.terminalClip()) || !slices.Equal(before, framebuf) {
				t.Fatal("caret changed content or escaped terminal bounds", shape, focused)
			}
		}
	}
}

func TestTextCursorUsesPaintSchedulerAndSynchronizedOutput(t *testing.T) {
	trm := regionTestTerminal(t)
	host := &cursorRecordingHost{}
	trm.host = host
	defer func() { trm.host = nil }()
	trm.paint()
	old := host.cursor
	host.uploads = nil
	trm.writeTerminalOutput(trm.termCore, []byte("\x1b[?2026h\x1b[4;5H\x1b[?25l"))
	trm.paintTerminalOutput(trm.termCore)
	if !host.cursor.Equal(old) || len(host.uploads) != 0 {
		t.Fatal("caret escaped synchronized output")
	}
	trm.writeTerminalOutput(trm.termCore, []byte("\x1b[?2026l"))
	trm.paintTerminalOutput(trm.termCore)
	if !host.cursor.Bounds.Empty() || len(host.uploads) != 0 || trm.termCore.HasPendingPaint() {
		t.Fatal("caret-only transaction did not commit independently")
	}
	trm.writeTerminalOutput(trm.termCore, []byte("\x1b[?25h\x1b[5 q\t"))
	trm.paintTerminalOutput(trm.termCore)
	if !host.cursor.Valid() || len(host.uploads) != 0 {
		t.Fatal("show/shape/tab did not schedule independent caret presentation")
	}
}
