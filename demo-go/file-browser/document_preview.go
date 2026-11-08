package main

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/mocham/GoLS/latexpdf"
	"st-go/semhtml"
)

const (
	documentPreviewVersion = "latexpdf-document-v11-auxiliary-bibliography"
	maxPreviewDocumentSize = 12 << 20
	previewFontScale       = 1.7
	maxPreviewVariants     = 4
	maxDocumentPreviews    = 32
)

var pdfPageObject = regexp.MustCompile(`/Type\s*/Page\b`)

type documentPreview struct {
	Pages      []string
	PDFPath    string
	PageCount  int
	MathErrors int
	Kind       string
}

type previewDocumentRenderer struct {
	cacheDir string
	docs     map[string]*documentPreview
	sources  map[string]*documentPreviewSource
	order    []string
	latex    *latexpdf.Renderer
	semantic *semhtml.Renderer
}

type documentPreviewSource struct {
	revision string
	keys     []string
}

func newPreviewDocumentRenderer(cacheDir string) *previewDocumentRenderer {
	return &previewDocumentRenderer{
		cacheDir: cacheDir,
		docs:     make(map[string]*documentPreview),
		sources:  make(map[string]*documentPreviewSource),
		latex:    latexpdf.NewRenderer(),
		semantic: semhtml.NewRenderer(),
	}
}

func (document *documentPreview) pages() int {
	if document.PDFPath != "" {
		return document.PageCount
	}
	return len(document.Pages)
}

func (document *documentPreview) pagePath(page int) (string, bool) {
	if document.PDFPath != "" {
		return document.PDFPath, true
	}
	if page < 1 || page > len(document.Pages) {
		return "", false
	}
	return document.Pages[page-1], false
}

func isRenderedDocument(path string) bool {
	return hasSuffixAny(strings.ToLower(path), ".tex", ".md", ".markdown", ".epub", ".html", ".htm", ".xhtml", ".doc", ".docx", ".xlsx")
}

func (renderer *previewDocumentRenderer) render(path string) (*documentPreview, error) {
	return renderer.renderSized(path, 0, 0)
}

