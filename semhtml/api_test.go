package semhtml

import (
	"bytes"
	"regexp"
	"testing"
)

var testPDFPageObject = regexp.MustCompile(`/Type\s*/Page\b`)

func TestRenderSemanticHTMLPDF(t *testing.T) {
	renderer := NewRenderer()
	document := Document{
		Title:  "Semantic book",
		Author: "Example Author",
		HTML: `<section class="epub-chapter" id="one">
<h1>First chapter</h1><p>Text with <strong>weight</strong>, <em>emphasis</em>, and <a href="#target">a link</a>.</p>
</section><section class="epub-chapter" id="two"><h1 id="target">第二章</h1><pre>code block</pre></section>`,
	}
	pdf, err := renderer.RenderPDFWithOptions(document, PDFOptions{PageWidth: 120, PageHeight: 160})
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.HasPrefix(pdf, []byte("%PDF-")) {
		t.Fatalf("renderer returned non-PDF output: %q", pdf[:min(len(pdf), 16)])
	}
	if pages := len(testPDFPageObject.FindAll(pdf, -1)); pages < 3 {
		t.Fatalf("PDF has %d pages, want title page plus two chapters", pages)
	}
	if bytes.Contains(pdf, []byte("/Subtype /Image")) {
		t.Fatal("semantic text was unexpectedly rasterized")
	}
}

func TestPDFCacheKeyIncludesDimensions(t *testing.T) {
	document := Document{Title: "Book", HTML: "<p>Text</p>"}
	first := PDFCacheKeyWithOptions(document, PDFOptions{PageWidth: 100, PageHeight: 150})
	second := PDFCacheKeyWithOptions(document, PDFOptions{PageWidth: 120, PageHeight: 150})
	if first == second {
		t.Fatal("PDF cache key ignored page dimensions")
	}
}
