package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"st-go/ptyutil"
)

func TestPTYTexIMAPAndDirtyToggleNeverSave(t *testing.T) {
	// A mistaken F5 compilation must fail, never launch a system typesetter.
	t.Setenv("PATH", t.TempDir())
	path := filepath.Join(t.TempDir(), "document.tex")
	initial := "\\documentclass{article}\namsart\n\\begin{document}\ncases\n%cursor\n\\end{document}\n"
	if err := os.WriteFile(path, []byte(initial), 0600); err != nil {
		t.Fatal(err)
	}
	p := startPTYEditor(t, "--preview-gap", "1m", path)
	p.await(t, "preview region query", func() bool { return strings.Contains(p.text(), "region query") })
	if err := ptyutil.SetWinSize(p.master, 24, 32); err != nil {
		t.Fatal(err)
	}
	p.send(t, "\x1bPregion layout main 1 1 32 24 secondary 33 1 48 24\x1b\\")
	p.await(t, "initial preview", func() bool { return len(previewPTYOpenCommands(p)) == 1 })
	p.send(t, "2G\x1b[15~\x1b")
	time.Sleep(100 * time.Millisecond)
	p.send(t, "4G\x1b[15~`a\ntail\x1b")
	time.Sleep(100 * time.Millisecond)
	before := len(p.text())
	p.send(t, ":toggle!\r")
	p.await(t, "unsaved preview bypassing automatic gap", func() bool { return len(previewPTYOpenCommands(p)) == 2 })
	p.await(t, "explicit dirty cursor position", func() bool { return strings.Contains(p.text()[before:], "region secondary position 600000") })
	if data, err := os.ReadFile(path); err != nil || string(data) != initial {
		t.Fatalf("F5/IMAP/toggle! wrote the file: %q %v", data, err)
	}
	before = len(p.text())
	p.send(t, ":toggle\r")
	p.await(t, "on-disk preview restored", func() bool { return len(previewPTYOpenCommands(p)) == 3 })
	p.await(t, "disk cursor clamped rather than marker", func() bool { return strings.Contains(p.text()[before:], "region secondary position 1000000") })
	// Unsaved expansions still use the normal crash-recovery journal.
	p.send(t, ":preserve\r")
	swap := filepath.Join(filepath.Dir(path), "."+filepath.Base(path)+".govim.swp")
	if info, err := os.Stat(swap); err != nil || info.Size() == 0 {
		t.Fatalf("unsaved edits weren't journaled: %v", err)
	}
	p.send(t, ":q!\r")
	select {
	case <-p.done:
	case <-time.After(5 * time.Second):
		t.Fatal("dirty preview session did not exit")
	}
	if p.err != nil {
		t.Fatalf("govim exit %v: %q", p.err, p.text())
	}
	if data, err := os.ReadFile(path); err != nil || string(data) != initial {
		t.Fatalf("toggle/discard changed disk contents: %q %v", data, err)
	}
}
