package main

import (
	"archive/zip"
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func testDOCXFiles() map[string]string {
	return map[string]string{
		"word/document.xml": `<?xml version="1.0"?>
<w:document xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main"><w:body>
<w:p><w:r><w:t>Hello </w:t></w:r><w:hyperlink><w:r><w:t>reader &amp; 世界</w:t></w:r></w:hyperlink></w:p>
<w:p><w:r><w:t>Next</w:t><w:tab/><w:t>column</w:t><w:br/><w:t>New line</w:t></w:r></w:p>
<w:tbl><w:tr><w:tc><w:p><w:r><w:t>Cell A</w:t></w:r></w:p></w:tc>
<w:tc><w:p><w:r><w:t>Cell B</w:t></w:r></w:p></w:tc></w:tr></w:tbl>
<w:p><w:r><w:instrText>field-code-marker</w:instrText><w:t>Visible result</w:t></w:r>
<w:del><w:r><w:t>deleted-marker</w:t></w:r></w:del>
<w:r><w:drawing><w:t>image-marker</w:t></w:drawing></w:r></w:p>
<w:p><w:r><w:t>&lt;script&gt;plain text&lt;/script&gt;</w:t></w:r></w:p>
<w:sectPr/></w:body></w:document>`,
		"word/media/ignore.png": "image data that is not text",
	}
}

func testXLSXFiles() map[string]string {
	return map[string]string{
		"xl/workbook.xml": `<workbook xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main"
xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships"><sheets>
<sheet name="Budget &amp; totals" sheetId="7" r:id="rId7"/>
<sheet name="Notes" sheetId="2" r:id="rId2"/>
</sheets></workbook>`,
		"xl/_rels/workbook.xml.rels": `<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">
<Relationship Id="rId2" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/worksheet" Target="/xl/worksheets/notes.xml"/>
<Relationship Id="rId7" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/worksheet" Target="worksheets/budget.xml"/>
<Relationship Id="strings" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/sharedStrings" Target="strings.xml"/>
<Relationship Id="link" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/hyperlink" Target="https://example.invalid/never-fetch" TargetMode="External"/>
</Relationships>`,
		"xl/strings.xml": `<sst xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main">
<si><t>Name &amp; value</t></si><si><r><t>Rich </t></r><r><t>text 世界</t></r><rPh><t>phonetic-marker</t></rPh></si>
</sst>`,
		"xl/worksheets/budget.xml": `<worksheet xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main"><sheetData>
<row r="1"><c r="A1" t="s"><v>0</v></c><c r="C1"><v>42.5</v></c><c r="E1" t="b"><v>1</v></c></row>
<row r="3"><c r="B3" t="inlineStr"><is><r><t>Inline </t></r><r><t>&lt;tag&gt;</t></r></is></c><c r="F3" t="s"><v>1</v></c></row>
<row r="4"><c r="A4"><f>SUM(C1,1)</f><v>43.5</v></c><c r="D4"><f>C1*2</f></c><c r="G4" t="b"><v>0</v></c></row>
<row r="5"><c r="A5" t="e"><v>#DIV/0!</v></c><c r="C5" t="str"><v>Saved string</v></c><c r="D5" s="1"><v>45292</v></c></row>
<row r="1048576"><c r="XFD1048576" t="inlineStr"><is><t>Sparse end</t></is></c></row>
</sheetData></worksheet>`,
		"xl/worksheets/notes.xml": `<worksheet><sheetData><row r="2"><c r="A2" t="inlineStr"><is><t>Second sheet</t></is></c></row></sheetData></worksheet>`,
		"xl/media/ignore.png":     "not preview text",
	}
}

func TestOfficeDOCXExtractsBodyText(t *testing.T) {
	path := filepath.Join(t.TempDir(), "letter.DOCX")
	makeTestZip(t, path, testDOCXFiles())
	book, err := readOfficeBook(path)
	if err != nil {
		t.Fatal(err)
	}
	if book.Title != "letter" {
		t.Fatalf("title = %q, want letter", book.Title)
	}
	for _, wanted := range []string{
		"Hello reader &amp; 世界", "Next | column", "New line", "Cell A", " | Cell B",
		"Visible result", "&lt;script&gt;plain text&lt;/script&gt;",
	} {
		if !strings.Contains(book.HTML, wanted) {
			t.Errorf("DOCX text missing %q: %s", wanted, book.HTML)
		}
	}
	for _, unwanted := range []string{"field-code-marker", "deleted-marker", "image-marker", "image data", "<script>"} {
		if strings.Contains(book.HTML, unwanted) {
			t.Errorf("DOCX text retained %q", unwanted)
		}
	}
}

func TestOfficeXLSXExtractsSheetsAndCellValues(t *testing.T) {
	path := filepath.Join(t.TempDir(), "budget.xlsx")
	makeTestZip(t, path, testXLSXFiles())
	book, err := readOfficeBook(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, wanted := range []string{
		"<h2>Budget &amp; totals</h2>", "<h2>Notes</h2>",
		"Row 1: A1: Name &amp; value | C1: 42.5 | E1: TRUE",
		"Row 3: B3: Inline &lt;tag&gt; | F3: Rich text 世界",
		"Row 4: A4: 43.5 | D4: =C1*2 | G4: FALSE",
		"A5: #DIV/0!", "C5: Saved string", "D5: 45292",
		"Row 1048576: XFD1048576: Sparse end", "A2: Second sheet",
	} {
		if !strings.Contains(book.HTML, wanted) {
			t.Errorf("XLSX text missing %q: %s", wanted, book.HTML)
		}
	}
	if strings.Index(book.HTML, "Budget") > strings.Index(book.HTML, "Notes") {
		t.Fatal("worksheets were not kept in workbook order")
	}
	for _, unwanted := range []string{"SUM(C1,1)", "phonetic-marker", "never-fetch", "<tag>"} {
		if strings.Contains(book.HTML, unwanted) {
			t.Errorf("XLSX text retained %q", unwanted)
		}
	}
}

func TestOfficeXLSXWithoutSharedStrings(t *testing.T) {
	files := testXLSXFiles()
	files["xl/_rels/workbook.xml.rels"] = strings.ReplaceAll(files["xl/_rels/workbook.xml.rels"],
		`<Relationship Id="strings" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/sharedStrings" Target="strings.xml"/>`, "")
	files["xl/worksheets/budget.xml"] = `<worksheet><sheetData><row><c t="inlineStr"><is><t>Inline only</t></is></c></row></sheetData></worksheet>`
	delete(files, "xl/strings.xml")
	path := filepath.Join(t.TempDir(), "inline.xlsx")
	makeTestZip(t, path, files)
	book, err := readOfficeBook(path)
	if err != nil || !strings.Contains(book.HTML, "Row 1: cell 1: Inline only") {
		t.Fatalf("inline-only XLSX = %s, %v", book.HTML, err)
	}
}

func TestOfficePreviewRejectsInvalidParts(t *testing.T) {
	for _, tc := range []struct {
		name, extension string
		files           map[string]string
	}{
		{"missing DOCX body", ".docx", map[string]string{"word/document.xml": `<document/>`}},
		{"missing part", ".docx", map[string]string{"word/other.xml": `<document/>`}},
		{"malformed XML", ".docx", map[string]string{"word/document.xml": `<document><body><p>`}},
		{"wrong XML root", ".docx", map[string]string{"word/document.xml": `<other><body/></other>`}},
		{"unexpanded entity", ".docx", map[string]string{"word/document.xml": `<!DOCTYPE document [<!ENTITY x SYSTEM "file:///etc/passwd">]><document><body><p><t>&x;</t></p></body></document>`}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "invalid"+tc.extension)
			makeTestZip(t, path, tc.files)
			if _, err := readOfficeBook(path); err == nil {
				t.Fatal("invalid Office document was accepted")
			}
		})
	}
	for _, target := range []string{"../../../outside.xml", "https://example.invalid/data.xml", "file:///etc/passwd", `..\outside.xml`} {
		t.Run(target, func(t *testing.T) {
			files := testXLSXFiles()
			files["xl/_rels/workbook.xml.rels"] = strings.ReplaceAll(files["xl/_rels/workbook.xml.rels"], "worksheets/budget.xml", target)
			path := filepath.Join(t.TempDir(), "unsafe.xlsx")
			makeTestZip(t, path, files)
			if _, err := readOfficeBook(path); err == nil {
				t.Fatal("unsafe XLSX relationship was accepted")
			}
		})
	}
	files := testXLSXFiles()
	files["xl/strings.xml"] = `<sst/>`
	path := filepath.Join(t.TempDir(), "invalid-index.xlsx")
	makeTestZip(t, path, files)
	if _, err := readOfficeBook(path); err == nil {
		t.Fatal("out-of-range shared string index was accepted")
	}
}

