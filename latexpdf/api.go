// Package latexpdf renders a practical subset of LaTeX to semantic HTML and
// native, vector-math PDF without invoking TeX, a browser, or another process.
package latexpdf

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

const (
	// DefaultRenderPageBytes is the default source-size target used by
	// PreparePages when paginating HTML views.
	DefaultRenderPageBytes = defaultRenderPageBytes
	pdfFormatVersion       = "latexpdf-native-v13-red-unknown-commands"
)

// Document contains an expanded LaTeX source document and its PDF metadata.
// Callers are responsible for resolving \input and \include directives before
// rendering.
type Document struct {
	Source   string
	Title    string
	Author   string
	Modified time.Time
}

// PDFOptions contains optional PDF layout overrides. Zero page dimensions
// preserve the corresponding A4 defaults. Dimensions are in millimetres.
type PDFOptions struct {
	PageWidth  float64
	PageHeight float64
	// Compact uses narrow preview padding and omits generated title/footer
	// furniture. Titles explicitly present in the source still render.
	Compact bool
}

// Section describes one rendered section heading.
type Section struct {
	ID     string
	Title  string
	Number string
	Level  int
	Page   int
}

type documentSection = Section

// Result is the semantic HTML representation used by both browser and PDF
// rendering.
type Result struct {
	Body                 template.HTML
	MathErrors           int
	MathErrorExpressions []string
	Sections             []Section
}

type renderedLatex = Result

// PDFResult contains PDF bytes and the diagnostics/navigation metadata from
// the same render. Section page numbers refer to pages in PDF.
type PDFResult struct {
	PDF                  []byte
	MathErrors           int
	MathErrorExpressions []string
	Sections             []Section
}

// SourcePage is one structurally paginated LaTeX body fragment.
type SourcePage struct {
	Source    string
	Oversized bool
}

type latexSourcePage = SourcePage

// Renderer owns concurrency-safe in-memory HTML, formula, and PDF caches.
// A Renderer may be reused by multiple goroutines.
type Renderer struct {
	once  sync.Once
	latex *latexRenderer
	pdf   *pdfRenderer
}

// NewRenderer constructs a reusable renderer with no host-runtime
// dependencies.
func NewRenderer() *Renderer {
	renderer := &Renderer{}
	renderer.ensureInitialized()
	return renderer
}

// EmbeddedFallbackFont returns the immutable embedded CJK fallback face used
// by the PDF renderer. It lets sibling renderers share the same font payload
// without embedding a second copy. Callers must not modify the returned bytes.
func EmbeddedFallbackFont() []byte { return pdfFontBytes }

// SupplementTableMacros adds generated table definitions when source invokes
// one or more of them without defining them itself. Standalone fragments are
// wrapped in a document environment so the definitions remain declarations
// rather than being rendered as an extra copy of every table.
func SupplementTableMacros(source, definitions string) string {
	return supplementTableMacros(source, definitions)
}

// RenderHTML renders a complete LaTeX source document. cacheKey may be empty;
// when supplied it should uniquely identify source.
func (renderer *Renderer) RenderHTML(source, cacheKey string) Result {
	renderer.ensureInitialized()
	if cacheKey == "" {
		cacheKey = sourceCacheKey(source)
	}
	return renderer.latex.render(source, cacheKey)
}

// RenderPreparedHTML renders one body page against the complete preprocessed
// document so declarations, counters, labels, and citations remain global.
func (renderer *Renderer) RenderPreparedHTML(prepared, body, cacheKey string) Result {
	renderer.ensureInitialized()
	if cacheKey == "" {
		cacheKey = sourceCacheKey(prepared + "\x00" + body)
	}
	return renderer.latex.renderPrepared(prepared, body, cacheKey)
}

// RenderPDF runs the complete LaTeX-to-HTML-to-PDF pipeline. Mathematics is
// emitted as PDF vector paths and ordinary text remains selectable.
func (renderer *Renderer) RenderPDF(document Document) ([]byte, error) {
	result, err := renderer.RenderPDFResult(document)
	return result.PDF, err
}

// RenderPDFWithOptions renders document with optional page layout overrides.
// RenderPDF remains the compatibility entry point for default A4 output.
func (renderer *Renderer) RenderPDFWithOptions(document Document, options PDFOptions) ([]byte, error) {
	result, err := renderer.RenderPDFResultWithOptions(document, options)
	return result.PDF, err
}

