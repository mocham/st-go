package main

import (
	"strings"
	"testing"
)

func memoryEditor(text string) *Editor {
	e := newEditor(defaultJournalOptions())
	final := strings.HasSuffix(text, "\n")
	text = strings.TrimSuffix(text, "\n")
	b := &Buffer{text: []rune(text), byteSize: len(text), meta: fileMetadata{Encoding: "utf-8", FinalEOL: final, Mode: 0600}}
	w := &Window{buffer: b, goal: -1, lineOriginalRow: -1, rect: rectangle{0, 0, 80, 23}}
	e.buffers = []*Buffer{b}
	e.tabs = []*tab{{root: &pane{window: w}, active: w}}
	return e
}
func editKeys(e *Editor, keys string) {
	for _, key := range parseKeyNotation(keys) {
		e.Feed(key)
	}
}
func editorText(e *Editor) string {
	data, err := e.buffer().encoded()
	if err != nil {
		panic(err)
	}
	return string(data)
}

func TestEditingCommands(t *testing.T) {
	for _, test := range []struct{ name, initial, keys, want string }{
		{"insert", "abc\n", "iXY<Esc>", "XYabc\n"},
		{"append", "abc\n", "A!<Esc>", "abc!\n"},
		{"counted insert", "a\n", "3ix<Esc>", "xxxa\n"},
		{"delete word", "one two three\n", "dw", "two three\n"},
		{"change word", "one two\n", "cwX<Esc>", "X two\n"},
		{"delete two words", "one two three\n", "d2w", "three\n"},
		{"multiplied counts", "a b c d e f g\n", "2d3w", "g\n"},
		{"delete line", "one\ntwo\nthree\n", "jdd", "one\nthree\n"},
		{"delete last line", "one\ntwo\n", "Gdd", "one\n"},
		{"change line", "one\ntwo\nthree\n", "jccX<Esc>", "one\nX\nthree\n"},
		{"change last lines", "one\ntwo\nthree\n", "j2ccX<Esc>", "one\nX\n"},
		{"yank put", "one\ntwo\n", "yyp", "one\none\ntwo\n"},
		{"delete put", "one\ntwo\n", "ddp", "two\none\n"},
		{"character put", "abcd\n", "ylp", "aabcd\n"},
		{"delete backwards", "abcd\n", "$2X", "ad\n"},
		{"undo insert", "abc\n", "Axyz<Esc>u", "abc\n"},
		{"redo insert", "abc\n", "Axyz<Esc>u<C-r>", "abcxyz\n"},
		{"undo change group", "one two\n", "cwhello<Esc>u", "one two\n"},
		{"dot insert", "a\nb\n", "Ax<Esc>j.", "ax\nbx\n"},
		{"dot change", "one two three\n", "cwX<Esc>w.", "X X three\n"},
		{"replace one", "abcd\n", "l2rX", "aXXd\n"},
		{"replace mode backspace", "abcdef\n", "Rxy<BS>z<Esc>", "xzcdef\n"},
		{"indent count", "a\nb\nc\n", "2>>", "    a\n    b\nc\n"},
		{"unindent", "    a\n", "<<", "a\n"},
		{"toggle case", "aBcD\n", "4~", "AbCd\n"},
		{"uppercase word", "one two\n", "gUw", "ONE two\n"},
		{"lowercase lines", "ONE\nTWO\n", "guG", "one\ntwo\n"},
		{"inner word", "hello world\n", "lldiw", " world\n"},
		{"around word", "hello world\n", "daw", "world\n"},
		{"inner paren", "a(b c)d\n", "ldi(", "a()d\n"},
		{"around bracket", "a[b c]d\n", "lda[", "ad\n"},
		{"nested objects", "a(b(c)d)e\n", "4l2di(", "a()e\n"},
		{"quoted object", "a \"hi there\" b\n", "4ldi\"", "a \"\" b\n"},
		{"join", "one\n   two\n", "J", "one two\n"},
		{"raw join", "one\n   two\n", "gJ", "one   two\n"},
		{"visual chars", "abcdef\n", "lv2ld", "aef\n"},
		{"visual lines", "a\nb\nc\n", "Vjd", "c\n"},
		{"visual block delete", "abcd\nefgh\n", "l<C-v>jld", "ad\neh\n"},
		{"visual block insert", "abcd\nefgh\n", "l<C-v>jIX<Esc>", "aXbcd\neXfgh\n"},
		{"visual block append", "abcd\nefgh\n", "l<C-v>jAX<Esc>", "abXcd\nefXgh\n"},
		{"visual fill", "abc\ndef\n", "vljrX", "XXX\nXXf\n"},
		{"register", "one\ntwo\n", "\"ayyj\"ap", "one\ntwo\none\n"},
		{"blackhole", "a\nb\nc\n", "yyj\"_ddp", "a\nc\na\n"},
		{"macro", "a\nb\nc\n", "qaAx<Esc>jq2@a", "ax\nbx\ncx\n"},
		{"insert backspace", "abc\ndef\n", "j0i<BS><Esc>", "abcdef\n"},
		{"insert tab", "x\n", "i<Tab>y<Esc>", "    yx\n"},
		{"auto indent", "    a\n", "A<Enter>b<Esc>", "    a\n    b\n"},
		{"smart indent", "{\n", "A<Enter>x<Esc>", "{\n    x\n"},
		{"open below", "a\nb\n", "oX<Esc>", "a\nX\nb\n"},
		{"open above", "a\nb\n", "OX<Esc>", "X\na\nb\n"},
		{"unicode edit", "α世界 café\n", "lrx", "αx界 café\n"},
		{"increment", "n=009\n", "<C-a>", "n=010\n"},
		{"decrement", "n=10\n", "3<C-x>", "n=7\n"},
		{"undo branch", "a\n", "ix<Esc>uiy<Esc>u<C-r>", "ya\n"},
	} {
		t.Run(test.name, func(t *testing.T) {
			e := memoryEditor(test.initial)
			editKeys(e, test.keys)
			if got := editorText(e); got != test.want {
				t.Fatalf("keys=%s\ntext=%q want=%q mode=%d pos=%d message=%q", test.keys, got, test.want, e.mode, e.pos(), e.message)
			}
			if e.pos() < 0 || e.pos() > len(e.buffer().text) {
				t.Fatal("invalid cursor")
			}
		})
	}
}

