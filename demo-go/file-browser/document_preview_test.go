package main

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"st-go/tui/preview"
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

func TestTeXPreviewFontMatchesGovimAcrossSidebarModes(t *testing.T) {
	for _, pane := range []int{240, 205} {
		browser := &Browser{rows: 60, cols: 240, previewW: pane, cellPixelW: 16, cellPixelH: 32}
		width, _ := browser.previewPDFPageSize()
		scale := preview.FontScaleForPane(width, pane, browser.cols)
		if scale <= 1.7 {
			t.Fatalf("large-screen browser font scale = %g, want larger than previous 1.7", scale)
		}
		actual := scale * float64(pane) * browser.cellPixelW / width
		expected := 0.64 * 1.07 * float64(browser.cols*60/100) * browser.cellPixelW / 140
		if difference := actual - expected; difference < -1e-9 || difference > 1e-9 {
			t.Fatalf("browser's displayed font density = %g, want %g", actual, expected)
		}
	}
}

func TestDocumentPreviewCacheDistinguishesFontSizes(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "font.tex")
	if err := os.WriteFile(path, []byte(`\begin{document}Body text.\end{document}`), 0o600); err != nil {
		t.Fatal(err)
	}
	renderer := newPreviewDocumentRenderer(dir)
	first, err := renderer.renderScaled(path, 140, 200, preview.FontScale)
	if err != nil {
		t.Fatal(err)
	}
	second, err := renderer.renderScaled(path, 140, 200, 0.8)
	if err != nil {
		t.Fatal(err)
	}
	if first.PDFPath == second.PDFPath {
		t.Fatal("font-size change reused a stale PDF")
	}
	cached, err := renderer.renderScaled(path, 140, 200, preview.FontScale)
	if err != nil || cached != first {
		t.Fatalf("same font-size preview was not cached: %v", err)
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

func TestDocumentPreviewCacheRemovesSupersededRevision(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "changing.md")
	if err := os.WriteFile(path, []byte("# First\n\nOld body."), 0o644); err != nil {
		t.Fatal(err)
	}
	renderer := newPreviewDocumentRenderer(dir)
	first, err := renderer.renderSized(path, 120, 160)
	if err != nil {
		t.Fatal(err)
	}
	second, err := renderer.renderSized(path, 140, 160)
	if err != nil {
		t.Fatal(err)
	}
	if first.PDFPath == second.PDFPath || len(renderer.docs) != 2 {
		t.Fatalf("size variants = %q, %q; cache entries=%d", first.PDFPath, second.PDFPath, len(renderer.docs))
	}
	if err := os.WriteFile(path, []byte("# Second\n\nNew body with a changed digest."), 0o644); err != nil {
		t.Fatal(err)
	}
	updated, err := renderer.renderSized(path, 120, 160)
	if err != nil {
		t.Fatal(err)
	}
	if updated.PDFPath == first.PDFPath || len(renderer.docs) != 1 {
		t.Fatalf("updated preview = %q; cache entries=%d", updated.PDFPath, len(renderer.docs))
	}
	for _, stale := range []string{first.PDFPath, second.PDFPath} {
		if _, err := os.Stat(stale); !os.IsNotExist(err) {
			t.Fatalf("stale preview %q still exists: %v", stale, err)
		}
	}
}

func TestDocumentPreviewCacheBoundsSizeVariants(t *testing.T) {
	dir := t.TempDir()
	renderer := newPreviewDocumentRenderer(dir)
	logicalPath := renderer.prepareSource(filepath.Join(dir, "document.tex"), "revision")
	var paths []string
	for index := 0; index < maxPreviewVariants+2; index++ {
		path := filepath.Join(dir, fmt.Sprintf("variant-%d.pdf", index))
		if err := os.WriteFile(path, []byte("pdf"), 0o600); err != nil {
			t.Fatal(err)
		}
		paths = append(paths, path)
		renderer.storeSourcePreview(logicalPath, fmt.Sprintf("key-%d", index), &documentPreview{PDFPath: path})
	}
	if len(renderer.docs) != maxPreviewVariants {
		t.Fatalf("variant cache entries = %d, want %d", len(renderer.docs), maxPreviewVariants)
	}
	for _, stale := range paths[:2] {
		if _, err := os.Stat(stale); !os.IsNotExist(err) {
			t.Fatalf("evicted variant %q still exists: %v", stale, err)
		}
	}
}

func TestDocumentPreviewCacheBoundsAllSources(t *testing.T) {
	dir := t.TempDir()
	renderer := newPreviewDocumentRenderer(dir)
	var paths []string
	for index := 0; index < maxDocumentPreviews+2; index++ {
		logicalPath := renderer.prepareSource(filepath.Join(dir, fmt.Sprintf("document-%d.tex", index)), "revision")
		path := filepath.Join(dir, fmt.Sprintf("document-%d.pdf", index))
		if err := os.WriteFile(path, []byte("pdf"), 0o600); err != nil {
			t.Fatal(err)
		}
		paths = append(paths, path)
		renderer.storeSourcePreview(logicalPath, fmt.Sprintf("key-%d", index), &documentPreview{PDFPath: path})
	}
	if len(renderer.docs) != maxDocumentPreviews || len(renderer.order) != maxDocumentPreviews {
		t.Fatalf("global preview cache = %d entries / %d order, want %d", len(renderer.docs), len(renderer.order), maxDocumentPreviews)
	}
	for _, stale := range paths[:2] {
		if _, err := os.Stat(stale); !os.IsNotExist(err) {
			t.Fatalf("globally evicted preview %q still exists: %v", stale, err)
		}
	}
}
