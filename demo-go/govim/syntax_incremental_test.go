package main

import (
	"math/rand"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/alecthomas/chroma/v2"
)

func calculateTestDemand(t *testing.T, calculate func(syntaxRequest, <-chan struct{}) syntaxResult, b *Buffer, first, last int) syntaxResult {
	t.Helper()
	if b.syntax.identity == nil {
		b.syntax.identity = &syntaxIdentity{}
	}
	request := syntaxRequest{identity: b.syntax.identity, key: syntaxKey{b.version, b.meta.Path}, runes: b.text, serial: b.syntax.serial, changes: b.syntax.changes, first: first, last: last}
	scanned := 0
	for attempt := 0; attempt < 500; attempt++ {
		result := calculate(request, nil)
		if result.err != nil {
			t.Fatalf("%s: %v", b.meta.Path, result.err)
		}
		scanned += result.scanned
		if result.complete {
			result.scanned = scanned
			b.syntax.changes = nil
			return result
		}
	}
	t.Fatal("syntax did not finish its demand")
	return syntaxResult{}
}

func assertSyntaxEquivalent(t *testing.T, text string, a, b []syntaxToken) {
	t.Helper()
	for at := range []rune(text) {
		if got, want := syntaxAt(a, at), syntaxAt(b, at); got != want {
			t.Fatalf("syntax differs at rune %d in %q: got %q want %q", at, text, got, want)
		}
	}
}

func TestIncrementalChromaMatchesFullAfterEdits(t *testing.T) {
	for _, test := range []struct{ path, text string }{
		{"example.go", "package main\n/* 世界\ncomment */\nvar s = `alpha\nbeta`\nfunc main() { println(42) }\n"},
		{"example.c", "#include <stdio.h>\n/* alpha\nbeta */\nint main(void) { return 42; }\n"},
		{"example.cc", "class Example {\npublic:\n int value = 42;\n};\n"},
		{"example.py", "value = \"\"\"alpha\nbeta\"\"\"\ndef example():\n    return 'hello'\n"},
		{"example.sh", "#!/bin/bash\ncat <<EOF\nhello $USER\nEOF\necho \"hello\"\n"},
		{"example.json", "{\n  \"message\": \"hello\",\n  \"count\": 42\n}\n"},
		{"example.yaml", "message: hello\ntext: |\n  alpha\n  beta\ncount: 42\n"},
		{"example.toml", "[example]\nmessage = \"hello\"\ncount = 42\n"},
		{"example.vim", "\" comment\nset number\nlet g:value = 42\n"},
		{"example.md", "# heading\n\nparagraph\n\n```go\nvar n = 42\n```\n\n**bold**\n"},
		{"example.html", "<html>\n<!-- comment -->\n<script>const n = 42;</script>\n<p>hello</p>\n</html>\n"},
		{"example.xml", "<?xml version=\"1.0\"?>\n<example count=\"42\">hello</example>\n"},
		{"example.css", "/* comment */\n.example {\n color: #aabbcc;\n}\n"},
		{"example.js", "/* comment */\nconst value = `alpha\n${42}`;\nfunction example() { return true; }\n"},
		{"example.php", "<p>hello</p>\n<?php\nfunction example() { return 'hello'; }\n?>\n"},
		{"example.csv", "name,count\n\"hello, 世界\",42\n\"alpha\nbeta\",7\n"},
		{"example.ini", "[example]\nmessage=hello\ncount=42\n"},
		{"Dockerfile", "FROM alpine:latest\nENV EXAMPLE=hello\nRUN echo \"hello\"\n"},
	} {
		t.Run(test.path, func(t *testing.T) {
			e := memoryEditor(test.text)
			defer e.close(false)
			b := e.buffer()
			b.meta.Path = test.path
			calculate := newSyntaxCalculator()
			calculateTestDemand(t, calculate, b, 0, len(b.text)/2)
			result := calculateTestDemand(t, calculate, b, 0, len(b.text))
			assertSyntaxEquivalent(t, string(b.text), result.tokens, highlightTestSource(t, test.path, string(b.text)).tokens)
			random := rand.New(rand.NewSource(52))
			insertions := []string{"x", "\n", "/*", "*/", "'", "\"", "`", "世界", "$", ""}
			for edit := 0; edit < 30; edit++ {
				at := random.Intn(len(b.text) + 1)
				end := min(len(b.text), at+random.Intn(3))
				replacement := insertions[random.Intn(len(insertions))]
				e.replace(at, end, replacement, 0)
				result = calculateTestDemand(t, calculate, b, 0, len(b.text))
				assertSyntaxEquivalent(t, string(b.text), result.tokens, highlightTestSource(t, test.path, string(b.text)).tokens)
			}
		})
	}
}

