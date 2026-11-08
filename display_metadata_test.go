package main

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
	"unicode/utf16"
	"unicode/utf8"

	"st-go/pdfmeta"
	"st-go/term"
)

func metadataTestPDF(title string) []byte {
	stream := "BT /F1 12 Tf 10 150 Td (Selectable page one) Tj ET\n"
	return pdfTestObjects([]string{
		"<</Type/Catalog/Pages 2 0 R/Outlines 7 0 R/Names<</Dests<</Names[(chapter2)[4 0 R/Fit]]>>>>>>",
		"<</Type/Pages/Kids[3 0 R 4 0 R]/Count 2>>",
		"<</Type/Page/Parent 2 0 R/MediaBox[0 0 200 200]/Resources<</Font<</F1 6 0 R>>>>/Contents 5 0 R>>",
		"<</Type/Page/Parent 2 0 R/MediaBox[0 0 200 200]>>",
		fmt.Sprintf("<</Length %d>>\nstream\n%sendstream", len(stream), stream),
		"<</Type/Font/Subtype/Type1/BaseFont/Helvetica>>",
		"<</Type/Outlines/First 8 0 R/Last 9 0 R/Count 3>>",
		"<</Title(" + title + ")/Parent 7 0 R/Dest[3 0 R/Fit]/Next 9 0 R/First 10 0 R/Last 10 0 R/Count 1>>",
		"<</Title(External)/Parent 7 0 R/Prev 8 0 R/A<</S/URI/URI(https://example.invalid/never-open)>>>>",
		"<</Title<FEFF4E16754C>/Parent 8 0 R/Dest(chapter2)>>",
	})
}

func metadataActualTextPDF(text string) []byte {
	var actual strings.Builder
	actual.WriteString("FEFF")
	for _, unit := range utf16.Encode([]rune(text)) {
		fmt.Fprintf(&actual, "%04X", unit)
	}
	stream := "/Span <</ActualText <" + actual.String() + ">>> BDC BT /F1 12 Tf 10 150 Td (Unicode text) Tj ET EMC\n"
	return pdfTestObjects([]string{
		"<</Type/Catalog/Pages 2 0 R>>", "<</Type/Pages/Kids[3 0 R]/Count 1>>",
		"<</Type/Page/Parent 2 0 R/MediaBox[0 0 300 200]/Resources<</Font<</F1 5 0 R>>>>/Contents 4 0 R>>",
		fmt.Sprintf("<</Length %d>>\nstream\n%sendstream", len(stream), stream),
		"<</Type/Font/Subtype/Type1/BaseFont/Helvetica>>",
	})
}

func unavailablePDFGconv(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	// Override the system's UTF-16 conversions with unavailable modules. This
	// reproduces a minimal host without requiring root or changing host files.
	modules := "module UTF-16LE// INTERNAL unavailable-utf16 1\nmodule INTERNAL UTF-16LE// unavailable-utf16 1\n" +
		"module UTF-16BE// INTERNAL unavailable-utf16 1\nmodule INTERNAL UTF-16BE// unavailable-utf16 1\n"
	if err := os.WriteFile(filepath.Join(dir, "gconv-modules"), []byte(modules), 0600); err != nil {
		t.Fatal(err)
	}
	return dir
}

