package frontend

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"st-go/pdfmeta"
	"st-go/term"
)

func awaitPreviewSearch(t *testing.T, trm *Terminal, token int) pdfmeta.Search {
	t.Helper()
	deadline := time.Now().Add(4 * time.Second)
	for time.Now().Before(deadline) {
		trm.pdfSearch.mu.Lock()
		data := append([]byte(nil), trm.pdfSearch.data...)
		current := trm.pdfSearch.token
		trm.pdfSearch.mu.Unlock()
		if current == token && data != nil {
			var result pdfmeta.Search
			if err := json.Unmarshal(data, &result); err != nil {
				t.Fatal(err)
			}
			return result
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("PDF preview search did not settle")
	return pdfmeta.Search{}
}

func TestFixedPreviewPDFSearchAndHighlight(t *testing.T) {
	trm := regionTestTerminal(t)
	encoded := occurrenceTestPDF()
	path := filepath.Join(t.TempDir(), "occurrences.pdf")
	if err := os.WriteFile(path, encoded, 0600); err != nil {
		t.Fatal(err)
	}
	if err := trm.SearchPDF(term.PDFSearchRequest{Token: 1, Path: path, Text: "NEEDLE"}); err != nil {
		t.Fatal(err)
	}
	metadata := awaitPreviewSearch(t, trm, 1)
	if metadata.Error != "" || !reflect.DeepEqual(metadata.Pages, []int{1, 1, 2}) {
		t.Fatalf("search=%+v", metadata)
	}
	const width, height = 320, 240
	base := make([]uint32, width*height)
	for i := range base {
		base[i] = 0xffffffff
	}
	paint := func(token, match int, data []byte) []uint32 {
		pixels := append([]uint32(nil), base...)
		trm.paintPDFPreviewSearch(data, 0, width, height, pixels, term.ImageDecodeOptions{SearchToken: token, SearchMatch: match})
		return pixels
	}
	first, second := paint(1, 1, encoded), paint(1, 2, encoded)
	if reflect.DeepEqual(first, base) || reflect.DeepEqual(first, second) {
		t.Fatal("same-page occurrence did not change the highlight")
	}
	active, other := 0, 0
	for _, pixel := range first {
		if pixel == 0xffffaa2d {
			active++
		}
		if pixel == 0xffffeb64 {
			other++
		}
	}
	if active == 0 || other == 0 {
		t.Fatal("active and other occurrences did not receive separate colors")
	}
	if !reflect.DeepEqual(paint(2, 1, encoded), base) || !reflect.DeepEqual(paint(1, 1, append([]byte(nil), encoded[:len(encoded)-1]...)), base) {
		t.Fatal("stale token or different PDF inherited highlights")
	}
	// Verify the actual PDF -> glyph-atlas path, not just the overlay helper.
	_, _, glyphs, ok := trm.imageDecodePDF(encoded, term.ImageDecodeOptions{ViewCols: 80, ViewRows: 30, FitHeight: true, SearchToken: 1, SearchMatch: 1})
	if !ok {
		t.Fatal("searched fixed-page PDF did not decode")
	}
	tinted := false
	for _, glyph := range glyphs {
		for _, pixel := range trm.imageAtlas[int(glyph.Fg) : int(glyph.Fg)+trm.cw*trm.ch] {
			r, g, b := (pixel>>16)&255, (pixel>>8)&255, pixel&255
			if r > g && g > b {
				tinted = true
				break
			}
		}
	}
	if !tinted {
		t.Fatal("PDF highlights did not reach terminal image cells")
	}
	if err := trm.SearchPDF(term.PDFSearchRequest{}); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(paint(1, 1, encoded), base) {
		t.Fatal("clearing retained the previous highlight")
	}
}

func TestFixedPreviewPDFSearchLatestRequestAndChunks(t *testing.T) {
	trm := regionTestTerminal(t)
	path := filepath.Join(t.TempDir(), "occurrences.pdf")
	os.WriteFile(path, occurrenceTestPDF(), 0600)
	trm.SearchPDF(term.PDFSearchRequest{Token: 1, Path: path, Text: "absent"})
	trm.SearchPDF(term.PDFSearchRequest{Token: 2, Path: path, Text: "needle"})
	if result := awaitPreviewSearch(t, trm, 2); len(result.Pages) != 3 {
		t.Fatalf("latest result=%+v", result)
	}
	pages := make([]int, 4096)
	for i := range pages {
		pages[i] = 1
	}
	data, err := json.Marshal(pdfmeta.Search{Pages: pages, Truncated: true})
	if err != nil {
		t.Fatal(err)
	}
	trm.pdfSearch.mu.Lock()
	trm.pdfSearch.data = data
	trm.pdfSearch.mu.Unlock()
	var output bytes.Buffer
	trm.termCore.SetWriter(func(data []byte) { output.Write(data) })
	var received []byte
	for offset := 0; offset < len(data); {
		output.Reset()
		if err := trm.SearchPDF(term.PDFSearchRequest{Token: 2, Offset: offset}); err != nil {
			t.Fatal(err)
		}
		trm.flushRegionReplies()
		fields := strings.Fields(strings.TrimSuffix(strings.TrimPrefix(output.String(), "\x1bP"), "\x1b\\"))
		if len(fields) != 6 || fields[0] != "pdfsearch" || fields[1] != "data" {
			t.Fatalf("bad search chunk: %q", output.String())
		}
		chunk, err := base64.StdEncoding.DecodeString(fields[5])
		if err != nil || len(chunk) > pdfmeta.ChunkBytes {
			t.Fatal("unbounded search chunk")
		}
		if fields[3] != fmt.Sprint(offset) {
			t.Fatal("incorrect search offset")
		}
		received = append(received, chunk...)
		offset += len(chunk)
	}
	if !bytes.Equal(received, data) {
		t.Fatal("chunked search reply was corrupted")
	}
	trm.SearchPDF(term.PDFSearchRequest{Token: 3, Path: path + ".missing", Text: "needle"})
	if result := awaitPreviewSearch(t, trm, 3); result.Error == "" {
		t.Fatal("native search failure reported as no matches")
	}
}
