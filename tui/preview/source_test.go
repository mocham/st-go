package preview

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func fixtureReader(files map[string]string) Reader {
	return func(path string) (string, error) {
		value, ok := files[filepath.Base(path)]
		if !ok {
			return "", fmt.Errorf("%s: %w", path, os.ErrNotExist)
		}
		return value, nil
	}
}

func TestIncludesMainAndCursorMapping(t *testing.T) {
	dir := t.TempDir()
	root := filepath.Join(dir, "main.tex")
	child := filepath.Join(dir, "part.tex")
	files := map[string]string{
		"main.tex": "\\documentclass{article}\n\\begin{document}\nBefore\n\\input{part}\nAfter\n\\end{document}\n",
		"part.tex": "%main=main\nFirst child line\n%cursor here\nLast child line\n",
	}
	snapshot, err := Load(child, fixtureReader(files))
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Root != root || !strings.Contains(snapshot.Source, "First child line") || strings.Contains(snapshot.Source, `\input{part}`) {
		t.Fatalf("not expanded: root=%q source=%q", snapshot.Root, snapshot.Source)
	}
	if !snapshot.HasFile(root) || !snapshot.HasFile(child) || !snapshot.HasFile(filepath.Join(dir, "part")) {
		t.Fatal("dependency set lost extensionless resolution")
	}
	if got, want := snapshot.Position(child, 3), snapshot.ExpandedPosition(snapshot.CursorLine); got != want {
		t.Fatalf("included cursor mapping %d want %d", got, want)
	}
	if snapshot.Position(root, 5) <= snapshot.Position(child, 2) {
		t.Fatal("root/include line mapping order is wrong")
	}
	if snapshot.Position(child, 1000) > 1000000 || snapshot.Position(root, -1) < 0 {
		t.Fatal("navigation did not clamp")
	}
}

func TestSourceCyclesMissingAndBudgets(t *testing.T) {
	for _, files := range []map[string]string{
		{"a.tex": "%main=b\n", "b.tex": "%main=a\n"},
		{"a.tex": `\input{b}`, "b.tex": `\input{a}`},
	} {
		if _, err := Load(filepath.Join(t.TempDir(), "a.tex"), fixtureReader(files)); err == nil {
			t.Fatal("recursive source accepted")
		}
	}
	snapshot, err := Load(filepath.Join(t.TempDir(), "root.tex"), fixtureReader(map[string]string{"root.tex": `Before \input{generated} After`}))
	if err != nil || !strings.Contains(snapshot.Source, `\input{generated}`) || !snapshot.HasFile(filepath.Join(filepath.Dir(snapshot.Root), "generated.tex")) {
		t.Fatalf("missing generated input not retained: %+v %v", snapshot, err)
	}
	if _, err := Load("large.tex", fixtureReader(map[string]string{"large.tex": strings.Repeat("\n", (64<<20)/64+1)})); err == nil {
		t.Fatal("unbounded line map accepted")
	}
}

func TestCursorMarkersAndMarkdownCode(t *testing.T) {
	for _, test := range []struct {
		source   string
		markdown bool
		line     int
	}{
		{"\\%cursor escaped\n% ordinary comment mentions %cursor\nText %cursor target\n%cursor later", false, 3},
		{"\\\\%cursor after line break", false, 1},
		{"\\verb|%cursor literal|\n\\begin{verbatim}\n%cursor literal\n\\end{verbatim}\n%cursor actual", false, 5},
		{"    %cursor indented code\n%cursor actual", true, 2},
		{"```tex\n%cursor example\n```\n`%cursor inline` and \\%cursor escaped\n%cursor actual\n# Heading\n", true, 5},
		{"~~~\n%cursor example\n~~~\nText %cursor actual\n", true, 4},
	} {
		line, stripped := cursorMarker(test.source, test.markdown)
		if line != test.line {
			t.Errorf("%q line=%d want %d", test.source, line, test.line)
		}
		if test.markdown && strings.Count(stripped, "\n") != strings.Count(test.source, "\n") {
			t.Fatal("stripping marker shifted source lines")
		}
	}
}

func TestMarkdownAndLatexRenderSelectablePDF(t *testing.T) {
	for _, test := range []struct{ path, source string }{
		{"notes.md", "# Synthetic heading\n\n%cursor\nHello **world**, $x^2$ and 世界.\n\n$$a+b=c$$\n"},
		{"notes.tex", "\\begin{document}\n%cursor\n\\section{Synthetic heading}\nHello world, $x^2$ and 世界.\n\\end{document}\n"},
	} {
		t.Run(test.path, func(t *testing.T) {
			snapshot, err := Load(filepath.Join(t.TempDir(), test.path), fixtureReader(map[string]string{test.path: test.source}))
			if err != nil {
				t.Fatal(err)
			}
			pdf, mathErrors, err := Render(snapshot)
			if err != nil || mathErrors != 0 || !strings.HasPrefix(string(pdf), "%PDF-") {
				t.Fatalf("render bytes=%d math errors=%d err=%v", len(pdf), mathErrors, err)
			}
			if snapshot.CursorLine == 0 {
				t.Fatal("source anchor lost")
			}
		})
	}
}

func TestSupportedNames(t *testing.T) {
	for _, name := range []string{"a.tex", "a.LaTeX", "a.ltx", "a.md", "a.markdown", "a.mdown", "a.mkd"} {
		if !Supported(name) {
			t.Errorf("%s unsupported", name)
		}
	}
	for _, name := range []string{"", "notes.txt", "example.go", "paper.pdf"} {
		if Supported(name) {
			t.Errorf("%s incorrectly supported", name)
		}
	}
}
