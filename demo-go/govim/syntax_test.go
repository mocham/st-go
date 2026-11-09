package main

import (
	"errors"
	"io"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/alecthomas/chroma/v2"
	"github.com/alecthomas/chroma/v2/lexers"
)

func syntaxAt(tokens []syntaxToken, offset int) string {
	for _, token := range tokens {
		if offset >= token.start && offset < token.end {
			return token.style
		}
	}
	return ""
}

func highlightTestSource(t *testing.T, path, text string) syntaxResult {
	t.Helper()
	result := calculateSyntax(syntaxRequest{key: syntaxKey{path: path}, text: text}, nil)
	if result.err != nil {
		t.Fatalf("%s: %v", path, result.err)
	}
	end := 0
	for _, token := range result.tokens {
		if token.start < end || token.end <= token.start || token.end > utf8.RuneCountInString(text) {
			t.Fatalf("invalid syntax offsets: %+v", token)
		}
		end = token.end
	}
	return result
}

func TestChromaLanguages(t *testing.T) {
	for _, test := range []struct{ path, text string }{
		{"example.go", "package main\nfunc main() { println(42) }\n"},
		{"example.c", "/* comment */\nint main(void) { return 42; }\n"},
		{"example.cpp", "class Example { public: int value = 42; };\n"},
		{"example.py", "def example():\n    return 'hello'\n"},
		{"example.rs", "fn main() { let value = 42; }\n"},
		{"example.json", "{\"message\": \"hello\", \"count\": 42}"},
		{"example.yaml", "message: hello\ncount: 42\n"},
		{"example.toml", "[example]\ncount = 42\n"},
		{"example.sh", "#!/bin/sh\necho \"hello\""},
		{"example.tex", "\\section{Synthetic heading}\n% synthetic comment\n$x^2$\n"},
		{"example.md", "# Synthetic heading\n\n**bold** and `code`\n\n```go\nvar n = 42\n```\n"},
		{"example.html", "<p class=\"example\">hello</p>\n"},
		{"example.css", ".example { color: #aabbcc; }\n"},
		{"example.sql", "SELECT count(*) FROM example WHERE id = 42;\n"},
		{"Makefile", "all:\n\tprintf 'hello'\n"},
		{"script", "#!/usr/bin/env -S python3 -u\nprint('hello')\n"},
	} {
		t.Run(test.path, func(t *testing.T) {
			result := highlightTestSource(t, test.path, test.text)
			if result.lexer == "" || len(result.tokens) == 0 {
				t.Fatalf("no highlighting: lexer=%q tokens=%+v", result.lexer, result.tokens)
			}
		})
	}
	for _, name := range []string{"notes.unknown-govim", ""} {
		result := highlightTestSource(t, name, "ordinary words are not code")
		if len(result.tokens) != 0 {
			t.Fatalf("unknown prose highlighted: %+v", result.tokens)
		}
	}
}

func TestChromaMultilineAndUnicodeOffsets(t *testing.T) {
	text := "package main\n/* 世界\nfunc hidden() { return 42 }\n*/\nvar s = `α\nfunc literal() {}`\nfunc actual() {}"
	result := highlightTestSource(t, "example.go", text)
	for _, test := range []struct {
		needle string
		kind   chroma.TokenType
	}{
		{"func hidden", chroma.CommentMultiline},
		{"func literal", chroma.LiteralStringBacktick},
		{"func actual", chroma.Keyword},
	} {
		offset := utf8.RuneCountInString(text[:strings.Index(text, test.needle)])
		if got, want := syntaxAt(result.tokens, offset), syntaxForeground(test.kind); got != want {
			t.Fatalf("%q style=%q want=%q", test.needle, got, want)
		}
	}
	python := "text = \"\"\"first line\ndef not_a_function(): 世界\nlast line\"\"\"\ndef actual():\n    return 42\n"
	result = highlightTestSource(t, "example.py", python)
	offset := utf8.RuneCountInString(python[:strings.Index(python, "def not_a_function")])
	if got := syntaxAt(result.tokens, offset); got != syntaxForeground(chroma.LiteralString) {
		t.Fatalf("triple-quoted string lost multiline state: %q", got)
	}
	// Do not let lexer newline/tab normalisation shift document coordinates.
	mixed := "package main\r\nvar s = \"世界\"\r// α\nvar n = 42"
	result = highlightTestSource(t, "example.go", mixed)
	offset = utf8.RuneCountInString(mixed[:strings.LastIndex(mixed, "42")])
	if got := syntaxAt(result.tokens, offset); got != syntaxForeground(chroma.LiteralNumber) {
		t.Fatalf("mixed newline offsets shifted: %q", got)
	}
}

func awaitSyntax(t *testing.T, e *Editor, b *Buffer) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		e.pollSyntax()
		e.syntaxTokens(b)
		if b.syntax.ready && b.syntax.key == (syntaxKey{b.version, b.meta.Path}) {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("syntax worker did not finish: %+v", b.syntax)
}

