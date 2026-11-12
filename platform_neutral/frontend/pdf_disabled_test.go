//go:build !backend_web && (!cgo || purego)

package frontend

import (
	"errors"
	"testing"
)

func TestMinimalPDFOperationsAreUnavailable(t *testing.T) {
	data := []byte("%PDF-1.4\n")
	if pdfPageCount(data) != 0 {
		t.Fatal("stub claimed PDF pages")
	}
	if _, _, ok := renderPDFPage(data, 0, 32, 32); ok {
		t.Fatal("stub claimed PDF rendering")
	}
	if pdf, err := openScrollPDF(data); pdf != nil || !errors.Is(err, errPDFUnavailable) {
		t.Fatalf("stub open: %v, %v", pdf, err)
	}
	var pdf *scrollPDF
	pdf.close()
	if _, err := pdf.metadata("text", 1); !errors.Is(err, errPDFUnavailable) {
		t.Fatal(err)
	}
	if _, err := pdf.pageSize(0); !errors.Is(err, errPDFUnavailable) {
		t.Fatal(err)
	}
	if _, _, err := pdf.matches(32, "query", func() bool { return true }); !errors.Is(err, errPDFUnavailable) {
		t.Fatal(err)
	}
}