func (renderer *previewDocumentRenderer) renderSized(path string, pageWidth, pageHeight float64) (*documentPreview, error) {
	extension := strings.ToLower(filepath.Ext(path))
	switch extension {
	case ".epub":
		return renderer.renderEPUBSized(path, pageWidth, pageHeight)
	case ".html", ".htm", ".xhtml":
		return renderer.renderHTMLSized(path, pageWidth, pageHeight)
	case ".docx", ".xlsx":
		return renderer.renderOfficeSized(path, pageWidth, pageHeight)
	case ".doc":
		return renderer.renderDOCSized(path, pageWidth, pageHeight)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	if len(data) > maxPreviewDocumentSize {
		return nil, fmt.Errorf("document is too large to preview")
	}
	kind := "Markdown"
	source := string(data)
	if extension == ".tex" {
		data, err = expandLocalTeXDocument(path, data, maxPreviewDocumentSize)
		if err != nil {
			return nil, err
		}
		source = string(data)
		kind = "TeX"
	} else {
		source = latexpdf.MarkdownToLaTeX(source)
	}

	modified := fileModificationTime(path)
	logicalPath := renderer.prepareSource(path, previewCacheDigest(
		documentPreviewVersion, extension, modified.UTC().Format(time.RFC3339Nano), string(data),
	))
	key := previewCacheDigest(
		documentPreviewVersion, logicalPath, extension,
		fmt.Sprintf("%.6f", pageWidth), fmt.Sprintf("%.6f", pageHeight),
		fmt.Sprintf("%g", previewFontScale), modified.UTC().Format(time.RFC3339Nano), string(data),
	)
	if cached, ok := renderer.docs[key]; ok {
		renderer.touchSourcePreview(key)
		return cached, nil
	}
	result, err := renderer.renderPDFDocument(path, source, key, kind, modified, pageWidth, pageHeight)
	if err != nil {
		return nil, err
	}
	renderer.storeSourcePreview(logicalPath, key, result)
	return result, nil
}

func (renderer *previewDocumentRenderer) renderPDFDocument(path, source, key, kind string, modified time.Time, pageWidth, pageHeight float64) (*documentPreview, error) {
	title := strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
	if kind == "TeX" {
		if value, ok := latexpdf.FindCommandArgument(source, "title"); ok {
			if value = strings.TrimSpace(latexpdf.PlainText(value)); value != "" {
				title = value
			}
		}
	}
	author := ""
	if value, ok := latexpdf.FindCommandArgument(source, "author"); ok {
		author = strings.TrimSpace(latexpdf.PlainText(value))
	}
	rendered, err := renderer.latex.RenderPDFResultWithOptions(latexpdf.Document{
		Source: source, Title: title, Author: author, Modified: modified,
	}, latexpdf.PDFOptions{PageWidth: pageWidth, PageHeight: pageHeight, FontScale: previewFontScale})
	if err != nil {
		return nil, err
	}
	pageCount := len(pdfPageObject.FindAll(rendered.PDF, -1))
	if pageCount == 0 {
		return nil, fmt.Errorf("latexpdf returned a PDF with no pages")
	}
	pdfPath := filepath.Join(renderer.cacheDir, fmt.Sprintf("document-%s.pdf", key[:16]))
	if err := os.WriteFile(pdfPath, rendered.PDF, 0o600); err != nil {
		return nil, err
	}
	result := &documentPreview{
		PDFPath: pdfPath, PageCount: pageCount, MathErrors: rendered.MathErrors, Kind: kind,
	}
	return result, nil
}

func previewCacheDigest(values ...string) string {
	digest := sha256.New()
	for _, value := range values {
		_, _ = digest.Write([]byte(value))
		_, _ = digest.Write([]byte{0})
	}
	return hex.EncodeToString(digest.Sum(nil))
}

func previewLogicalPath(path string) string {
	if absolute, err := filepath.Abs(path); err == nil {
		return filepath.Clean(absolute)
	}
	return filepath.Clean(path)
}

func (renderer *previewDocumentRenderer) prepareSource(path, revision string) string {
	logicalPath := previewLogicalPath(path)
	if renderer.docs == nil {
		renderer.docs = make(map[string]*documentPreview)
	}
	if renderer.sources == nil {
		renderer.sources = make(map[string]*documentPreviewSource)
	}
	if source := renderer.sources[logicalPath]; source != nil {
		if source.revision == revision {
			return logicalPath
		}
		for _, key := range append([]string(nil), source.keys...) {
			renderer.removeSourcePreview(key)
		}
		// Drop stale PDF/HTML/formula bytes held behind content keys. Valid
		// on-disk variants for other sources remain available through docs.
		renderer.latex = latexpdf.NewRenderer()
		renderer.semantic = semhtml.NewRenderer()
	}
	renderer.sources[logicalPath] = &documentPreviewSource{revision: revision}
	return logicalPath
}

func (renderer *previewDocumentRenderer) storeSourcePreview(logicalPath, key string, preview *documentPreview) {
	renderer.docs[key] = preview
	source := renderer.sources[logicalPath]
	if source == nil {
		source = &documentPreviewSource{}
		renderer.sources[logicalPath] = source
	}
	for _, existing := range source.keys {
		if existing == key {
			renderer.touchSourcePreview(key)
			return
		}
	}
	source.keys = append(source.keys, key)
	for len(source.keys) > maxPreviewVariants {
		oldest := source.keys[0]
		source.keys = source.keys[1:]
		renderer.removeSourcePreview(oldest)
	}
	renderer.touchSourcePreview(key)
	for len(renderer.order) > maxDocumentPreviews {
		renderer.removeSourcePreview(renderer.order[0])
	}
}

func (renderer *previewDocumentRenderer) removeSourcePreview(key string) {
	preview := renderer.docs[key]
	delete(renderer.docs, key)
	for logicalPath, source := range renderer.sources {
		for index, sourceKey := range source.keys {
			if sourceKey == key {
				source.keys = append(source.keys[:index], source.keys[index+1:]...)
				break
			}
		}
		if len(source.keys) == 0 {
			delete(renderer.sources, logicalPath)
		}
	}
	for index, cachedKey := range renderer.order {
		if cachedKey == key {
			renderer.order = append(renderer.order[:index], renderer.order[index+1:]...)
			break
		}
	}
	if preview == nil {
		return
	}
	if preview.PDFPath != "" {
		_ = os.Remove(preview.PDFPath)
	}
	for _, page := range preview.Pages {
		_ = os.Remove(page)
	}
}

func (renderer *previewDocumentRenderer) touchSourcePreview(key string) {
	for index, cachedKey := range renderer.order {
		if cachedKey == key {
			renderer.order = append(renderer.order[:index], renderer.order[index+1:]...)
			break
		}
	}
	renderer.order = append(renderer.order, key)
}