func TestExAndSearchCommands(t *testing.T) {
	for _, test := range []struct{ command, want string }{
		{"%s/one/ONE/g", "ONE two ONE\nthree ONE\n"},
		{"1s/one/X/", "X two one\nthree one\n"},
		{"%s/\\(one\\)/[\\1]/g", "[one] two [one]\nthree [one]\n"},
		{"%s/one/&!/g", "one! two one!\nthree one!\n"},
		{"1,2delete", ""},
		{"1copy $", "one two one\nthree one\none two one\n"},
		{"1move $", "three one\none two one\n"},
		{"g/three/d", "one two one\n"},
		{"v/three/d", "three one\n"},
	} {
		t.Run(test.command, func(t *testing.T) {
			e := memoryEditor("one two one\nthree one\n")
			e.execute(test.command)
			if got := editorText(e); got != test.want {
				t.Fatalf("%s: %q want %q message=%q", test.command, got, test.want, e.message)
			}
		})
	}
	e := memoryEditor("one two one\nthree one\n")
	editKeys(e, "/one<Enter>")
	if e.pos() != 8 {
		t.Fatalf("search=%d", e.pos())
	}
	editKeys(e, "n")
	if e.buffer().row(e.pos()) != 1 {
		t.Fatal("next search")
	}
	editKeys(e, "N")
	if e.pos() != 8 {
		t.Fatal("reverse search")
	}
	e.execute("noh")
	if e.search.highlight {
		t.Fatal("nohlsearch")
	}
	e.execute("%s/one/X/gc")
	for _, key := range []string{"y", "n", "a"} {
		e.Feed(keyEvent{Key: key})
	}
	if editorText(e) != "X two one\nthree X\n" {
		t.Fatalf("confirm=%q message=%q", editorText(e), e.message)
	}
	editKeys(e, "u")
	if editorText(e) != "one two one\nthree one\n" {
		t.Fatal("substitution confirmation undo group")
	}
}

func TestMotionPositions(t *testing.T) {
	for _, test := range []struct {
		keys     string
		row, col int
	}{
		{"w", 0, 4}, {"e", 0, 2}, {"2w", 0, 8}, {"$b", 0, 8}, {"j", 1, 0}, {"$j", 1, 2}, {"G", 2, 2}, {"gg", 0, 0}, {"2G", 1, 0}, {"2j^", 2, 2}, {"fo", 0, 6}, {"2fo", 0, 0}, {"ft", 0, 4}, {"tt", 0, 3},
	} {
		t.Run(test.keys, func(t *testing.T) {
			e := memoryEditor("one two three\nabc\n  end\n")
			editKeys(e, test.keys)
			row := e.buffer().row(e.pos())
			col := e.pos() - e.buffer().start(row)
			if row != test.row || col != test.col {
				t.Fatalf("position=%d,%d want%d,%d message%s", row, col, test.row, test.col, e.message)
			}
		})
	}
}

func TestCountsBoundsAndReadOnly(t *testing.T) {
	e := memoryEditor("abc\n")
	editKeys(e, strings.Repeat("9", 30)+"x")
	if e.pos() < 0 || e.pos() > len(e.buffer().text) {
		t.Fatal("count overflow")
	}
	e = memoryEditor("abc\n")
	e.buffer().readonly = true
	editKeys(e, "dd")
	if editorText(e) != "abc\n" {
		t.Fatal("readonly edit")
	}
	e = memoryEditor("abc\n")
	e.Feed(keyEvent{Key: "<Paste>", Paste: "x\x1b:q!\ny"})
	if e.quit || !strings.Contains(editorText(e), ":q!") {
		t.Fatal("paste was interpreted as commands")
	}
	e = memoryEditor("abc\n")
	editKeys(e, "qa@aq@a")
	if e.macroDepth != 0 {
		t.Fatal("macro recursion leaked")
	}
}
