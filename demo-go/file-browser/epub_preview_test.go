package main

import (
	"archive/zip"
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeTestEPUB(t *testing.T, filePath string, files map[string]string) {
	t.Helper()
	file, err := os.Create(filePath)
	if err != nil {
		t.Fatal(err)
	}
	writer := zip.NewWriter(file)
	for name, contents := range files {
		entry, err := writer.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := entry.Write([]byte(contents)); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
}

func testEPUBFiles() map[string]string {
	return map[string]string{
		"mimetype": "application/epub+zip",
		"META-INF/container.xml": `<?xml version="1.0"?>
<container xmlns="urn:oasis:names:tc:opendocument:xmlns:container">
  <rootfiles><rootfile full-path="OEBPS/content.opf" media-type="application/oebps-package+xml"/></rootfiles>
</container>`,
		"OEBPS/content.opf": `<?xml version="1.0" encoding="UTF-8"?>
<package xmlns="http://www.idpf.org/2007/opf" version="3.0">
  <metadata xmlns:dc="http://purl.org/dc/elements/1.1/">
    <dc:title>Test Publication</dc:title><dc:creator>Ada Reader</dc:creator>
  </metadata>
  <manifest>
    <item id="one" href="text/chapter1.xhtml" media-type="application/xhtml+xml"/>
    <item id="two" href="text/chapter2.xhtml" media-type="application/xhtml+xml"/>
  </manifest>
  <spine><itemref idref="one"/><itemref idref="two"/></spine>
</package>`,
		"OEBPS/text/chapter1.xhtml": `<!doctype html><html xmlns="http://www.w3.org/1999/xhtml"><head>
<title>First fallback title</title><style>.hidden { display:none }</style></head><body>
<h1 id="opening">Opening</h1><p style="color: red">Hello <strong>reader</strong>. <a href="chapter2.xhtml#destination">Continue</a>.
<a href="https://example.com/book">Publisher</a><a href="javascript:alert(1)">Unsafe</a></p>
<script>script-marker</script><p hidden="hidden">hidden-marker</p>
<table><tr><th>Term</th><th>Meaning</th></tr><tr><td>EPUB</td><td>Book</td></tr></table>
<figure><img src="cover.png" alt="A useful diagram"/><figcaption>Figure caption</figcaption></figure>
</body></html>`,
		"OEBPS/text/chapter2.xhtml": `<!doctype html><html xmlns="http://www.w3.org/1999/xhtml"><head><title>Second chapter</title></head>
<body><p id="destination">Arrived in 第二章.</p></body></html>`,
	}
}

func TestReadEPUBBookNormalizesSpine(t *testing.T) {
	filePath := filepath.Join(t.TempDir(), "book.epub")
	writeTestEPUB(t, filePath, testEPUBFiles())
	book, err := readEPUBBook(filePath)
	if err != nil {
		t.Fatal(err)
	}
	if book.Title != "Test Publication" || book.Author != "Ada Reader" {
		t.Fatalf("metadata = %q / %q", book.Title, book.Author)
	}
	if count := strings.Count(book.HTML, `class="epub-chapter"`); count != 2 {
		t.Fatalf("semantic HTML has %d chapters, want 2: %s", count, book.HTML)
	}
	for _, wanted := range []string{
		"Opening", "Hello", "Term", "Meaning", "EPUB", "Book",
		"[Image: A useful diagram]", "Figure caption", "Second chapter", "第二章",
		`href="#` + epubAnchorID(1, "destination") + `"`,
		`id="` + epubAnchorID(1, "destination") + `"`,
	} {
		if !strings.Contains(book.HTML, wanted) {
			t.Errorf("semantic HTML is missing %q: %s", wanted, book.HTML)
		}
	}
	for _, unwanted := range []string{"script-marker", "hidden-marker", "<style", `style="color: red"`, "javascript:"} {
		if strings.Contains(book.HTML, unwanted) {
			t.Errorf("semantic HTML retained %q: %s", unwanted, book.HTML)
		}
	}
}

func TestEPUBFileTypeIsRenderedText(t *testing.T) {
	browser := &Browser{}
	if got := browser.fileType("/books/example.epub"); got != "text" {
		t.Fatalf("EPUB file type = %q, want text", got)
	}
}

func TestEPUBDocumentPreviewUsesSemanticPDF(t *testing.T) {
	dir := t.TempDir()
	filePath := filepath.Join(dir, "book.epub")
	writeTestEPUB(t, filePath, testEPUBFiles())
	renderer := newPreviewDocumentRenderer(dir)
	document, err := renderer.renderSized(filePath, 120, 160)
	if err != nil {
		t.Fatal(err)
	}
	if document.Kind != "EPUB" || document.PDFPath == "" || document.PageCount < 3 {
		t.Fatalf("unexpected EPUB preview: %#v", document)
	}
	pdf, err := os.ReadFile(document.PDFPath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.HasPrefix(pdf, []byte("%PDF-")) || bytes.Contains(pdf, []byte("/Subtype /Image")) {
		t.Fatalf("EPUB preview is not a native vector PDF (%d bytes)", len(pdf))
	}
	if !isRenderedDocument(filePath) {
		t.Fatal("EPUB was not classified as a rendered document")
	}
}

func TestEPUBPreviewEmitsPDFPages(t *testing.T) {
	dir := t.TempDir()
	filePath := filepath.Join(dir, "book.epub")
	writeTestEPUB(t, filePath, testEPUBFiles())
	f := startBrowser(t, testEnv(), dir)
	f.send("\x1b[B")
	waitContains(t, f, "EPUB  1/", "EPUB first page title")
	waitContains(t, f, " page 1", "EPUB first PDF page")
	if strings.Contains(f.output(), "fit-contain page 1") {
		t.Fatal("EPUB semantic PDF requested an avoidable second scaling pass")
	}
	f.send("]")
	waitContains(t, f, "EPUB  2/", "EPUB second PDF page")
}

func TestEPUBRejectsUnsafePackagePath(t *testing.T) {
	filePath := filepath.Join(t.TempDir(), "unsafe.epub")
	writeTestEPUB(t, filePath, map[string]string{
		"META-INF/container.xml": `<container><rootfiles><rootfile full-path="../content.opf"/></rootfiles></container>`,
	})
	if _, err := readEPUBBook(filePath); err == nil || !strings.Contains(err.Error(), "unsafe archive path") {
		t.Fatalf("unsafe package path error = %v", err)
	}
}
