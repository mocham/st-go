package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"st-go/tui/preview"
)

func fakePreviewRender(request previewRequest, stop <-chan struct{}) previewResult {
	result := previewResult{request: request}
	result.source, result.err = preview.Load(request.path, func(path string) (string, error) {
		if text, ok := request.overrides[path]; ok {
			return text, nil
		}
		data, err := os.ReadFile(path)
		return string(data), err
	})
	if result.err == nil && (!request.reuse || request.hash != result.source.Hash) {
		result.pdf = []byte("%PDF-1.4 synthetic fixture\n")
	}
	return result
}

func newPreviewTestEditor(t *testing.T, name, text string) (*Editor, *strings.Builder, *atomic.Int32) {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, []byte(text), 0600); err != nil {
		t.Fatal(err)
	}
	e := memoryEditor(text)
	b := e.buffer()
	b.meta.Path = path
	b.meta.Existed = true
	b.meta.DiskHash = digest([]byte(text))
	out := &strings.Builder{}
	options := defaultPreviewOptions()
	options.gap = 0
	p := newEditorPreview(options, out)
	e.preview = p
	calls := &atomic.Int32{}
	p.render = func(request previewRequest, stop <-chan struct{}) previewResult {
		calls.Add(1)
		return fakePreviewRender(request, stop)
	}
	t.Cleanup(func() {
		if err := e.close(false); err != nil {
			t.Error(err)
		}
	})
	return e, out, calls
}

