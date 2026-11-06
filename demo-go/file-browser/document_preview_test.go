package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPreviewPageAfterClampsBounds(t *testing.T) {
	for _, test := range []struct {
		page, delta, total, want int
	}{
		{page: 1, delta: -1, total: 4, want: 1},
		{page: 2, delta: 1, total: 4, want: 3},
		{page: 4, delta: 1, total: 4, want: 4},
		{page: 4, delta: 1, total: 0, want: 5},
	} {
		if got := previewPageAfter(test.page, test.delta, test.total); got != test.want {
			t.Errorf("previewPageAfter(%d, %d, %d) = %d, want %d", test.page, test.delta, test.total, got, test.want)
		}
	}
}

func TestPreviewPDFPageSizeMatchesPanePixels(t *testing.T) {
	browser := &Browser{
		rows: 27, previewW: 43,
		cellPixelW: 9, cellPixelH: 18,
	}
	width, height := browser.previewPDFPageSize()
	wantRatio := float64(43*9) / float64((27-3)*18)
	if difference := width/height - wantRatio; difference < -1e-9 || difference > 1e-9 {
		t.Fatalf("page ratio = %g, pane pixel ratio = %g", width/height, wantRatio)
	}
	if width < 60 || height < 60 {
		t.Fatalf("page size %.2fx%.2fmm is too small for latexpdf margins", width, height)
	}
}

func TestMarkdownDocumentPreviewUsesLatexPDF(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "notes.md")
	source := `# Macro preview

\newcommand{\RR}{\mathbb{R}}

Inline math uses $f:\RR\to\RR$ and a display follows.

$$
\int_0^1 x^2\,dx=\frac13
$$
`
	if err := os.WriteFile(path, []byte(source), 0644); err != nil {
		t.Fatal(err)
	}
	renderer := newPreviewDocumentRenderer(dir)
	document, err := renderer.render(path)
	if err != nil {
		t.Fatal(err)
	}
	if document.Kind != "Markdown" || document.PDFPath == "" || len(document.Pages) != 0 || document.PageCount < 1 || document.MathErrors != 0 {
		t.Fatalf("unexpected document result: %#v", document)
	}
	pdf, err := os.ReadFile(document.PDFPath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.HasPrefix(pdf, []byte("%PDF-")) || bytes.Contains(pdf, []byte("/Subtype /Image")) {
		t.Fatalf("Markdown preview is not a native vector PDF (%d bytes)", len(pdf))
	}
	if pagePath, isPDF := document.pagePath(1); !isPDF || pagePath != document.PDFPath {
		t.Fatalf("Markdown pagePath = %q, %t; want PDF %q", pagePath, isPDF, document.PDFPath)
	}
}

func TestTeXDocumentPreviewPaginates(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "lecture.tex")
	body := strings.Repeat("This paragraph contains enough text to exercise page layout and remains inside the rendered page.\n\n", 95)
	source := `\documentclass{article}
\newcommand{\ZZ}{\mathbb{Z}}
\title{A paginated lecture}
\begin{document}
\maketitle
\section{First section}
The macro renders as $\ZZ/n\ZZ$.

` + body + `
\end{document}`
	if err := os.WriteFile(path, []byte(source), 0644); err != nil {
		t.Fatal(err)
	}
	renderer := newPreviewDocumentRenderer(dir)
	document, err := renderer.render(path)
	if err != nil {
		t.Fatal(err)
	}
	if document.Kind != "TeX" || document.MathErrors != 0 {
		t.Fatalf("unexpected TeX result: %#v", document)
	}
	if document.PageCount < 2 {
		t.Fatalf("long TeX document rendered as %d page(s), want at least 2", document.PageCount)
	}
	if len(document.Pages) != 0 || document.PDFPath == "" {
		t.Fatalf("TeX preview was not rendered directly to PDF: %#v", document)
	}
	pdf, err := os.ReadFile(document.PDFPath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.HasPrefix(pdf, []byte("%PDF-")) {
		t.Fatalf("TeX preview is not a PDF: %q", pdf[:min(len(pdf), 16)])
	}
	if got := len(pdfPageObject.FindAll(pdf, -1)); got != document.PageCount {
		t.Fatalf("PDF page objects = %d, preview page count = %d", got, document.PageCount)
	}
	if pagePath, isPDF := document.pagePath(2); !isPDF || pagePath != document.PDFPath {
		t.Fatalf("pagePath(2) = %q, %t; want shared PDF %q", pagePath, isPDF, document.PDFPath)
	}
}
