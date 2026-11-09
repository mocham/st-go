package main

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"st-go/term"
)

func contentsCommand(t *testing.T, output string) term.RegionList {
	t.Helper()
	prefix := "\x1bPregion secondary list "
	at := strings.LastIndex(output, prefix)
	if at < 0 {
		t.Fatalf("missing TOC command: %q", output)
	}
	value, _, _ := strings.Cut(output[at+len(prefix):], "\x1b\\")
	data, err := base64.StdEncoding.DecodeString(value)
	if err != nil {
		t.Fatal(err)
	}
	var list term.RegionList
	if err := json.Unmarshal(data, &list); err != nil {
		t.Fatal(err)
	}
	if err := list.Validate(); err != nil {
		t.Fatal(err)
	}
	return list
}

func TestContentsSwitchesSamePanelAndTracksUnsavedPositions(t *testing.T) {
	text := "\\chapter{Chapter}\n\\section{Section}\ntext\n\\label{sec:x}\n"
	e, out, calls := newPreviewTestEditor(t, "notes.tex", text)
	settlePreview(t, e)
	awaitSyntax(t, e, e.buffer())
	var r renderer
	if err := e.draw(&r, io.Discard); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	e.Feed(keyEvent{Key: "<S-Tab>"})
	if err := e.preview.tick(e, time.Now()); err != nil {
		t.Fatal(err)
	}
	var mainOutput strings.Builder
	if err := e.draw(&r, &mainOutput); err != nil {
		t.Fatal(err)
	}
	if mainOutput.Len() != 0 {
		t.Fatalf("switching panels repainted the editor: %q", mainOutput.String())
	}
	list := contentsCommand(t, out.String())
	if len(list.Items) != 3 || list.Items[2].Level != 4 || strings.Contains(out.String(), "region split") || strings.Contains(out.String(), "region reset") {
		t.Fatalf("TOC did not reuse panel: %q", out.String())
	}
	count := calls.Load()
	out.Reset()
	// Moving source offsets without changing headings must not repaint the list.
	if !e.replace(0, 0, "before\n", 0) {
		t.Fatal("edit failed")
	}
	if err := e.preview.tick(e, time.Now()); err != nil {
		t.Fatal(err)
	}
	if out.Len() != 0 || calls.Load() != count {
		t.Fatalf("inert source edit repainted/rerendered: %q", out.String())
	}
	e.Feed(keyEvent{Preview: &previewEvent{kind: "activate", token: list.Token, value: 3}})
	if e.pos() != strings.Index(string(e.buffer().text), "\\label") || !e.buffer().dirty() {
		t.Fatalf("TOC did not navigate unsaved source: pos=%d", e.pos())
	}
	if e.fullRedraw {
		t.Fatal("TOC navigation forced a full editor redraw")
	}
	if len(e.jumps) == 0 {
		t.Fatal("TOC click did not enter jump history")
	}
	if data, err := os.ReadFile(e.buffer().meta.Path); err != nil || string(data) != text {
		t.Fatal("TOC wrote the source file")
	}
	out.Reset()
	e.Feed(keyEvent{Key: "<S-Tab>"})
	if !strings.Contains(out.String(), "region secondary list restore") || e.preview.toc.active {
		t.Fatalf("preview was not restored: %q", out.String())
	}
}

func TestContentsRejectsStaleClicksAndPreservesTabBinding(t *testing.T) {
	e, out, _ := newPreviewTestEditor(t, "notes.tex", "\\section{One}\n\\section{Two}\n")
	settlePreview(t, e)
	e.Feed(keyEvent{Key: "<S-Tab>"})
	e.preview.tick(e, time.Now())
	old := contentsCommand(t, out.String())
	e.replace(0, 0, "\\chapter{New}\n", 0)
	out.Reset()
	e.preview.tick(e, time.Now())
	next := contentsCommand(t, out.String())
	if next.Token == old.Token {
		t.Fatal("structural change retained old click token")
	}
	pos := e.pos()
	e.Feed(keyEvent{Preview: &previewEvent{kind: "activate", token: old.Token, value: 2}})
	if e.pos() != pos {
		t.Fatal("stale click navigated to an unrelated entry")
	}
	e.Feed(keyEvent{Preview: &previewEvent{kind: "activate", token: next.Token, value: 3}})
	want := e.pos()
	e.Feed(keyEvent{Key: "\x0f"})
	if e.pos() == want {
		t.Fatal("Ctrl-O did not return from TOC jump")
	}
	e.Feed(keyEvent{Key: "<Tab>"})
	if e.pos() != want || !e.preview.toc.active {
		t.Fatal("Tab no longer moves forward through jumps")
	}
	// A hidden preview completion must not displace the list on a save.
	e.execute("w")
	out.Reset()
	e.preview.tick(e, time.Now())
	if strings.Contains(out.String(), "region secondary open") {
		t.Fatal("save replaced the TOC with a PDF")
	}
}

