package main

import (
	"bytes"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPreviewMathMacrosExpand(t *testing.T) {
	source := `
\newcommand{\RR}{\mathbb{R}}
\newcommand{\pair}[2]{\left(#1,#2\right)}
\def\sq#1{#1^2}
`
	macros := extractPreviewMathMacros(source)
	got := expandPreviewMathMacros(`\pair{\RR}{\sq{x}}`, macros)
	for _, unexpanded := range []string{`\pair`, `\RR`, `\sq`} {
		if strings.Contains(got, unexpanded) {
			t.Fatalf("macro %s was not expanded: %s", unexpanded, got)
		}
	}
	if !strings.Contains(got, `\mathbb{R}`) || !strings.Contains(got, `^2`) {
		t.Fatalf("expanded expression lost macro bodies: %s", got)
	}
}

func TestPreviewFormulaRasterizes(t *testing.T) {
	renderer := newPreviewDocumentRenderer(t.TempDir())
	for _, expression := range []string{`f:\mathbb{R}\to\mathbb{R}`, `\int_0^1 x^2\,dx=\frac13`} {
		formula, err := renderer.formulaExpanded(expression, true, 230, 1000)
		if err != nil {
			t.Fatalf("rasterize %q: %v", expression, err)
		}
		nonTransparent := 0
		for offset := 3; offset < len(formula.Pix); offset += 4 {
			if formula.Pix[offset] != 0 {
				nonTransparent++
			}
		}
		if nonTransparent == 0 {
			t.Fatalf("rasterize %q produced a blank image", expression)
		}
	}
}

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

func TestMarkdownDocumentPreviewRendersMathJaxPage(t *testing.T) {
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
	if document.Kind != "Markdown" || len(document.Pages) != 1 || document.MathErrors != 0 {
		t.Fatalf("unexpected document result: %#v", document)
	}
	file, err := os.Open(document.Pages[0])
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	page, err := png.Decode(file)
	if err != nil {
		t.Fatal(err)
	}
	if page.Bounds().Dx() != documentPreviewWidth || page.Bounds().Dy() != documentPreviewHeight {
		t.Fatalf("page bounds = %v, want %dx%d", page.Bounds(), documentPreviewWidth, documentPreviewHeight)
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