// RenderPDFResult runs the complete pipeline and returns PDF bytes together
// with math diagnostics and section destinations.
func (renderer *Renderer) RenderPDFResult(document Document) (PDFResult, error) {
	return renderer.RenderPDFResultWithOptions(document, PDFOptions{})
}

// RenderPDFResultWithOptions is RenderPDFResult with optional page dimensions.
func (renderer *Renderer) RenderPDFResultWithOptions(document Document, options PDFOptions) (PDFResult, error) {
	renderer.ensureInitialized()
	key := PDFCacheKeyWithOptions(document, options)
	rendered := renderer.RenderHTML(document.Source, "pdf-html-"+sourceCacheKey(document.Source))
	pdf, err := renderer.pdf.render(document, rendered.Body, rendered.Sections, key, options)
	if err != nil {
		return PDFResult{}, err
	}
	sections := append([]Section(nil), rendered.Sections...)
	sectionPages := renderer.pdf.pagesForSections(key)
	for index := range sections {
		sections[index].Page = sectionPages[sections[index].ID]
	}
	return PDFResult{
		PDF:                  pdf,
		MathErrors:           rendered.MathErrors,
		MathErrorExpressions: append([]string(nil), rendered.MathErrorExpressions...),
		Sections:             sections,
	}, nil
}

// PDFCacheKey returns the stable content key for the PDF bytes produced from
// document. Persistent caches may use this key without first rendering HTML.
func PDFCacheKey(document Document) string {
	return PDFCacheKeyWithOptions(document, PDFOptions{})
}

// PDFCacheKeyWithOptions returns the stable content key for PDF bytes rendered
// with options.
func PDFCacheKeyWithOptions(document Document, options PDFOptions) string {
	digest := sha256.New()
	for _, value := range []string{
		pdfFormatVersion,
		document.Title,
		document.Author,
		document.Modified.UTC().Format(time.RFC3339Nano),
		strconv.FormatFloat(options.PageWidth, 'g', -1, 64),
		strconv.FormatFloat(options.PageHeight, 'g', -1, 64),
		strconv.FormatBool(options.Compact),
		document.Source,
	} {
		_, _ = digest.Write([]byte(value))
		_, _ = digest.Write([]byte{0})
	}
	return hex.EncodeToString(digest.Sum(nil))
}

func (renderer *Renderer) ensureInitialized() {
	renderer.once.Do(func() {
		renderer.latex = newLatexRenderer()
		renderer.pdf = newPDFRenderer()
	})
}

func sourceCacheKey(source string) string {
	digest := sha256.Sum256([]byte(source))
	return hex.EncodeToString(digest[:])
}

// PreparePages preprocesses source and paginates its body at structural
// boundaries for HTML presentation.
func PreparePages(source string, byteLimit int) (string, []SourcePage) {
	source = normalizeLatexSourceEncoding(source)
	return prepareLatexPages(source, byteLimit)
}

// DocumentSections returns the section navigation metadata for pages.
func DocumentSections(pages []SourcePage) []Section {
	return latexDocumentSections(pages)
}

// DocumentType returns a document-type directive from the source preamble.
func DocumentType(source string) string {
	return latexDocumentType(normalizeLatexSourceEncoding(source))
}

// PlainText reduces a LaTeX text fragment to displayable plain text.
func PlainText(source string) string { return texFragmentToPlainText(source) }

// DocumentPlainText extracts searchable plain text from a LaTeX document.
func DocumentPlainText(source string) string { return latexToPlainText(source) }

// FindCommandArgument returns the first braced argument of a named command.
func FindCommandArgument(source, name string) (string, bool) {
	return findCommandArgument(normalizeLatexSourceEncoding(source), name)
}

// CommandBoundary reports whether position ends a TeX command name.
func CommandBoundary(source string, position int) bool {
	return commandBoundary(source, position)
}

// StripComments removes ordinary LaTeX comments while preserving escaped
// percent signs.
func StripComments(source string) string { return stripLatexComments(source) }

// ParseBraced parses a balanced braced argument beginning at start.
func ParseBraced(source string, start int) (string, int, bool) {
	return parseBraced(source, start)
}

// SkipSpace advances over TeX whitespace beginning at position.
func SkipSpace(source string, position int) int { return skipTeXSpace(source, position) }

func collapseWhitespace(value string) string {
	return strings.Join(strings.FieldsFunc(value, unicode.IsSpace), " ")
}
