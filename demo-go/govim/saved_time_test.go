package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestCommandRowShowsSavedTimestampNotRepeatedPath(t *testing.T) {
	path := filepath.Join(t.TempDir(), "notes.txt")
	if err := os.WriteFile(path, []byte("Text\n"), 0600); err != nil {
		t.Fatal(err)
	}
	mtime := time.Date(2026, 11, 9, 10, 11, 12, 0, time.Local)
	if err := os.Chtimes(path, mtime, mtime); err != nil {
		t.Fatal(err)
	}
	e := newEditor(defaultJournalOptions())
	defer e.close(false)
	b, err := e.open(path, false)
	if err != nil {
		t.Fatal(err)
	}
	if !b.savedAt.Equal(mtime) {
		t.Fatalf("loaded saved time = %v", b.savedAt)
	}
	e.cols = len(path) + 30
	var r renderer
	if err := e.draw(&r, &strings.Builder{}); err != nil {
		t.Fatal(err)
	}
	rowText := func(y int) string {
		var text strings.Builder
		for _, cell := range r.previous[y] {
			text.WriteString(cell.text)
		}
		return text.String()
	}
	if !strings.Contains(rowText(e.rows-2), path) || strings.Contains(rowText(e.rows-1), path) || !strings.Contains(rowText(e.rows-1), "Last saved 2026-11-09 10:11:12") {
		t.Fatalf("status=%q command=%q", rowText(e.rows-2), rowText(e.rows-1))
	}
	e.replace(0, 0, "Edited ", 0)
	if !b.savedAt.Equal(mtime) {
		t.Fatal("unsaved edit changed saved time")
	}
	start := time.Now()
	e.execute("w")
	if b.savedAt.Before(start) || b.savedAt.After(time.Now()) || e.message != "" {
		t.Fatalf("successful save not timestamped: %v message=%q", b.savedAt, e.message)
	}
	saved := b.savedAt
	b.readonly = true
	e.execute("w")
	if !b.savedAt.Equal(saved) || !strings.Contains(e.message, "readonly") {
		t.Fatal("failed save advanced timestamp or hid error")
	}
}

func TestUnsavedBufferTimestampAndPromptPriority(t *testing.T) {
	e := memoryEditor("Text\n")
	defer e.close(false)
	if savedTimeLabel(e.buffer()) != "Not saved yet" {
		t.Fatal("unnamed buffer pretends to be saved")
	}
	e.buffer().savedAt = time.Date(2026, 11, 9, 10, 11, 12, 0, time.Local)
	e.beginPrompt(":")
	var r renderer
	if err := e.draw(&r, &strings.Builder{}); err != nil {
		t.Fatal(err)
	}
	if r.previous[e.rows-1][0].text != ":" {
		t.Fatal("timestamp obscured command prompt")
	}
}
