package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestExistingSwapOpensReadonlyAndPreservesRecovery(t *testing.T) {
	for _, location := range []string{"beside", "cache", "explicit"} {
		for _, active := range []bool{false, true} {
			name := location + map[bool]string{false: "/stale", true: "/active"}[active]
			t.Run(name, func(t *testing.T) {
				cache := t.TempDir()
				t.Setenv("XDG_CACHE_HOME", cache)
				ownerOptions, viewerOptions := defaultJournalOptions(), defaultJournalOptions()
				switch location {
				case "cache":
					ownerOptions.Directory = filepath.Join(cache, "govim", "swap")
				case "explicit":
					ownerOptions.Directory = t.TempDir()
					viewerOptions = ownerOptions
				}
				owner, path := diskEditor(t, "disk text\n", ownerOptions)
				editKeys(owner, "A unsaved<Esc>")
				journal := owner.buffer().journal
				if err := journal.Sync(); err != nil {
					t.Fatal(err)
				}
				if !active {
					if err := owner.close(false); err != nil {
						t.Fatal(err)
					}
				}
				before, err := os.ReadFile(journal.path)
				if err != nil {
					t.Fatal(err)
				}
				viewer := newEditor(viewerOptions)
				defer viewer.close(true)
				b, err := viewer.open(path, false)
				if err != nil {
					t.Fatal(err)
				}
				if !b.readonly || b.journal != nil || editorText(viewer) != "disk text\n" {
					t.Fatalf("readonly=%v journal=%v text=%q", b.readonly, b.journal, editorText(viewer))
				}
				if !strings.Contains(viewer.message, "readonly") || !strings.Contains(viewer.message, "govim -r") {
					t.Fatalf("missing recovery guidance: %q", viewer.message)
				}
				for _, keys := range []string{"x", "iBAD<Esc>", "dd", "u", "<C-r>"} {
					editKeys(viewer, keys)
					if editorText(viewer) != "disk text\n" || b.dirty() {
						t.Fatalf("readonly edit %q changed buffer", keys)
					}
				}
				viewer.execute("w")
				if !strings.Contains(viewer.message, "readonly") {
					t.Fatalf("ordinary write was not refused: %q", viewer.message)
				}
				if err := viewer.close(true); err != nil {
					t.Fatal(err)
				}
				after, err := os.ReadFile(journal.path)
				if err != nil || !bytes.Equal(before, after) {
					t.Fatalf("viewing changed the existing swap: %v", err)
				}
				if active {
					if j, err := openJournal(journal.path, false, ownerOptions); err == nil {
						j.Close(false)
						t.Fatal("viewing released the other editor's lock")
					}
					owner.close(false)
				}
				recovery := newEditor(viewerOptions)
				defer recovery.close(false)
				if _, err := recovery.open(path, true); err != nil {
					t.Fatal(err)
				}
				if editorText(recovery) != "disk text unsaved\n" {
					t.Fatalf("recovery lost edits: %q", editorText(recovery))
				}
			})
		}
	}
}

func TestSwapReadonlyFallbackOnlyOnOpenConflict(t *testing.T) {
	owner, path := diskEditor(t, "original\n", defaultJournalOptions())
	viewer := newEditor(defaultJournalOptions())
	defer viewer.close(true)
	b, err := viewer.open("", false)
	if err != nil {
		t.Fatal(err)
	}
	journal := b.journal
	if err := viewer.bindPath(path); err == nil || b.meta.Path != "" || b.journal != journal || b.readonly {
		t.Fatalf("rename must refuse an existing swap without changing the buffer: %v", err)
	}

	// Other journal failures remain errors, rather than allowing unjournaled edits.
	options := defaultJournalOptions()
	options.Directory = path // a regular file cannot hold a swap
	broken := newEditor(options)
	defer broken.close(false)
	if _, err := broken.open(path, false); err == nil || len(broken.buffers) != 0 {
		t.Fatal("non-conflict journal failure was ignored")
	}
	if owner.buffer().journal.closed {
		t.Fatal("another buffer's journal was closed")
	}
}

func TestPTYExistingSwapStartsReadonly(t *testing.T) {
	for _, backend := range []string{"fb", "web"} {
		for _, readonlyFlag := range []bool{false, true} {
			name := backend + map[bool]string{false: "/default", true: "/R"}[readonlyFlag]
			t.Run(name, func(t *testing.T) {
				t.Setenv("ST_GO_BACKEND", backend)
				owner, path := diskEditor(t, "original\n", defaultJournalOptions())
				journal := owner.buffer().journal
				if readonlyFlag {
					owner.close(false) // also exercise an abandoned journal
				}
				before, err := os.ReadFile(journal.path)
				if err != nil {
					t.Fatal(err)
				}
				args := []string{"--no-preview"}
				if readonlyFlag {
					args = append(args, "-R")
				}
				p := startPTYEditor(t, append(args, path)...)
				p.await(t, "readonly startup", func() bool {
					screen := p.screenText()
					return strings.Contains(screen, "Opened readonly") && strings.Contains(screen, "[RO]")
				})
				p.send(t, "x")
				p.await(t, "readonly edit rejection", func() bool { return strings.Contains(p.screenText(), "readonly buffer") })
				p.send(t, ":w\r")
				p.await(t, "readonly save rejection", func() bool { return strings.Contains(p.screenText(), "use :w!") })
				p.send(t, ":q\r")
				select {
				case <-p.done:
				case <-time.After(5 * time.Second):
					t.Fatal("readonly viewer did not quit")
				}
				if p.err != nil {
					t.Fatalf("viewer exited abnormally: %v output=%q", p.err, p.text())
				}
				after, err := os.ReadFile(journal.path)
				if err != nil || !bytes.Equal(before, after) {
					t.Fatalf("viewer changed swap: %v", err)
				}
				data, err := os.ReadFile(path)
				if err != nil || string(data) != "original\n" {
					t.Fatalf("viewer changed disk file: %q %v", data, err)
				}
			})
		}
	}
}
