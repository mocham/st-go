package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func texEditor(t *testing.T, text string) *Editor {
	t.Helper()
	e := memoryEditor(text)
	e.buffer().meta.Path = "/synthetic/document.tex"
	t.Cleanup(func() { e.close(false) })
	return e
}

func TestEmbeddedIMAPGreekAndProtection(t *testing.T) {
	for _, test := range []struct{ input, want string }{
		{"`a", `\alpha`}, {"`b", `\beta`}, {"`e", `\varepsilon`}, {"`f", `\varphi`}, {"`w", `\omega`},
		{"`D", `\Delta`}, {"`P", `\Pi`}, {"`W", `\Omega`}, {"``a", "``a"}, {"\\`a", "\\`a"}, {"\"`a", "\"`a"}, {"`j", "`j"},
	} {
		e := texEditor(t, "")
		editKeys(e, "i"+test.input+"<Esc>")
		if got := string(e.buffer().text); got != test.want {
			t.Errorf("%q -> %q want %q", test.input, got, test.want)
		}
	}
	e := texEditor(t, "")
	editKeys(e, "3i`a<Esc>")
	if got := string(e.buffer().text); got != strings.Repeat(`\alpha`, 3) {
		t.Fatalf("counted insert %q", got)
	}
	editKeys(e, "u")
	if len(e.buffer().text) != 0 {
		t.Fatal("IMAP didn't share insert undo group")
	}
	editKeys(e, "<C-r>")
	if got := string(e.buffer().text); got != strings.Repeat(`\alpha`, 3) {
		t.Fatal("IMAP redo lost expansion")
	}
}

func TestIMAPBypassesPasteRegistersAndOtherFiletypes(t *testing.T) {
	for _, path := range []string{"notes.md", "notes.go", ""} {
		e := texEditor(t, "")
		e.buffer().meta.Path = path
		editKeys(e, "i`a<Esc>")
		if string(e.buffer().text) != "`a" {
			t.Fatalf("expanded in %q", path)
		}
	}
	e := texEditor(t, "")
	e.Feed(keyEvent{Key: "<Paste>", Paste: "`a"})
	if string(e.buffer().text) != "`a" {
		t.Fatal("paste was interpreted as IMAP keys")
	}
	e = texEditor(t, "")
	e.registers['a'] = register{Text: "`a"}
	editKeys(e, "i<C-r>a<Esc>")
	if string(e.buffer().text) != "`a" {
		t.Fatal("register text was interpreted as IMAP keys")
	}
}

func TestF5EnvironmentCursorAndCtrlJ(t *testing.T) {
	e := texEditor(t, "cases\n")
	e.registers['a'] = register{Text: "sentinel"}
	editKeys(e, "<F5>")
	want := "\\begin{cases}\n\n\\end{cases}<++>"
	if string(e.buffer().text) != want || e.mode != insertMode || e.pos() != len("\\begin{cases}\n") {
		t.Fatalf("environment=%q mode=%d cursor=%d", string(e.buffer().text), e.mode, e.pos())
	}
	editKeys(e, "x<C-j>y<Esc>")
	if got := string(e.buffer().text); got != "\\begin{cases}\nx\n\\end{cases}y" {
		t.Fatalf("placeholder insertion %q", got)
	}
	if e.registers['a'].Text != "sentinel" {
		t.Fatal("snippet overwrote a register")
	}
	editKeys(e, "u")
	if string(e.buffer().text) != "cases" {
		t.Fatal("snippet/placeholder undo did not restore trigger")
	}
	e = texEditor(t, "  align*\n")
	editKeys(e, "A<F5>")
	if string(e.buffer().text) != "  \\begin{align*}\n  \n  \\end{align*}<++>" || e.pos() != len("  \\begin{align*}\n  ") {
		t.Fatal("indented template lost cursor placement")
	}
}

