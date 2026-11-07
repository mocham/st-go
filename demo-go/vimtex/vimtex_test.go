package main

import (
	"bytes"
	"crypto/sha256"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestRefreshClock(t *testing.T) {
	clock := refreshClock{gap: 2100 * time.Millisecond}
	a, b := sha256.Sum256([]byte("first")), sha256.Sum256([]byte("second"))
	now := time.Now()
	if !clock.ready(now, a) {
		t.Fatal("first render should be immediate")
	}
	clock.start(now, a)
	if clock.ready(now.Add(time.Hour), b) {
		t.Fatal("started concurrent renders")
	}
	clock.busy = false
	clock.lastPublished = now.Add(time.Second)
	if clock.ready(now.Add(3*time.Second), b) || !clock.ready(now.Add(3100*time.Millisecond), b) {
		t.Fatal("refresh gap was not measured from actual publication")
	}
	if clock.ready(now.Add(time.Hour), a) {
		t.Fatal("unchanged content was rendered again")
	}
	clock.published, clock.hasPublished = b, true
	clock.hasAttempt = false
	if clock.ready(now.Add(time.Hour), b) {
		t.Fatal("reverting to the displayed revision caused a redundant render")
	}
}

func TestOutputBoundaries(t *testing.T) {
	for _, parts := range [][]string{
		{"\x1b[38;2;", "120;30;10m"},
		{"\x1b]2;title", "\a"},
		{"\x1bPopen 'x';", "\x1b", "\\"},
		{"\x1b", "(", "B"},
		{"\xe7", "\x95", "\x8c"},
		{"\x1b[", "\x18"},
	} {
		var boundary outputBoundary
		for i, part := range parts {
			boundary.observe([]byte(part))
			if got, want := boundary.safe(), i == len(parts)-1; got != want {
				t.Fatalf("parts %q at %d: safe=%v want=%v", parts, i, got, want)
			}
		}
	}
}

func writeSource(t *testing.T, path, data string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(data), 0600); err != nil {
		t.Fatal(err)
	}
}

func TestSourceIncludesAndAtomicSaves(t *testing.T) {
	dir := t.TempDir()
	root, include := filepath.Join(dir, "main.tex"), filepath.Join(dir, "part.tex")
	writeSource(t, root, "% \\input{ignored}\n\\input{part}\n\\input{generated}")
	writeSource(t, include, "old text")
	snapshot := loadSource(root)
	if snapshot.err != nil || !strings.Contains(snapshot.text, "old text") || snapshot.changed() {
		t.Fatalf("initial snapshot = %#v", snapshot)
	}
	writeSource(t, include+".new", "replacement text")
	if err := os.Rename(include+".new", include); err != nil {
		t.Fatal(err)
	}
	if !snapshot.changed() {
		t.Fatal("did not detect atomic replacement of an included file")
	}
	next := loadSource(root)
	if next.err != nil || next.hash == snapshot.hash || !strings.Contains(next.text, "replacement text") {
		t.Fatal("included source was not refreshed")
	}
	writeSource(t, filepath.Join(dir, "generated.tex"), "generated text")
	if !next.changed() {
		t.Fatal("did not detect creation of a previously missing include")
	}
	writeSource(t, include, `\input{main}`)
	if s := loadSource(root); s.err == nil {
		t.Fatal("recursive includes were accepted")
	}
}

func TestSourceSeekMapsRootLinesPastIncludes(t *testing.T) {
	dir := t.TempDir()
	root := filepath.Join(dir, "main.tex")
	writeSource(t, filepath.Join(dir, "part.tex"), "first\nsecond\nthird\n")
	writeSource(t, root, "before\n\\input{part}\nafter\n")
	s := loadSource(root)
	if s.err != nil || s.expandedLine(3) != 6 {
		t.Fatalf("root line mapping: %v, expanded=%d, text=%q", s.err, s.expandedLine(3), s.text)
	}
}

