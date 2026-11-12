//go:build backend_web || !cgo || purego

package frontend

import (
	"errors"
	"st-go/pdfmeta"
)

var errPDFUnavailable = errors.New("PDF operation requires a native PDF decoder or an attached browser renderer")

type scrollPDF struct {
	renderedRows uint64
	cache        regionBitmap
}

func openScrollPDF([]byte) (*scrollPDF, error) { return nil, errPDFUnavailable }
func (*scrollPDF) close()                      {}
func (*scrollPDF) metadata(string, int) (pdfmeta.Payload, error) {
	return pdfmeta.Payload{}, errPDFUnavailable
}
func (*scrollPDF) render(int, int, int, uint32) (regionBitmap, error) {
	return regionBitmap{}, errPDFUnavailable
}
func (*scrollPDF) renderScreens(int, int, int, uint32, int) (regionBitmap, error) {
	return regionBitmap{}, errPDFUnavailable
}
func (*scrollPDF) find(int, *regionSearch, func() bool) (int, int, error) {
	return 0, 0, errPDFUnavailable
}
func (*scrollPDF) pageSize(int) (pdfPageSize, error) { return pdfPageSize{}, errPDFUnavailable }
func (*scrollPDF) matches(int, string, func() bool) (*pdfMatches, []int, error) {
	return nil, nil, errPDFUnavailable
}
func renderPDFPage([]byte, int, int, int) ([]byte, int, bool)               { return nil, 0, false }
func renderPDFPageViewport([]byte, int, int, int, bool) ([]byte, int, bool) { return nil, 0, false }
func pdfPageCount([]byte) int                                               { return 0 }