func TestOfficePreviewLimitsExpansion(t *testing.T) {
	path := filepath.Join(t.TempDir(), "large.docx")
	makeTestZip(t, path, map[string]string{
		"word/document.xml": `<document><body><p><t>` + strings.Repeat("x", maxOfficeTextSize+1) + `</t></p></body></document>`,
	})
	if _, err := readOfficeBook(path); err == nil {
		t.Fatal("oversized DOCX text was accepted")
	}
	// A large shared value reused in many cells must be bounded during each
	// row's expansion, not only once after accumulating the whole worksheet.
	data := []byte(`<worksheet><sheetData><row>` + strings.Repeat(`<c t="s"><v>0</v></c>`, 3) + `</row></sheetData></worksheet>`)
	if _, err := extractXLSXSheetText(data, []string{strings.Repeat("x", maxOfficeTextSize/2)}); err == nil {
		t.Fatal("oversized expanded XLSX row was accepted")
	}
	archive := officeArchive{
		entries:   map[string]*zip.File{"large.xml": {FileHeader: zip.FileHeader{UncompressedSize64: 1024}}},
		remaining: 16,
	}
	if _, err := archive.read("large.xml"); err == nil {
		t.Fatal("part exceeding the total XML budget was accepted")
	}
}

func TestOfficeDocumentPreviewUsesPagedPDFAndCache(t *testing.T) {
	for extension, files := range map[string]map[string]string{".docx": testDOCXFiles(), ".xlsx": testXLSXFiles()} {
		t.Run(extension, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "document"+extension)
			makeTestZip(t, path, files)
			renderer := newPreviewDocumentRenderer(dir)
			document, err := renderer.renderSized(path, 120, 160)
			if err != nil {
				t.Fatal(err)
			}
			if document.Kind != strings.ToUpper(extension[1:]) || document.PDFPath == "" || document.pages() < 1 {
				t.Fatalf("unexpected Office preview: %#v", document)
			}
			pdf, err := os.ReadFile(document.PDFPath)
			if err != nil || !bytes.HasPrefix(pdf, []byte("%PDF-")) || bytes.Contains(pdf, []byte("/Subtype /Image")) {
				t.Fatalf("Office preview is not a text PDF: %v", err)
			}
			cached, err := renderer.renderSized(path, 120, 160)
			if err != nil || cached != document {
				t.Fatalf("Office preview was not cached: %v", err)
			}
			if !isRenderedDocument(path) || (&Browser{}).fileType(path) != "text" {
				t.Fatal("Office file was not classified as previewable text")
			}
		})
	}
}

