package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestExpandLocalTeXDocumentRecursively(t *testing.T) {
	directory := t.TempDir()
	root := filepath.Join(directory, "root.tex")
	writeLocalTeXTestFile(t, root, `\documentclass{article}
% \input{ignored}
\input{macros}
\begin{document}\include{chapters/body}\end{document}`)
	writeLocalTeXTestFile(t, filepath.Join(directory, "macros.tex"), `\newcommand{\R}{\mathbb R}`)
	writeLocalTeXTestFile(t, filepath.Join(directory, "chapters", "body.tex"), `Nested $\R$.`)

	source, err := os.ReadFile(root)
	if err != nil {
		t.Fatal(err)
	}
	expanded, err := expandLocalTeXDocument(root, source, maxPreviewDocumentSize)
	if err != nil {
		t.Fatal(err)
	}
	text := string(expanded)
	for _, wanted := range []string{`\newcommand{\R}{\mathbb R}`, `Nested $\R$.`, `% \input{ignored}`} {
		if !strings.Contains(text, wanted) {
			t.Errorf("expanded source is missing %q: %s", wanted, text)
		}
	}
	if strings.Contains(text, `\input{macros}`) || strings.Contains(text, `\include{chapters/body}`) {
		t.Fatalf("expanded source retained resolved includes: %s", text)
	}
}

func TestTeXPreviewCacheIncludesDependencyContent(t *testing.T) {
	directory := t.TempDir()
	cache := t.TempDir()
	root := filepath.Join(directory, "root.tex")
	body := filepath.Join(directory, "body.tex")
	writeLocalTeXTestFile(t, root, `\documentclass{article}\begin{document}\input{body}\end{document}`)
	writeLocalTeXTestFile(t, body, `First $x$.`)
	renderer := newPreviewDocumentRenderer(cache)

	first, err := renderer.renderSized(root, 120, 160)
	if err != nil {
		t.Fatal(err)
	}
	writeLocalTeXTestFile(t, body, `Changed $y$.`)
	second, err := renderer.renderSized(root, 120, 160)
	if err != nil {
		t.Fatal(err)
	}
	if first.PDFPath == second.PDFPath || len(renderer.docs) != 2 {
		t.Fatalf("include edit reused stale preview: first=%q second=%q entries=%d", first.PDFPath, second.PDFPath, len(renderer.docs))
	}
}

func TestExpandLocalTeXDocumentRejectsCycles(t *testing.T) {
	directory := t.TempDir()
	first := filepath.Join(directory, "first.tex")
	writeLocalTeXTestFile(t, first, `\input{second}`)
	writeLocalTeXTestFile(t, filepath.Join(directory, "second.tex"), `\input{first}`)
	source, err := os.ReadFile(first)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := expandLocalTeXDocument(first, source, maxPreviewDocumentSize); err == nil || !strings.Contains(err.Error(), "recursive TeX include") {
		t.Fatalf("recursive expansion error = %v", err)
	}
}

func TestExpandLocalTeXDocumentAddsUsedTableCompanion(t *testing.T) {
	directory := t.TempDir()
	fragment := filepath.Join(directory, "example.tex")
	writeLocalTeXTestFile(t, fragment, `\section[Short]{Long heading}\ExampleTable`)
	definitions := `\newcommand{\ExampleTable}{\begin{tabular}{cc}Name & $x$\\A & $1$\end{tabular}}`
	writeLocalTeXTestFile(t, filepath.Join(directory, "example-tables.tex"), definitions)
	source, err := os.ReadFile(fragment)
	if err != nil {
		t.Fatal(err)
	}
	expanded, err := expandLocalTeXDocument(fragment, source, maxPreviewDocumentSize)
	if err != nil {
		t.Fatal(err)
	}
	text := string(expanded)
	if !strings.Contains(text, definitions) || !strings.Contains(text, `\begin{document}`) {
		t.Fatalf("table companion was not attached to the fragment: %s", text)
	}
}

func writeLocalTeXTestFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}