func TestNavigationInputPreservesKeysAndConsumesEvents(t *testing.T) {
	var input navigationInput
	var events []string
	callback := func(kind string, value int) { events = append(events, fmt.Sprintf("%s %d", kind, value)) }
	now := time.Now()
	var output []byte
	for _, chunk := range []string{"ihello", "\x1bPregion secondary eve", "nt scroll -1\x1b", "\\world", "\x1b[A"} {
		output = append(output, input.feed([]byte(chunk), now, callback)...)
	}
	if string(output) != "ihelloworld\x1b[A" || len(events) != 1 || events[0] != "scroll -1" {
		t.Fatalf("keys=%q events=%v", output, events)
	}
	input.feed([]byte("\x1b"), now, callback)
	if string(input.expire(now.Add(navigationWait))) != "\x1b" {
		t.Fatal("standalone Escape key was swallowed")
	}
	bad := navigationPrefix + "seek invalid\x1b\\"
	if got := input.feed([]byte(bad), now, callback); string(got) != bad {
		t.Fatal("malformed event swallowed user input")
	}
}

func TestMissingSourceAndGapValidation(t *testing.T) {
	if _, err := parseOptions([]string{filepath.Join(t.TempDir(), "missing.tex")}, io.Discard); err == nil {
		t.Fatal("accepted nonexistent source")
	}
	if _, err := parseOptions([]string{"-gap", "2s", "ignored.tex"}, io.Discard); err == nil || !strings.Contains(err.Error(), "greater than 2s") {
		t.Fatalf("gap=2s: %v", err)
	}
}

func TestSplitOptions(t *testing.T) {
	path := filepath.Join(t.TempDir(), "source.tex")
	writeSource(t, path, "Hello.")
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name  string
		args  []string
		width int // zero expects an invalid option error
	}{
		{"default", nil, 60},
		{"equal", []string{"-split", "1:1"}, 50},
		{"vim wider", []string{"-split", "3:2"}, 40},
		{"rounded", []string{"-split", "1:2"}, 67},
		{"large weights", []string{"-split", "4294967295:4294967295"}, 50},
		{"legacy width", []string{"-width", "45"}, 45},
		{"conflicting flags", []string{"-split", "4:6", "-width", "50"}, 0},
		{"missing colon", []string{"-split", "46"}, 0},
		{"extra colon", []string{"-split", "4:6:2"}, 0},
		{"empty weight", []string{"-split", "4:"}, 0},
		{"zero weight", []string{"-split", "0:6"}, 0},
		{"negative weight", []string{"-split", "4:-6"}, 0},
		{"overflow", []string{"-split", "18446744073709551616:6"}, 0},
		{"too narrow", []string{"-split", "1000:1"}, 0},
		{"too wide", []string{"-split", "1:1000"}, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			args := append([]string{"-vim", executable}, tc.args...)
			args = append(args, path, "-u", "NONE")
			o, err := parseOptions(args, io.Discard)
			if tc.width == 0 {
				if err == nil {
					t.Fatal("accepted invalid split options")
				}
				return
			}
			if err != nil || o.previewWidth != tc.width || strings.Join(o.vimArgs, " ") != "-u NONE" {
				t.Fatalf("preview width=%d, Vim args=%v, err=%v", o.previewWidth, o.vimArgs, err)
			}
		})
	}
}

func TestRenderAndPublishPDF(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "source.tex")
	writeSource(t, path, `\documentclass{article}\begin{document}\section{First}Hello $x^2$.\newpage\section{Second}World.\end{document}`)
	snapshot := loadSource(path)
	if snapshot.err != nil {
		t.Fatal(snapshot.err)
	}
	result := renderSource(path, snapshot)
	if result.err != nil || !bytes.HasPrefix(result.pdf, []byte("%PDF-")) {
		t.Fatalf("render PDF: %v", result.err)
	}
	preview := filepath.Join(dir, "preview.pdf")
	if err := publishPDF(preview, result.pdf); err != nil {
		t.Fatal(err)
	}
	if data, err := os.ReadFile(preview); err != nil || !bytes.Equal(data, result.pdf) {
		t.Fatal("published PDF differs from complete render")
	}
	if _, err := os.Stat(preview + ".next"); !os.IsNotExist(err) {
		t.Fatal("temporary PDF was not consumed by atomic publication")
	}
}
