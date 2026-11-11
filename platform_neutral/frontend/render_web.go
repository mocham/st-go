//go:build backend_web

package frontend

import (
	"fmt"
	"image"
	"st-go/limits"
	"st-go/pdfmeta"
	"sync"
)

// The web profile owns font bytes, not font faces or glyph bitmaps. Browser
// renderers consume the same configured font asset through the resource API.
var embeddedFont []byte
var fontMu sync.Mutex
var ft struct{ generation uint64 }

func loadFonts(path string, _ int) bool {
	if path != "" {
		data, err := limits.ReadFile(path)
		if err != nil {
			return false
		}
		embeddedFont = data
	}
	return len(embeddedFont) > 0
}

// Legacy pixel-only embedding entry points are unavailable in this profile.
// Live web surfaces always select the semantic path before reaching them.
func makeGlyph(r rune, fg, bg uint32, w, h, baseline int) (int, []uint32)       { return 0, nil }
func makeGlyphLocked(r rune, fg, bg uint32, w, h, baseline int) (int, []uint32) { return 0, nil }

type scrollPDF struct {
	renderedRows uint64
	cache        regionBitmap
}
type pdfMatch struct {
	page int
	box  image.Rectangle
}
type pdfMatches struct {
	query     string
	hits      []pdfMatch
	active    int
	truncated bool
}
type pdfPageSize struct{ width, height float64 }

func browserPDFError() error {
	return fmt.Errorf("PDF operation requires an attached browser renderer")
}
func openScrollPDF([]byte) (*scrollPDF, error) { return nil, browserPDFError() }
func (*scrollPDF) close()                      {}
func (*scrollPDF) metadata(string, int) (pdfmeta.Payload, error) {
	return pdfmeta.Payload{}, browserPDFError()
}
func (*scrollPDF) render(int, int, int, uint32) (regionBitmap, error) {
	return regionBitmap{}, browserPDFError()
}
func (*scrollPDF) renderScreens(int, int, int, uint32, int) (regionBitmap, error) {
	return regionBitmap{}, browserPDFError()
}
func (*scrollPDF) find(int, *regionSearch, func() bool) (int, int, error) {
	return 0, 0, browserPDFError()
}
func (*scrollPDF) pageSize(int) (pdfPageSize, error) { return pdfPageSize{}, browserPDFError() }
func (*scrollPDF) matches(int, string, func() bool) (*pdfMatches, []int, error) {
	return nil, nil, browserPDFError()
}
func renderPDFPage([]byte, int, int, int) ([]byte, int, bool)               { return nil, 0, false }
func renderPDFPageViewport([]byte, int, int, int, bool) ([]byte, int, bool) { return nil, 0, false }
func pdfPageCount([]byte) int                                               { return 0 }