// Static binaries must not depend on the host's dynamically loaded iconv
// modules: page geometry works even when the C++ UTF-16 round trip returns empty.
func TestNativePDFTextWithoutHostModules(t *testing.T) {
	if os.Getenv("ST_TEST_PDF_NO_HOST_MODULES") == "1" {
		doc, err := openScrollPDF(metadataActualTextPDF("LaTeX café 世界 😀 \x00 end"))
		if err != nil {
			t.Fatal(err)
		}
		defer doc.close()
		bitmap, err := doc.render(300, 200, 0, 0xffffffff)
		if err != nil || len(bitmap.pageTops) != 2 {
			t.Fatalf("page geometry/rendering failed: %v", err)
		}
		result, err := doc.metadata("text", 1)
		if err != nil || !utf8.ValidString(result.Text) || !strings.Contains(result.Text, "LaTeX café 世界 😀 \x00 end") {
			t.Fatalf("text without host conversion modules = %q, %v", result.Text, err)
		}
		for _, needle := range []string{"LaTeX", "世界", "😀", "end"} {
			page, _, err := doc.find(300, &regionSearch{text: needle, direction: 1}, func() bool { return true })
			if err != nil || page != 1 {
				t.Fatalf("search %q without host modules = page %d, %v", needle, page, err)
			}
			matches, _, err := doc.matches(300, needle, func() bool { return true })
			if err != nil || matches == nil || len(matches.hits) != 1 {
				t.Fatalf("occurrence search %q without host modules: %+v, %v", needle, matches, err)
			}
		}
		return
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	child := exec.Command(executable, "-test.run=^TestNativePDFTextWithoutHostModules$", "-test.v")
	child.Env = append(envWithout(os.Environ(), "GCONV_PATH"), "ST_TEST_PDF_NO_HOST_MODULES=1", "GCONV_PATH="+unavailablePDFGconv(t))
	if output, err := child.CombinedOutput(); err != nil {
		t.Fatalf("isolated PDF text extraction: %v\n%s", err, output)
	}
}

func TestNativePDFPageTextUTF8Budget(t *testing.T) {
	text := "L" + strings.Repeat("😀", pdfmeta.TextBytes/4+2) + "TAIL"
	doc, err := openScrollPDF(metadataActualTextPDF(text))
	if err != nil {
		t.Fatal(err)
	}
	defer doc.close()
	result, err := doc.metadata("text", 1)
	if err != nil || !result.Truncated || !utf8.ValidString(result.Text) || len(result.Text) > pdfmeta.TextBytes ||
		len(result.Text) < pdfmeta.TextBytes-4 || !strings.HasPrefix(text, result.Text) {
		t.Fatalf("bounded UTF-8 output: bytes=%d, truncated=%v, valid=%v, %v", len(result.Text), result.Truncated, utf8.ValidString(result.Text), err)
	}
}

func TestNativePDFOutlineAndPageText(t *testing.T) {
	doc, err := openScrollPDF(metadataTestPDF("Opening"))
	if err != nil {
		t.Fatal(err)
	}
	defer doc.close()
	result, err := doc.metadata("toc", 0)
	if err != nil || len(result.TOC) != 3 {
		t.Fatalf("outline = %+v, %v", result, err)
	}
	for index, want := range []pdfmeta.Entry{{Title: "Opening", Page: 1}, {Title: "世界", Page: 2, Level: 1}, {Title: "External"}} {
		if result.TOC[index] != want {
			t.Fatalf("outline entry = %+v, want %+v", result.TOC[index], want)
		}
	}
	text, err := doc.metadata("text", 1)
	if err != nil || !strings.Contains(text.Text, "Selectable page one") {
		t.Fatalf("page text = %+v, %v", text, err)
	}
	blank, err := doc.metadata("text", 2)
	if err != nil || strings.TrimSpace(blank.Text) != "" {
		t.Fatalf("blank page text = %+v, %v", blank, err)
	}
	if _, err := doc.metadata("text", 3); err == nil {
		t.Fatal("out-of-range page accepted")
	}
}

func TestRegionMetadataChunksSurviveLayoutChanges(t *testing.T) {
	trm := regionTestTerminal(t)
	file := filepath.Join(t.TempDir(), "outline.pdf")
	if err := os.WriteFile(file, metadataTestPDF(strings.Repeat("x", 4000)), 0600); err != nil {
		t.Fatal(err)
	}
	var replies bytes.Buffer
	trm.termCore.SetWriter(func(data []byte) { replies.Write(data) })
	trm.writeTerminalOutput(trm.termCore, []byte("\x1bPregion stack 1\x1b\\"))
	trm.mu.Lock()
	err := trm.OpenRegion(term.SecondaryRegion, term.RegionImage{Path: file, Continuous: true, Options: term.ImageDecodeOptions{FitWidth: true}})
	trm.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	var encoded []byte
	chunks := 0
	for {
		trm.mu.Lock()
		err = trm.QueryRegionDocument(term.SecondaryRegion, term.RegionDocumentQuery{Kind: "toc", Token: 77, Offset: len(encoded)})
		trm.mu.Unlock()
		if err != nil {
			t.Fatal(err)
		}
		if chunks == 0 {
			trm.writeTerminalOutput(trm.termCore, []byte("\x1bPregion split 60%\x1b\\"))
		}
		deadline := time.Now().Add(4 * time.Second)
		var fields []string
		for time.Now().Before(deadline) {
			trm.mu.Lock()
			trm.flushRegionReplies()
			text := replies.String()
			replies.Reset()
			trm.mu.Unlock()
			start := strings.Index(text, "\x1bPregion secondary data ")
			if start >= 0 {
				fields = strings.Fields(strings.TrimSuffix(text[start+2:], "\x1b\\"))
				break
			}
			time.Sleep(time.Millisecond)
		}
		if len(fields) != 9 || fields[3] != "77" {
			t.Fatalf("metadata reply fields = %q", fields)
		}
		chunk, err := base64.StdEncoding.DecodeString(fields[8])
		if err != nil || len(chunk) > pdfmeta.ChunkBytes {
			t.Fatalf("invalid metadata chunk: %v", err)
		}
		offset, _ := strconv.Atoi(fields[6])
		next, _ := strconv.Atoi(fields[7])
		if offset != len(encoded) {
			t.Fatal("out-of-order metadata chunk")
		}
		encoded = append(encoded, chunk...)
		chunks++
		if next == -1 {
			break
		}
		if next != len(encoded) {
			t.Fatal("invalid continuation offset")
		}
	}
	var payload pdfmeta.Payload
	if err := json.Unmarshal(encoded, &payload); err != nil || len(payload.TOC) != 3 || chunks < 2 {
		t.Fatalf("chunked outline = %+v, chunks=%d, %v", payload, chunks, err)
	}
}
