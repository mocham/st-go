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
	latex    *latexpdf.Renderer
	semantic *semhtml.Renderer
}

func newPreviewDocumentRenderer(cacheDir string) *previewDocumentRenderer {
	return &previewDocumentRenderer{
		cacheDir: cacheDir,
		docs:     make(map[string]*documentPreview),
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
	return hasSuffixAny(strings.ToLower(path), ".tex", ".md", ".markdown", ".epub", ".html", ".htm", ".xhtml")
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

	version := fmt.Sprintf("%s\x00%s\x00%.6f\x00%.6f\x00%g\x00", documentPreviewVersion, extension, pageWidth, pageHeight, previewFontScale)
	digest := sha256.Sum256(append([]byte(version), data...))
	key := hex.EncodeToString(digest[:])
	if cached, ok := renderer.docs[key]; ok {
		return cached, nil
	}
	return renderer.renderPDFDocument(path, source, key, kind, pageWidth, pageHeight)
}

func (renderer *previewDocumentRenderer) renderPDFDocument(path, source, key, kind string, pageWidth, pageHeight float64) (*documentPreview, error) {
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
	modified := time.Time{}
	if info, err := os.Stat(path); err == nil {
		modified = info.ModTime()
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
	renderer.docs[key] = result
	return result, nil
}