func TestContentsInputAndClickProtocol(t *testing.T) {
	for _, sequence := range []string{"\x1b[Z", "\x1b[1;2Z"} {
		var parser inputParser
		var events []keyEvent
		for _, b := range []byte(sequence) {
			parser.feed([]byte{b}, time.Now(), func(e keyEvent) { events = append(events, e) })
		}
		if len(events) != 1 || events[0].Key != "<S-Tab>" {
			t.Fatalf("Shift-Tab = %+v", events)
		}
	}
	if event := parsePreviewEvent("region secondary event activate 17 4"); event == nil || event.token != 17 || event.value != 4 {
		t.Fatal("click event not parsed")
	}
	for _, control := range []string{"region secondary event activate 0 1", "region secondary event activate 1 0", "region secondary event activate 1 99999", "region secondary event activate 1 2 extra"} {
		if parsePreviewEvent(control) != nil {
			t.Fatalf("accepted invalid click: %q", control)
		}
	}
}

func TestContentsHandlesInitialNegotiationAndResize(t *testing.T) {
	e, out, _ := newPreviewTestEditor(t, "notes.tex", "\\section{One}\n")
	e.Feed(keyEvent{Key: "<S-Tab>"})
	if err := e.preview.tick(e, time.Now()); err != nil {
		t.Fatal(err)
	}
	e.preview.event(e, previewEvent{kind: "layout", cols: 32, rows: 24, secondary: true})
	if !e.preview.ready {
		t.Fatal("TOC ignored initial region confirmation")
	}
	if err := e.preview.tick(e, time.Now()); err != nil {
		t.Fatal(err)
	}
	if list := contentsCommand(t, out.String()); len(list.Items) != 1 {
		t.Fatal("TOC not published after initial negotiation")
	}
	e.preview.event(e, previewEvent{kind: "layout", cols: 38, rows: 25, secondary: true})
	if e.cols != 38 || e.rows != 25 {
		t.Fatal("TOC blocked editor resize notification")
	}
}

func TestPTYShiftTabContentsNavigatesAndRestoresPreview(t *testing.T) {
	path := filepath.Join(t.TempDir(), "notes.tex")
	initial := "\\documentclass{article}\n\\begin{document}\n\\section{One}\ntext\n\\section{Two}\n\\end{document}\n"
	if err := os.WriteFile(path, []byte(initial), 0600); err != nil {
		t.Fatal(err)
	}
	p := startPTYEditor(t, "--preview-gap", "0", path)
	p.await(t, "region query", func() bool { return strings.Contains(p.text(), "region query") })
	p.send(t, "\x1bPregion layout main 1 1 32 24 secondary 33 1 48 24\x1b\\")
	p.await(t, "preview", func() bool { return strings.Contains(p.text(), "region secondary open ") })
	p.send(t, "\x1b[Z")
	p.await(t, "contents", func() bool { return strings.Contains(p.text(), "region secondary list ") })
	list := contentsCommand(t, p.text())
	if len(list.Items) != 2 {
		t.Fatalf("TOC = %+v", list)
	}
	p.send(t, fmt.Sprintf("\x1bPregion secondary event activate %d 2\x1b\\", list.Token))
	p.await(t, "editor navigation", func() bool { return strings.Contains(p.screenText(), "5,1") })
	p.send(t, "\x1b[1;2Z")
	p.await(t, "preview restore", func() bool { return strings.Contains(p.text(), "region secondary list restore") })
	p.send(t, ":q!\r")
	select {
	case <-p.done:
	case <-time.After(5 * time.Second):
		t.Fatal("editor did not exit")
	}
	if p.err != nil {
		t.Fatalf("exit: %v", p.err)
	}
	if data, err := os.ReadFile(path); err != nil || string(data) != initial {
		t.Fatal("contents navigation changed disk")
	}
}
