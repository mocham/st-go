package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func testHTMLDocument() string {
	return `<!doctype html><html><head>
<title>Semantic HTML Preview</title>
<meta name="author" content="Grace Reader">
<style>body { display: none } .unused { color: red }</style>
</head><body>
<h1 id="opening">Opening</h1>
<p>Hello <strong>reader</strong>. <a href="#destination">Jump</a>.
<a href="https://example.com/article">Source</a><a href="javascript:alert(1)">Unsafe</a></p>
<script>script-marker</script><p hidden>hidden-marker</p>
<table><tr><th>Format</th><th>Preview</th></tr><tr><td>HTML</td><td>PDF</td></tr></table>
<p id="destination">Destination <img src="diagram.png" alt="A semantic diagram"></p>
</body></html>`
}

func TestReadHTMLBookNormalizesDocument(t *testing.T) {
	filePath := filepath.Join(t.TempDir(), "article.html")
	if err := os.WriteFile(filePath, []byte(testHTMLDocument()), 0o644); err != nil {
		t.Fatal(err)
	}
	book, err := readHTMLBook(filePath)
	if err != nil {
		t.Fatal(err)
	}
	if book.Title != "Semantic HTML Preview" || book.Author != "Grace Reader" {
		t.Fatalf("metadata = %q / %q", book.Title, book.Author)
	}
	for _, wanted := range []string{
		`class="html-document"`, "Opening", "Hello", "Format", "Preview", "HTML", "PDF",
		"[Image: A semantic diagram]", `href="#` + epubAnchorID(0, "destination") + `"`,
		`id="` + epubAnchorID(0, "destination") + `"`, `href="https://example.com/article"`,
	} {
		if !strings.Contains(book.HTML, wanted) {
			t.Errorf("semantic HTML is missing %q: %s", wanted, book.HTML)
		}
	}
	for _, unwanted := range []string{"epub-chapter", "script-marker", "hidden-marker", "<style", "javascript:"} {
		if strings.Contains(book.HTML, unwanted) {
			t.Errorf("semantic HTML retained %q: %s", unwanted, book.HTML)
		}
	}
}

func TestHTMLPreviewDecodesDeclaredCharset(t *testing.T) {
	filePath := filepath.Join(t.TempDir(), "latin.html")
	source := append([]byte(`<html><head><meta charset="iso-8859-1"><title>Caf`), 0xe9)
	source = append(source, []byte(`</title></head><body><p>R`)...)
	source = append(source, 0xe9)
	source = append(source, []byte(`sum`)...)
	source = append(source, 0xe9)
	source = append(source, []byte(`</p></body></html>`)...)
	if err := os.WriteFile(filePath, source, 0o644); err != nil {
		t.Fatal(err)
	}
	book, err := readHTMLBook(filePath)
	if err != nil {
		t.Fatal(err)
	}
	if book.Title != "Café" || !strings.Contains(book.HTML, "Résumé") {
		t.Fatalf("declared charset was not decoded: title=%q HTML=%s", book.Title, book.HTML)
	}
}

func TestHTMLDocumentPreviewUsesSemanticPDF(t *testing.T) {
	dir := t.TempDir()
	filePath := filepath.Join(dir, "article.html")
	if err := os.WriteFile(filePath, []byte(testHTMLDocument()), 0o644); err != nil {
		t.Fatal(err)
	}
	renderer := newPreviewDocumentRenderer(dir)
	document, err := renderer.renderSized(filePath, 120, 160)
	if err != nil {
		t.Fatal(err)
	}
	if document.Kind != "HTML" || document.PDFPath == "" || document.PageCount < 1 {
		t.Fatalf("unexpected HTML preview: %#v", document)
	}
	pdf, err := os.ReadFile(document.PDFPath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.HasPrefix(pdf, []byte("%PDF-")) || bytes.Contains(pdf, []byte("/Subtype /Image")) {
		t.Fatalf("HTML preview is not a native vector PDF (%d bytes)", len(pdf))
	}
	for _, extension := range []string{".html", ".htm", ".xhtml"} {
		if !isRenderedDocument("document" + extension) {
			t.Errorf("%s was not classified as a rendered document", extension)
		}
	}
}

func TestHTMLPreviewEmitsPDFPage(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "article.html"), []byte(testHTMLDocument()), 0o644); err != nil {
		t.Fatal(err)
	}
	f := startBrowser(t, testEnv(), dir)
	f.send("\x1b[B")
	waitContains(t, f, "HTML  1/", "HTML first page title")
	waitContains(t, f, " page 1", "HTML PDF page")
	if strings.Contains(f.output(), "fit-contain page 1") {
		t.Fatal("HTML semantic PDF requested an avoidable second scaling pass")
	}
}

func TestXHTMLFileTypeIsRenderedText(t *testing.T) {
	browser := &Browser{}
	if got := browser.fileType("/documents/example.xhtml"); got != "text" {
		t.Fatalf("XHTML file type = %q, want text", got)
	}
}
