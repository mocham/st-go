package stterminal

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"st-go/config"
)

type pdfSearchTestHooks struct {
	mockHooks
	requests []PDFSearchRequest
	options  ImageDecodeOptions
}

func (h *pdfSearchTestHooks) SearchPDF(request PDFSearchRequest) error {
	h.requests = append(h.requests, request)
	return nil
}
func (h *pdfSearchTestHooks) ImageDecode(_ []byte, options ImageDecodeOptions) (int, int, []Glyph, bool) {
	h.options = options
	return 1, 1, []Glyph{{U: ImageRune}}, true
}

func TestFixedPreviewPDFSearchProtocol(t *testing.T) {
	h := &pdfSearchTestHooks{}
	core := NewTerm(config.Default(), h)
	file := filepath.Join(t.TempDir(), "document.pdf")
	if err := os.WriteFile(file, []byte("%PDF-1.4\n"), 0600); err != nil {
		t.Fatal(err)
	}
	core.Twrite([]byte("\x1bPpdfsearch query 7 '"+file+"' 'café α'; pdfsearch chunk 7 3072; open '"+file+"' rect 2 2 8 8 fit-height page 2 pdfsearch 7 3; pdfsearch clear\x1b\\"), false)
	want := []PDFSearchRequest{{Token: 7, Path: file, Text: "café α"}, {Token: 7, Offset: 3072}, {}}
	if !reflect.DeepEqual(h.requests, want) {
		t.Fatalf("requests=%+v", h.requests)
	}
	if h.options.SearchToken != 7 || h.options.SearchMatch != 3 || h.options.Page != 1 || !h.options.FitHeight {
		t.Fatalf("paint options=%+v", h.options)
	}
	for _, command := range []string{"query 0 path text", "query 7 path ''", "query 7 path '" + strings.Repeat("x", 1025) + "'", "chunk 7 -1", "chunk 7 1048577", "query 7 path text extra", "clear extra"} {
		if err := core.dslPDFSearch(dslTokenize(command)); err == nil {
			t.Fatalf("accepted %q", command)
		}
	}
}