func TestOfficePreviewEmitsPDFPage(t *testing.T) {
	for extension, files := range map[string]map[string]string{".docx": testDOCXFiles(), ".xlsx": testXLSXFiles()} {
		t.Run(extension, func(t *testing.T) {
			dir := t.TempDir()
			if extension == ".docx" {
				files["word/document.xml"] = `<document><body>` + strings.Repeat(
					`<p><r><t>A paragraph from a long document, with enough text for paginated preview navigation.</t></r></p>`, 100) + `</body></document>`
			} else {
				files["xl/worksheets/notes.xml"] = `<worksheet><sheetData>` + strings.Repeat(
					`<row><c t="inlineStr"><is><t>A worksheet row for paginated preview navigation.</t></is></c></row>`, 100) + `</sheetData></worksheet>`
			}
			makeTestZip(t, filepath.Join(dir, "document"+extension), files)
			browser := startBrowser(t, testEnv(), dir)
			browser.send("\x1b[B")
			waitContains(t, browser, strings.ToUpper(extension[1:])+"  1/", "Office first page title")
			waitContains(t, browser, " page 1", "Office PDF placement")
			if strings.Contains(browser.output(), "fit-contain page 1") {
				t.Fatal("Office preview requested a second scaling pass")
			}
			browser.send("]")
			waitContains(t, browser, strings.ToUpper(extension[1:])+"  2/", "Office next page title")
			waitContains(t, browser, " page 2", "Office next PDF page")
		})
	}
}

func TestOfficePreviewInvalidatesChangedZIPText(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "document.docx")
	files := testDOCXFiles()
	makeTestZip(t, path, files)
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	renderer := newPreviewDocumentRenderer(dir)
	first, err := renderer.renderSized(path, 120, 160)
	if err != nil {
		t.Fatal(err)
	}
	files["word/document.xml"] = strings.ReplaceAll(files["word/document.xml"], "Hello ", "Updated ")
	makeTestZip(t, path, files)
	if err := os.Chtimes(path, info.ModTime(), info.ModTime()); err != nil {
		t.Fatal(err)
	}
	updated, err := renderer.renderSized(path, 120, 160)
	if err != nil {
		t.Fatal(err)
	}
	if first.PDFPath == updated.PDFPath || len(renderer.docs) != 1 {
		t.Fatal("changed Office XML with unchanged mtime reused a stale preview")
	}
	if _, err := os.Stat(first.PDFPath); !os.IsNotExist(err) {
		t.Fatalf("superseded Office preview was not removed: %v", err)
	}
}