func settlePreview(t *testing.T, e *Editor) {
	t.Helper()
	p := e.preview
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if err := p.tick(e, time.Now()); err != nil {
			t.Fatal(err)
		}
		if p.split && !p.ready && !p.unsupported {
			p.event(e, previewEvent{kind: "layout", cols: 32, rows: 24, secondary: true})
		}
		if p.published && !p.busy && p.pending == nil && p.completed == nil {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("preview did not settle: pending=%v busy=%v ready=%v message=%s", p.pending != nil, p.busy, p.ready, e.message)
}

func TestPreviewSaveCursorMarkerAndNoMove(t *testing.T) {
	e, out, calls := newPreviewTestEditor(t, "notes.md", "# Title\n\nOne\nTwo\nThree\nFour\n")
	e.setPos(e.buffer().start(3))
	settlePreview(t, e)
	if !strings.Contains(out.String(), "region split 60%") || !strings.Contains(out.String(), "fit-width continuous") || !strings.Contains(out.String(), "region secondary position ") {
		t.Fatalf("missing initial preview: %q", out.String())
	}
	initialCalls := calls.Load()
	out.Reset()
	// Unchanged saves navigate, without regenerating/reopening the PDF.
	e.setPos(e.buffer().start(4))
	e.execute("w")
	settlePreview(t, e)
	want := fmt.Sprintf("region secondary position %d", e.preview.source.Position(e.buffer().meta.Path, 5))
	if !strings.Contains(out.String(), want) || strings.Contains(out.String(), "region secondary open ") {
		t.Fatalf("unchanged save didn't seek without reopening: %q", out.String())
	}
	if calls.Load() != initialCalls+1 {
		t.Fatal("save hook did not prepare exactly once")
	}
	// A marker overrides the editor's saved cursor, including a trailing marker.
	e.replace(e.buffer().start(2), e.buffer().end(2), "One %cursor anchor", 0)
	e.setPos(e.buffer().start(5))
	out.Reset()
	e.execute("write")
	settlePreview(t, e)
	want = fmt.Sprintf("region secondary position %d", e.preview.source.ExpandedPosition(3))
	if !strings.Contains(out.String(), want) {
		t.Fatalf("marker wasn't preferred: %q", out.String())
	}
	// wnomove suppresses both marker and cursor jumps, while publishing changes.
	e.replace(e.buffer().end(4), e.buffer().end(4), " changed", 0)
	out.Reset()
	e.execute("wnomove")
	settlePreview(t, e)
	if !strings.Contains(out.String(), "region secondary open ") || strings.Contains(out.String(), "region secondary position ") {
		t.Fatalf("wnomove moved or failed to refresh: %q", out.String())
	}
	if data, err := os.ReadFile(e.buffer().meta.Path); err != nil || !strings.Contains(string(data), "changed") {
		t.Fatalf("wnomove didn't save: %q %v", data, err)
	}
}

func TestPreviewDoesNotPollFilesOrRenderUnsavedEdits(t *testing.T) {
	e, out, calls := newPreviewTestEditor(t, "notes.tex", "\\begin{document}\nOriginal\n\\end{document}\n")
	settlePreview(t, e)
	hash := e.preview.source.Hash
	count := calls.Load()
	out.Reset()
	e.replace(e.buffer().start(1), e.buffer().end(1), "Unsaved", 0)
	for i := 0; i < 20; i++ {
		if err := e.preview.tick(e, time.Now().Add(time.Duration(i)*time.Hour)); err != nil {
			t.Fatal(err)
		}
	}
	if calls.Load() != count || e.preview.source.Hash != hash || out.Len() != 0 {
		t.Fatal("unsaved editing triggered preview work")
	}
	if err := os.WriteFile(e.buffer().meta.Path, []byte("\\begin{document}\nExternal\n\\end{document}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 20; i++ {
		e.preview.tick(e, time.Now().Add(time.Duration(i)*time.Hour))
	}
	if calls.Load() != count || e.preview.source.Hash != hash {
		t.Fatal("preview polled an external file change")
	}
	e.execute("w") // conflict, not a successful save
	if e.preview.pending != nil || calls.Load() != count {
		t.Fatal("failed save invoked the preview hook")
	}
	e.execute("e!")
	settlePreview(t, e)
	if !strings.Contains(e.preview.source.Source, "External") {
		t.Fatal("explicit reload did not refresh")
	}
}

func TestPreviewIncludedBufferSaveAndMainCursor(t *testing.T) {
	e, out, _ := newPreviewTestEditor(t, "main.tex", "\\begin{document}\nRoot before\n\\input{part}\nRoot after\n\\end{document}\n")
	child := filepath.Join(filepath.Dir(e.buffer().meta.Path), "part.tex")
	if err := os.WriteFile(child, []byte("%main=main\nChild first\nChild second\n"), 0600); err != nil {
		t.Fatal(err)
	}
	settlePreview(t, e)
	buffer, err := loadBuffer(child)
	if err != nil {
		t.Fatal(err)
	}
	e.buffers = append(e.buffers, buffer)
	if _, err := buffer.change([]patch{{buffer.start(1), "Child first", "Child saved"}}, 0, 0, 0); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	e.execute("wall")
	settlePreview(t, e)
	if !strings.Contains(e.preview.source.Source, "Child saved") || !strings.Contains(out.String(), "region secondary open ") {
		t.Fatal("saving an included buffer didn't refresh its parent")
	}
	e.switchBuffer(buffer)
	e.setPos(buffer.start(2))
	out.Reset()
	settlePreview(t, e)
	if e.preview.source.Root == child {
		t.Fatal("%main redirect was not followed")
	}
	want := fmt.Sprintf("region secondary position %d", e.preview.source.Position(child, 3))
	if !strings.Contains(out.String(), want) {
		t.Fatalf("included editor cursor wasn't mapped: %q", out.String())
	}
}

func TestPreviewCoalescesSavesAndRejectsStaleResult(t *testing.T) {
	e, out, calls := newPreviewTestEditor(t, "notes.md", "# Initial\n\nText\n")
	settlePreview(t, e)
	release := make(chan struct{})
	started := make(chan struct{}, 1)
	normal := e.preview.render
	e.preview.render = func(request previewRequest, stop <-chan struct{}) previewResult {
		started <- struct{}{}
		select {
		case <-release:
		case <-stop:
		}
		return normal(request, stop)
	}
	e.replace(e.buffer().start(2), e.buffer().end(2), "Intermediate", 0)
	e.execute("w")
	e.preview.tick(e, time.Now())
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("worker did not start")
	}
	for _, text := range []string{"Second", "Latest"} {
		e.replace(e.buffer().start(2), e.buffer().end(2), text, 0)
		e.execute("wnomove")
	}
	e.preview.render = normal
	out.Reset()
	before := calls.Load()
	close(release)
	settlePreview(t, e)
	if !strings.Contains(e.preview.source.Source, "Latest") || strings.Contains(e.preview.source.Source, "Intermediate") {
		t.Fatal("stale result was published")
	}
	if got := strings.Count(out.String(), "region secondary open "); got != 1 {
		t.Fatalf("published %d revisions instead of latest only: %q", got, out.String())
	}
	if strings.Contains(out.String(), "region secondary position ") {
		t.Fatal("coalescing lost latest wnomove policy")
	}
	if calls.Load() != before+2 {
		t.Fatal("rapid saves weren't coalesced behind the one active worker")
	}
}

func TestPreviewFailureKeepsLastPDFAndCleanup(t *testing.T) {
	e, out, _ := newPreviewTestEditor(t, "notes.md", "# Initial\n")
	settlePreview(t, e)
	p := e.preview
	path, dir := p.pdfPath, p.directory
	old, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	p.render = func(request previewRequest, stop <-chan struct{}) previewResult {
		return previewResult{request: request, err: fmt.Errorf("synthetic render failure")}
	}
	e.replace(0, 0, "Changed\n", 0)
	e.execute("w")
	out.Reset()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		p.tick(e, time.Now())
		if !p.busy && p.pending == nil {
			break
		}
		time.Sleep(time.Millisecond)
	}
	now, err := os.ReadFile(path)
	if err != nil || string(now) != string(old) || strings.Contains(out.String(), "region secondary open ") {
		t.Fatal("failed render replaced the last successful PDF")
	}
	if err := p.close(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatal("preview temporary directory not removed")
	}
	if !strings.Contains(out.String(), "region reset") {
		t.Fatal("preview split was not restored")
	}
}

