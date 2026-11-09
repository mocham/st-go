package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

func TestPromptReadlineEditing(t *testing.T) {
	for _, prefix := range []string{":", "/", "?"} {
		for _, test := range []struct {
			name, initial, keys, want string
			cursor                    int
		}{
			{"insert middle", "ab", "<Left>X", "aXb", 3},
			{"arrows", "abc", "<Left><Left><Right>X", "abXc", 4},
			{"control begin", "abc", "<C-a>X", "Xabc", 2},
			{"control end", "abc", "<C-a><C-e>X", "abcX", 5},
			{"home end", "abc", "<Home>X<End>Y", "XabcY", 6},
			{"control left right", "abc", "<C-b><C-b><C-f>X", "abXc", 4},
			{"backspace middle", "abc", "<Left><BS>", "ac", 2},
			{"delete middle", "abc", "<Home><Right><Delete>", "ac", 2},
			{"control delete", "abc", "<C-a><C-f><C-d>", "ac", 2},
			{"boundaries", "abc", "<Home><Left><BS>", "abc", 1},
			{"delete end", "abc", "<End><Delete><C-d>", "abc", 4},
			{"kill to start", "one two", "<Left><Left><Left><C-u>", "two", 1},
			{"kill to end", "one two", "<Home><Right><Right><Right><C-k>", "one", 4},
			{"word rubout", "one two   ", "<C-w>", "one ", 5},
			{"kill yank", "one two", "<C-w><C-y>", "one two", 8},
			{"word motion", "one two", "<C-Left>X", "one Xtwo", 6},
			{"word forward", "one two", "<Home><C-Right>X", "oneX two", 5},
			{"meta words", "one two", "<A-b>X<A-f>Y", "one XtwoY", 10},
			{"meta delete", "one two", "<Home><A-d>", " two", 1},
			{"unicode insert", "α世界", "<Left>X", "α世X界", 4},
			{"unicode backspace", "α世界", "<Left><BS>", "α界", 2},
			{"unicode delete", "α世界", "<Home><Right><Delete>", "α界", 2},
		} {
			t.Run(prefix+test.name, func(t *testing.T) {
				e := memoryEditor("document\n")
				e.beginPrompt(prefix + test.initial)
				editKeys(e, test.keys)
				if e.prompt != prefix+test.want || e.promptCursor != test.cursor {
					t.Fatalf("prompt=%q cursor=%d want=%q cursor=%d", e.prompt, e.promptCursor, prefix+test.want, test.cursor)
				}
				if !utf8.ValidString(e.prompt) || editorText(e) != "document\n" {
					t.Fatal("prompt editing changed document/UTF-8")
				}
			})
		}
	}
}

func TestPromptHistoryRetainsDraftAndCursor(t *testing.T) {
	e := memoryEditor("document\n")
	e.promptHistory = []string{":first", "/alpha", ":second", "?backward", "/beta"}
	e.beginPrompt(":draft")
	e.promptKey("<Left>")
	draftCursor := e.promptCursor
	editKeys(e, "<Up>")
	if e.prompt != ":second" || e.promptCursor != len(":second") {
		t.Fatal("history prefix/end cursor")
	}
	editKeys(e, "<C-p><C-p>")
	if e.prompt != ":first" {
		t.Fatal("history traversed before its beginning")
	}
	editKeys(e, "<C-n><Down>")
	if e.prompt != ":draft" || e.promptCursor != draftCursor {
		t.Fatal("history did not restore draft/cursor")
	}
	editKeys(e, "<Down>")
	if e.prompt != ":draft" {
		t.Fatal("down at draft cleared input")
	}
	e.beginPrompt("/query")
	editKeys(e, "<Up>")
	if e.prompt != "/beta" {
		t.Fatal("search history selected Ex/backward entry")
	}
	editKeys(e, "<Up>")
	if e.prompt != "/alpha" {
		t.Fatal("search history order")
	}
}

func TestPromptPasteAtCursorAndBounds(t *testing.T) {
	e := memoryEditor("document\n")
	e.beginPrompt(":ab")
	e.promptKey("<Left>")
	e.Feed(keyEvent{Key: "<Paste>", Paste: "世界\r\nx\ry\nz\t!"})
	if e.prompt != ":a世界 x y z !b" || e.promptCursor != utf8.RuneCountInString(":a世界 x y z !") {
		t.Fatalf("paste cursor/newline handling: %q %d", e.prompt, e.promptCursor)
	}
	before, cursor := e.prompt, e.promptCursor
	e.insertPrompt(strings.Repeat("X", maxPromptBytes))
	if e.prompt != before || e.promptCursor != cursor {
		t.Fatal("oversized prompt was partially inserted")
	}
	e.insertPrompt(string([]byte{0xff}))
	if e.prompt != before {
		t.Fatal("invalid UTF-8 prompt accepted")
	}
	for _, key := range []string{"<Esc>", "\x03", "<BS>"} {
		e.beginPrompt(":")
		e.Feed(keyEvent{Key: key})
		if e.prompt != "" {
			t.Fatalf("empty prompt cancellation failed: %q", key)
		}
	}
}

