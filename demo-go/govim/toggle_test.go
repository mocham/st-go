package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestToggleDiskAndDirtySnapshotWithoutSaving(t *testing.T) {
	initial := "# Heading\n%cursor\nSaved text\nAnother line\nLast line\n"
	e, out, _ := newPreviewTestEditor(t, "notes.md", initial)
	settlePreview(t, e)
	b := e.buffer()
	e.replace(b.start(2), b.end(2), "Dirty snapshot", 0)
	e.setPos(b.start(4))
	version, saved, diskHash, undo := b.version, b.savedVersion, b.meta.DiskHash, len(b.undo)
	out.Reset()
	e.execute("toggle")
	settlePreview(t, e)
	if !strings.Contains(e.preview.source.Source, "Saved text") || strings.Contains(e.preview.source.Source, "Dirty snapshot") {
		t.Fatal("plain toggle rendered unsaved content")
	}
	want := fmt.Sprintf("region secondary position %d", e.preview.source.Position(b.meta.Path, 5))
	if !strings.Contains(out.String(), want) {
		t.Fatalf("toggle didn't override %%cursor: %q", out.String())
	}
	if b.version != version || b.savedVersion != saved || b.meta.DiskHash != diskHash || len(b.undo) != undo {
		t.Fatal("navigation changed edit/save state")
	}
	// Explicit previews bypass the automatic render gap. Capture the buffer now,
	// not a later mutation while the background worker is processing it.
	e.preview.options.gap = time.Minute
	out.Reset()
	e.execute("toggle!")
	if e.preview.pending == nil || !e.preview.pending.immediate || e.preview.pending.overrides[b.meta.Path] != previewText(b) {
		t.Fatal("dirty toggle didn't capture an immutable immediate request")
	}
	e.replace(b.start(2), b.end(2), "Newer unsaved edit", 0)
	settlePreview(t, e)
	if !strings.Contains(e.preview.source.Source, "Dirty snapshot") || strings.Contains(e.preview.source.Source, "Newer unsaved edit") {
		t.Fatal("dirty preview raced live buffer contents")
	}
	if !strings.Contains(out.String(), "region secondary open ") || !strings.Contains(out.String(), want) {
		t.Fatalf("dirty toggle didn't draw and navigate: %q", out.String())
	}
	if data, err := os.ReadFile(b.meta.Path); err != nil || string(data) != initial {
		t.Fatalf("toggle! wrote the file: %q %v", data, err)
	}
	if b.savedVersion != saved || b.meta.DiskHash != diskHash || !b.dirty() {
		t.Fatal("toggle! marked unsaved content saved")
	}
	// A plain toggle after a transient dirty preview restores the disk version.
	out.Reset()
	e.execute("toggle")
	settlePreview(t, e)
	if e.preview.source.Source != initial || !strings.Contains(out.String(), "region secondary open ") {
		t.Fatal("plain toggle retained the transient dirty document")
	}
}

func TestToggleDirtyIncludesAndMainAreOverlaid(t *testing.T) {
	e, _, _ := newPreviewTestEditor(t, "main.tex", "\\begin{document}\nSaved parent\n\\input{part}\n\\end{document}\n")
	root := e.buffer()
	childPath := filepath.Join(filepath.Dir(root.meta.Path), "part.tex")
	childText := "%main=main\nSaved child\n"
	if err := os.WriteFile(childPath, []byte(childText), 0600); err != nil {
		t.Fatal(err)
	}
	child, err := loadBuffer(childPath)
	if err != nil {
		t.Fatal(err)
	}
	e.buffers = append(e.buffers, child)
	settlePreview(t, e)
	e.replace(root.start(1), root.end(1), "Dirty parent", 0)
	if _, err := child.change([]patch{{child.start(1), "Saved child", "Dirty child"}}, 0, 0, 0); err != nil {
		t.Fatal(err)
	}
	e.switchBuffer(child)
	e.setPos(child.start(1))
	e.execute("toggle!")
	settlePreview(t, e)
	if e.preview.source.Root != root.meta.Path || !strings.Contains(e.preview.source.Source, "Dirty parent") || !strings.Contains(e.preview.source.Source, "Dirty child") {
		t.Fatalf("unsaved %%main/includes lost: %q", e.preview.source.Source)
	}
	if data, err := os.ReadFile(childPath); err != nil || string(data) != childText {
		t.Fatal("dirty include preview saved the include")
	}
	if data, err := os.ReadFile(root.meta.Path); err != nil || strings.Contains(string(data), "Dirty parent") {
		t.Fatal("dirty include preview saved its root")
	}
}

func TestToggleUnwrittenFileAndCommandValidation(t *testing.T) {
	e, out, _ := newPreviewTestEditor(t, "notes.md", "# Initially saved\n")
	settlePreview(t, e)
	path := filepath.Join(t.TempDir(), "not-created.md")
	if err := e.bindPath(path); err != nil {
		t.Fatal(err)
	}
	e.replace(0, len(e.buffer().text), "# Unsaved new document", 0)
	out.Reset()
	e.execute("toggle!")
	settlePreview(t, e)
	if !strings.Contains(e.preview.source.Source, "Unsaved new document") || !strings.Contains(out.String(), "region secondary open ") {
		t.Fatal("couldn't preview a not-yet-written file")
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("toggle! created the source file")
	}
	for _, command := range []string{"toggle bad-argument", "%toggle"} {
		e.execute(command)
		if e.preview.pending != nil || !strings.Contains(e.message, "usage:") {
			t.Fatalf("accepted %q", command)
		}
	}
	plain := memoryEditor("text")
	defer plain.close(false)
	plain.execute("toggle!")
	if !strings.Contains(plain.message, "disabled") {
		t.Fatal("disabled preview command didn't report an error")
	}
}