func TestSyntaxCacheEditUndoAndRename(t *testing.T) {
	e := memoryEditor("/* open\npackage main\n*/\n")
	defer e.close(false)
	b := e.buffer()
	b.meta.Path = "example.go"
	e.syntaxTokens(b)
	awaitSyntax(t, e, b)
	at := strings.Index(string(b.text), "package")
	if got := syntaxAt(b.syntax.tokens, at); got != syntaxForeground(chroma.Comment) {
		t.Fatalf("comment style=%q", got)
	}
	if !e.replace(0, 2, "  ", 0) {
		t.Fatal("edit failed")
	}
	if len(e.syntaxTokens(b)) != 0 {
		t.Fatal("stale syntax survived edit")
	}
	awaitSyntax(t, e, b)
	if got := syntaxAt(b.syntax.tokens, at); got != syntaxForeground(chroma.Keyword) {
		t.Fatalf("edit did not reclassify following line: %q", got)
	}
	editKeys(e, "u")
	e.syntaxTokens(b)
	awaitSyntax(t, e, b)
	if got := syntaxAt(b.syntax.tokens, at); got != syntaxForeground(chroma.Comment) {
		t.Fatalf("undo did not invalidate syntax: %q", got)
	}
	b.meta.Path = "example.txt"
	if len(e.syntaxTokens(b)) != 0 {
		t.Fatal("stale syntax survived rename")
	}
	awaitSyntax(t, e, b)
	if len(b.syntax.tokens) != 0 {
		t.Fatal("plain text acquired Go highlighting")
	}
}

func TestSyntaxWorkerRejectsStaleSnapshot(t *testing.T) {
	e := memoryEditor("old")
	defer e.close(false)
	b := e.buffer()
	b.meta.Path = "example.go"
	started := make(chan syntaxRequest, 1)
	release := make(chan struct{})
	e.syntax = newSyntaxHighlighter(func(request syntaxRequest, stop <-chan struct{}) syntaxResult {
		started <- request
		select {
		case <-release:
		case <-stop:
		}
		return syntaxResult{syntaxRequest: request, tokens: []syntaxToken{{0, 1, "38;5;1"}}}
	})
	e.syntaxTokens(b)
	var request syntaxRequest
	select {
	case request = <-started:
	case <-time.After(time.Second):
		t.Fatal("worker did not start")
	}
	if !e.replace(0, 3, "new text", 0) {
		t.Fatal("editing blocked by syntax work")
	}
	if request.text != "old" {
		t.Fatal("worker did not receive immutable snapshot")
	}
	close(release)
	deadline := time.Now().Add(time.Second)
	for b.syntax.waiting && time.Now().Before(deadline) {
		e.pollSyntax()
		time.Sleep(time.Millisecond)
	}
	if b.syntax.waiting || b.syntax.ready || len(b.syntax.tokens) != 0 {
		t.Fatal("stale result was accepted")
	}
}

func TestSyntaxPreservesEditorOverlays(t *testing.T) {
	e := memoryEditor("package main\n")
	defer e.close(false)
	e.buffer().meta.Path = "example.go"
	awaitSyntax(t, e, e.buffer())
	var r renderer
	var out strings.Builder
	if err := e.draw(&r, &out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), syntaxCellStyle(syntaxForeground(chroma.Keyword), true)+"package") {
		t.Fatalf("syntax lost cursorline background: %q", out.String())
	}
	e.startSearch("package", false, false, 1)
	out.Reset()
	if err := e.draw(&r, &out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), activeSearchStyle+"package") {
		t.Fatal("syntax covered search highlight")
	}
	e.anchor = 0
	e.mode = visualMode
	e.setPos(6)
	out.Reset()
	if err := e.draw(&r, &out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), selectionStyle+"package") {
		t.Fatal("syntax covered visual selection")
	}
}

type syntaxTestLexer struct {
	chroma.Lexer
	tokenise func(*chroma.TokeniseOptions, string) (chroma.Iterator, error)
}

func (l syntaxTestLexer) Tokenise(options *chroma.TokeniseOptions, text string) (chroma.Iterator, error) {
	return l.tokenise(options, text)
}

func TestSyntaxFailureLimitsAndCancellation(t *testing.T) {
	base := lexers.Get("go")
	for _, test := range []struct {
		name     string
		tokenise func(*chroma.TokeniseOptions, string) (chroma.Iterator, error)
	}{
		{"error", func(*chroma.TokeniseOptions, string) (chroma.Iterator, error) {
			return nil, errors.New("synthetic lexer error")
		}},
		{"panic", func(*chroma.TokeniseOptions, string) (chroma.Iterator, error) {
			return func() chroma.Token { panic("synthetic panic") }, nil
		}},
		{"changed text", func(*chroma.TokeniseOptions, string) (chroma.Iterator, error) {
			return func() chroma.Token { return chroma.Token{Type: chroma.Keyword, Value: "other"} }, nil
		}},
		{"omitted text", func(*chroma.TokeniseOptions, string) (chroma.Iterator, error) {
			return func() chroma.Token { return chroma.EOF }, nil
		}},
		{"too many tokens", func(*chroma.TokeniseOptions, string) (chroma.Iterator, error) {
			return func() chroma.Token { return chroma.Token{Type: chroma.Text, Value: ""} }, nil
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			tokens, err := lexSyntax(syntaxTestLexer{base, test.tokenise}, "package main", nil, time.Now().Add(5*time.Second))
			if err == nil || len(tokens) != 0 {
				t.Fatalf("lexer did not fail closed: %+v %v", tokens, err)
			}
		})
	}
	stop := make(chan struct{})
	close(stop)
	if tokens, err := lexSyntax(base, "package main", stop, time.Now().Add(time.Second)); err == nil || len(tokens) != 0 {
		t.Fatal("cancelled syntax ran")
	}
	if _, err := lexSyntax(base, "package main", nil, time.Now().Add(-time.Second)); err == nil {
		t.Fatal("expired budget ignored")
	}
	e := memoryEditor(strings.Repeat("x", maxSyntaxBytes+1))
	defer e.close(false)
	e.buffer().meta.Path = "large.go"
	if tokens := e.syntaxTokens(e.buffer()); len(tokens) != 0 || e.syntax != nil {
		t.Fatal("oversized syntax snapshot queued")
	}
	if err := e.draw(&renderer{}, io.Discard); err != nil {
		t.Fatal(err)
	}
}