func TestPromptSubmitEditedExAndSearch(t *testing.T) {
	e := memoryEditor("b\na\n")
	editKeys(e, ":%srot<C-a><C-f><C-f><C-d><C-f>r<Enter>")
	if editorText(e) != "a\nb\n" {
		t.Fatalf("edited Ex did not execute: %q message=%s", editorText(e), e.message)
	}
	e = memoryEditor("hello world\n")
	editKeys(e, "/wrld<C-a><Right>o<Enter>")
	if e.search.pattern != "world" || e.pos() != 6 {
		t.Fatalf("edited search did not execute: pattern=%q pos=%d", e.search.pattern, e.pos())
	}
	e = memoryEditor("one two three\n")
	editKeys(e, "d/thre<C-a><C-e>e<Enter>")
	if editorText(e) != "three\n" {
		t.Fatalf("prompt cursor broke operator search: %q", editorText(e))
	}
}

func TestPromptHorizontalScrollAndRenderedCursor(t *testing.T) {
	e := memoryEditor("document\n")
	defer e.close(false)
	e.rows, e.cols = 4, 14
	e.beginPrompt("/" + strings.Repeat("α世界", 12))
	line, column := e.promptLine(e.cols - 1)
	if e.promptView <= 1 || column < 1 || column >= e.cols-1 || !strings.HasPrefix(line, "/") {
		t.Fatalf("prompt end offscreen: %q %d view=%d", line, column, e.promptView)
	}
	editKeys(e, "<C-a>")
	line, column = e.promptLine(e.cols - 1)
	if e.promptView != 1 || column != 1 || line != e.prompt {
		t.Fatal("prompt beginning remained scrolled")
	}
	editKeys(e, "<C-f><C-f>")
	_, column = e.promptLine(e.cols - 1)
	if column != 5 {
		t.Fatalf("Unicode cursor cells=%d want5", column)
	}
	var out strings.Builder
	if err := e.draw(&renderer{}, &out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), fmt.Sprintf("\x1b[%d;%dH", e.rows, column+1)) {
		t.Fatalf("rendered cursor not at insertion point: %q", out.String())
	}
	editKeys(e, "<C-e>")
	_, column = e.promptLine(2)
	if column < 0 || column >= 2 {
		t.Fatal("tiny prompt cursor outside viewport")
	}
}

func TestPromptInputReadlineKeysFragmented(t *testing.T) {
	input := "\x1b[D\x1b[C\x01\x05\x1b[1;5D\x1b[1;5C\x1bb\x1bf\x1bd\x1b\x7f"
	want := []string{"<Left>", "<Right>", "\x01", "\x05", "<C-Left>", "<C-Right>", "<A-b>", "<A-f>", "<A-d>", "<A-BS>"}
	for size := 1; size < 8; size++ {
		var parser inputParser
		var got []string
		for at := 0; at < len(input); at += size {
			parser.feed([]byte(input[at:min(len(input), at+size)]), time.Now(), func(event keyEvent) { got = append(got, event.Key) })
		}
		if strings.Join(got, ",") != strings.Join(want, ",") {
			t.Fatalf("chunk%d keys=%q want%q", size, got, want)
		}
	}
	// Do not reinterpret a quick insert Escape + b as a global Meta binding.
	e := memoryEditor("one two\n")
	editKeys(e, "A!")
	e.Feed(keyEvent{Key: "<A-b>"})
	if e.mode != normalMode || e.pos() != 4 {
		t.Fatalf("Escape+b stopped working outside prompt: mode=%d pos=%d", e.mode, e.pos())
	}
}

func TestPTYReadlineSearchAndEx(t *testing.T) {
	path := filepath.Join(t.TempDir(), "document.txt")
	if err := os.WriteFile(path, []byte("hello world\n"), 0600); err != nil {
		t.Fatal(err)
	}
	p := startPTYEditor(t, path)
	p.send(t, "/wrld\x01\x1b[Co\r")
	p.await(t, "edited search", func() bool { return strings.Contains(p.text(), "match 1/1") })
	p.send(t, ":%s/world/earthX/\x05\x1b[D\x7f\r")
	p.await(t, "edited substitution", func() bool { return strings.Contains(p.text(), "1 substitutions") })
	p.send(t, ":wq\r")
	select {
	case <-p.done:
	case <-time.After(5 * time.Second):
		t.Fatal("readline PTY did not exit")
	}
	data, err := os.ReadFile(path)
	if err != nil || string(data) != "hello earth\n" {
		t.Fatalf("edited prompt save=%q err=%v output=%q", data, err, p.text())
	}
}