func TestPreviewGapCoalescesBeforeStartingWork(t *testing.T) {
	e, out, calls := newPreviewTestEditor(t, "notes.md", "# Initial\n")
	settlePreview(t, e)
	p := e.preview
	before := calls.Load()
	out.Reset()
	base := time.Now()
	p.options.gap = 2100 * time.Millisecond
	p.lastAttempt, p.lastPublished = base, base
	e.replace(0, 0, "Intermediate\n", 0)
	e.execute("w")
	if err := p.tick(e, base.Add(2*time.Second)); err != nil {
		t.Fatal(err)
	}
	if p.busy || calls.Load() != before || p.pending == nil {
		t.Fatal("preview render gap was ignored")
	}
	e.replace(0, len("Intermediate"), "Latest", 0)
	e.execute("wnomove")
	ready := base.Add(p.options.gap)
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if err := p.tick(e, ready); err != nil {
			t.Fatal(err)
		}
		if !p.busy && p.pending == nil && p.completed == nil {
			break
		}
		time.Sleep(time.Millisecond)
	}
	if calls.Load() != before+1 || !strings.Contains(p.source.Source, "Latest") || p.lastPublished.Sub(base) < p.options.gap {
		t.Fatal("waiting saves did not coalesce into one spaced publication")
	}
	if strings.Count(out.String(), "region secondary open ") != 1 || strings.Contains(out.String(), "region secondary position ") {
		t.Fatal("coalescing lost publication or wnomove policy")
	}
}

func TestPreviewTerminalNegotiationAndBufferSwitch(t *testing.T) {
	e, out, calls := newPreviewTestEditor(t, "notes.md", "# Initial\n")
	p := e.preview
	now := time.Now()
	if err := p.tick(e, now); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 0 {
		t.Fatal("rendered before terminal confirmed support")
	}
	if err := p.tick(e, now.Add(3*time.Second)); err != nil {
		t.Fatal(err)
	}
	if !p.unsupported || !strings.Contains(out.String(), "region reset") {
		t.Fatal("unsupported terminal not restored")
	}
	editKeys(e, "iStill editing<Esc>")
	if !strings.Contains(string(e.buffer().text), "Still editing") {
		t.Fatal("unsupported preview broke editing")
	}
	// A successful session restores full-width mode when leaving a preview file.
	p.unsupported = false
	p.split = false
	p.pending = nil
	p.path = ""
	settlePreview(t, e)
	out.Reset()
	other := &Buffer{meta: fileMetadata{Path: filepath.Join(t.TempDir(), "plain.txt")}, text: []rune("Plain")}
	e.buffers = append(e.buffers, other)
	e.switchBuffer(other)
	if err := p.tick(e, time.Now()); err != nil {
		t.Fatal(err)
	}
	if p.split || !strings.Contains(out.String(), "region reset") {
		t.Fatal("non-preview buffer kept the split")
	}
}

func TestPreviewProtocolFragmentsAndPasteIsolation(t *testing.T) {
	frame := "\x1bPregion layout main 1 1 32 24 secondary 33 1 48 24\x1b\\"
	for split := 0; split <= len(frame); split++ {
		var parser inputParser
		var events []keyEvent
		emit := func(event keyEvent) { events = append(events, event) }
		now := time.Now()
		parser.feed([]byte(frame[:split]), now, emit)
		parser.feed([]byte(frame[split:]+"i"), now, emit)
		if len(events) != 2 || events[0].Preview == nil || events[0].Preview.cols != 32 || events[1].Key != "i" {
			t.Fatalf("fragment %d corrupted protocol/input: %+v", split, events)
		}
	}
	var parser inputParser
	var events []keyEvent
	emit := func(event keyEvent) { events = append(events, event) }
	parser.feed([]byte("\x1b[200~"+frame+"\x1b[201~"), time.Now(), emit)
	if len(events) != 1 || events[0].Key != "<Paste>" || events[0].Paste != frame {
		t.Fatal("DCS-looking paste executed as preview protocol")
	}
	events = nil
	parser.feed([]byte("\x1bP"+strings.Repeat("x", 9000)), time.Now(), emit)
	parser.feed([]byte("discarded\x1b\\j"), time.Now(), emit)
	if len(events) != 1 || events[0].Key != "j" {
		t.Fatal("oversized DCS leaked into editor input")
	}
}
