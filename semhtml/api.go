// Package semhtml renders a controlled semantic-HTML subset as a native PDF.
// It deliberately does not implement browser layout or a CSS cascade.
package semhtml

import (
	"crypto/sha256"
	"encoding/hex"
	"html/template"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"
)

const pdfFormatVersion = "semhtml-native-v1"

// Document is semantic HTML and the metadata written to its PDF.
type Document struct {
	HTML     string
	Title    string
	Author   string
	Modified time.Time
}

// PDFOptions contains optional page dimensions in millimetres. Zero values
// retain the renderer's A4 defaults.
type PDFOptions struct {
	PageWidth  float64
	PageHeight float64
}

// Renderer owns bounded, concurrency-safe PDF and formula caches.
type Renderer struct {
	once sync.Once
	pdf  *pdfRenderer
}

// NewRenderer constructs a renderer with embedded fonts and no host-runtime
// dependencies.
func NewRenderer() *Renderer {
	renderer := &Renderer{}
	renderer.ensureInitialized()
	return renderer
}

// RenderPDF renders semantic HTML with the default page size.
func (renderer *Renderer) RenderPDF(document Document) ([]byte, error) {
	return renderer.RenderPDFWithOptions(document, PDFOptions{})
}

// RenderPDFWithOptions renders semantic HTML with optional page dimensions.
func (renderer *Renderer) RenderPDFWithOptions(document Document, options PDFOptions) ([]byte, error) {
	if renderer == nil {
		renderer = NewRenderer()
	} else {
		renderer.ensureInitialized()
	}
	return renderer.pdf.render(document, template.HTML(document.HTML), PDFCacheKeyWithOptions(document, options), options)
}

func (renderer *Renderer) ensureInitialized() {
	renderer.once.Do(func() {
		renderer.pdf = newPDFRenderer()
	})
}

// PDFCacheKey returns the stable content key for a default-size rendering.
func PDFCacheKey(document Document) string {
	return PDFCacheKeyWithOptions(document, PDFOptions{})
}

// PDFCacheKeyWithOptions returns the stable content key for a rendering.
func PDFCacheKeyWithOptions(document Document, options PDFOptions) string {
	digest := sha256.New()
	for _, value := range []string{
		pdfFormatVersion,
		document.Title,
		document.Author,
		document.Modified.UTC().Format(time.RFC3339Nano),
		strconv.FormatFloat(options.PageWidth, 'g', -1, 64),
		strconv.FormatFloat(options.PageHeight, 'g', -1, 64),
		document.HTML,
	} {
		_, _ = digest.Write([]byte(value))
		_, _ = digest.Write([]byte{0})
	}
	return hex.EncodeToString(digest.Sum(nil))
}

func collapseWhitespace(value string) string {
	return strings.Join(strings.FieldsFunc(value, unicode.IsSpace), " ")
}