func TestLazySyntaxViewportAndConvergence(t *testing.T) {
	e := memoryEditor(strings.Repeat("var number = 42\n", 20000))
	defer e.close(false)
	b := e.buffer()
	b.meta.Path = "large.go"
	calculate := newSyntaxCalculator()
	result := calculateTestDemand(t, calculate, b, 0, 800)
	if result.scanned > 850 || result.scanned < 800 {
		t.Fatalf("initial demand scanned %d runes", result.scanned)
	}
	result = calculateTestDemand(t, calculate, b, 0, 2000)
	if result.scanned > 1250 {
		t.Fatalf("scroll restarted whole prefix: %d", result.scanned)
	}
	at := b.start(30) + strings.Index(string(b.line(30)), "42")
	e.replace(at, at+2, "43", 0)
	result = calculateTestDemand(t, calculate, b, 0, 2000)
	if result.scanned > 100 {
		t.Fatalf("local edit did not converge: %d", result.scanned)
	}
	if result = calculateTestDemand(t, calculate, b, 0, 2000); result.scanned != 0 {
		t.Fatalf("inert demand re-lexed %d runes", result.scanned)
	}
	e.replace(b.start(1000), b.start(1000)+3, "let", 0)
	if result = calculateTestDemand(t, calculate, b, 0, 2000); result.scanned != 0 {
		t.Fatalf("offscreen edit re-lexed viewport: %d", result.scanned)
	}
}

func TestTeXMathAndTextGroups(t *testing.T) {
	text := "Plain prose $inline + x^2$ text.\n\\[display\\]\n\\begin{align*}\nalpha &= \\frac{1}{2} \\\\\n\\begin{aligned}\nbeta &= 3\n\\end{aligned}\n\\text{ordinary {nested} words}\n\\end{align*}\nAfter math \\$ not math.\n% $ comment\n\\verb|$not math$|\n\\begin{verbatim}\n$also not math$\n\\end{verbatim}\nLast prose.\n"
	e := memoryEditor(text)
	defer e.close(false)
	b := e.buffer()
	b.meta.Path = "example.tex"
	calculate := newSyntaxCalculator()
	for _, test := range []struct {
		needle string
		kind   chroma.TokenType
	}{
		{"Plain prose", chroma.Text}, {"inline", chroma.GenericOutput}, {"display", chroma.GenericOutput}, {"alpha", chroma.GenericOutput}, {"beta", chroma.GenericOutput},
		{"ordinary", chroma.Text}, {"nested", chroma.Text}, {"After math", chroma.Text}, {"Last prose", chroma.Text}, {"% $ comment", chroma.Comment}, {"$not math$", chroma.LiteralString}, {"$also not math$", chroma.LiteralString}, {"\\frac", chroma.Keyword},
	} {
		at := utf8.RuneCountInString(text[:strings.Index(text, test.needle)])
		result := calculateTestDemand(t, calculate, b, max(0, at-2), at+len([]rune(test.needle)))
		want := syntaxForeground(test.kind)
		if test.kind == chroma.Text {
			want = ""
		}
		if got := syntaxAt(result.tokens, at); got != want {
			t.Errorf("%q style %q want %q", test.needle, got, want)
		}
	}
}

func TestTeXDirtyStatePropagatesLazily(t *testing.T) {
	e := memoryEditor("Before\n$$\nmath\nstill math\n$$\nAfter\n" + strings.Repeat("Unchanged prose\n", 1000))
	defer e.close(false)
	b := e.buffer()
	b.meta.Path = "example.tex"
	calculate := newSyntaxCalculator()
	calculateTestDemand(t, calculate, b, 0, 1500)
	e.replace(b.start(1), b.end(1), "", 0)
	result := calculateTestDemand(t, calculate, b, 0, 1500)
	at := b.start(3)
	if syntaxAt(result.tokens, at) != "" {
		t.Fatal("removing math opener did not propagate state")
	}
	if result.scanned > 1600 {
		t.Fatalf("TeX calculated unrequested tail: %d", result.scanned)
	}
	// A second edit before unfinished propagation must not converge against a
	// checkpoint from a different revision and falsely reuse its invalid tail.
	e.replace(b.start(1), b.start(1), "$$", 0)
	result = calculateTestDemand(t, calculate, b, 0, len(b.text))
	fresh := newSyntaxCalculator()
	expected := calculateTestDemand(t, fresh, b, 0, len(b.text))
	assertSyntaxEquivalent(t, string(b.text), result.tokens, expected.tokens)
}