func TestF5PreamblePackageAndContext(t *testing.T) {
	for _, test := range []struct {
		prefix string
		pkg    bool
	}{
		{"\\documentclass{article}\n", true}, {"% \\documentclass{article}\n", false},
		{"\\\\documentclass{article}\n", false}, {"\\documentclass{article}\n\\begin {document}\n", false},
	} {
		e := texEditor(t, test.prefix+"amsart\n")
		row := strings.Count(test.prefix, "\n")
		e.setPos(e.buffer().start(row))
		editKeys(e, "<F5>")
		if test.pkg {
			if got := string(e.buffer().line(row)); got != `\usepackage{amsart}` || e.pos() != e.buffer().end(row) {
				t.Fatalf("package %q cursor=%d", got, e.pos())
			}
		} else if !strings.Contains(string(e.buffer().text), `\begin{amsart}`) {
			t.Fatal("non-preamble treated as a package")
		}
	}
	e := texEditor(t, "not a standalone name\n")
	before := e.buffer().version
	editKeys(e, "<F5>")
	if e.mode != normalMode || e.buffer().version != before {
		t.Fatal("invalid trigger was changed")
	}
	e = texEditor(t, "cases\n")
	e.buffer().readonly = true
	editKeys(e, "<F5>")
	if e.mode != normalMode || string(e.buffer().text) != "cases" {
		t.Fatal("readonly snippet changed content/mode")
	}
	called := false
	e = texEditor(t, "\\begin{document}\n")
	e.compile = func(string, bool) error { called = true; return nil }
	editKeys(e, "<C-F5>")
	if !called {
		t.Fatal("Ctrl-F5 compilation was lost")
	}
}

func TestCtrlJParserIsDistinctFromEnterAndWraps(t *testing.T) {
	e := texEditor(t, "<++> tail <++> suffix")
	e.setPos(len(e.buffer().text) - 1)
	var parser inputParser
	parser.feed([]byte("\niX\r"), time.Now(), e.Feed)
	// Ctrl-J in Normal wraps to the first stop and enters Insert; i is then
	// literal inserted text, while CR is an actual newline (not a stop jump).
	if got := string(e.buffer().text); !strings.HasPrefix(got, "iX\n tail ") {
		t.Fatalf("Ctrl-J/Enter parser %q", got)
	}
	e = texEditor(t, "")
	editKeys(e, "i<C-j>")
	if len(e.buffer().text) != 0 || e.mode != insertMode {
		t.Fatal("missing placeholder inserted a newline")
	}
	editKeys(e, "<Enter>")
	if string(e.buffer().text) != "\n" {
		t.Fatal("Enter no longer inserts newline")
	}
	e = texEditor(t, "")
	editKeys(e, "i<C-v><C-j>")
	if string(e.buffer().text) != "\n" {
		t.Fatal("quoted Ctrl-J wasn't literal")
	}
}

func TestTexSnippetJournalRecovery(t *testing.T) {
	path := filepath.Join(t.TempDir(), "document.tex")
	if err := os.WriteFile(path, []byte("cases\n"), 0600); err != nil {
		t.Fatal(err)
	}
	options := defaultJournalOptions()
	e := newEditor(options)
	if _, err := e.open(path, false); err != nil {
		t.Fatal(err)
	}
	defer e.close(false)
	editKeys(e, "A<F5>`a<C-j>tail<Esc>")
	want := editorText(e)
	if err := e.buffer().journal.Sync(); err != nil {
		t.Fatal(err)
	}
	if err := e.buffer().journal.Close(false); err != nil {
		t.Fatal(err)
	}
	recovered := newEditor(options)
	defer recovered.close(false)
	if _, err := recovered.open(path, true); err != nil {
		t.Fatal(err)
	}
	if editorText(recovered) != want {
		t.Fatal("snippet/stop edits weren't recovered")
	}
	editKeys(recovered, "u")
	if editorText(recovered) != "cases\n" {
		t.Fatal("recovery lost snippet undo group")
	}
	if data, err := os.ReadFile(path); err != nil || string(data) != "cases\n" {
		t.Fatal("snippets wrote the original file without a save")
	}
}
