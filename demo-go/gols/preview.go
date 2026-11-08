package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/mocham/GoLS/latexpdf"
)

const maxPreviewDocumentSize = 32 << 20
const previewFontScale = 1.7

var pdfPageObject = regexp.MustCompile(`/Type\s*/Page\b`)

type renderedPreview struct {
	path       string
	pageCount  int
	mathErrors int
	pdf        bool
	toc        []previewTOCEntry
}

type previewTOCEntry struct {
	Title  string
	Number string
	Level  int
	Page   int
}

type documentPreviewer struct {
	cacheDir string
	renderer *latexpdf.Renderer
	cache    map[string]renderedPreview
}

func newDocumentPreviewer(cacheDir string) *documentPreviewer {
	return &documentPreviewer{
		cacheDir: cacheDir,
		renderer: latexpdf.NewRenderer(),
		cache:    make(map[string]renderedPreview),
	}
}

func (previewer *documentPreviewer) render(ctx context.Context, corpus *corpus, metadata documentMeta, pageWidth, pageHeight float64) (renderedPreview, error) {
	keySource := strings.Join([]string{
		corpus.file, fmt.Sprintf("%d", metadata.ID), metadata.SHA256,
		metadata.Title, fmt.Sprintf("%d", metadata.ModTimeNS),
		fmt.Sprintf("%.6f", pageWidth), fmt.Sprintf("%.6f", pageHeight),
		fmt.Sprintf("%g", previewFontScale),
	}, "\x00")
	digest := sha256.Sum256([]byte(keySource))
	key := hex.EncodeToString(digest[:])
	if cached, ok := previewer.cache[key]; ok {
		return cached, nil
	}
	document, err := corpus.document(ctx, metadata.ID)
	if err != nil {
		return renderedPreview{}, err
	}
	if len(document.Content) > maxPreviewDocumentSize {
		return renderedPreview{}, fmt.Errorf("document exceeds the %d MiB preview limit", maxPreviewDocumentSize>>20)
	}
	isTeX := strings.EqualFold(document.Extension, ".tex")
	isMarkdown := strings.EqualFold(document.Extension, ".md") || strings.EqualFold(document.Extension, ".markdown")
	if !isTeX && !isMarkdown {
		return previewer.materializeText(document, key)
	}

	source := document.Content
	if isTeX {
		source = corpus.expandedSource(ctx, document)
	} else {
		source = latexpdf.MarkdownToLaTeX(source)
	}
	title := strings.TrimSpace(document.Title)
	if title == "" {
		title = strings.TrimSuffix(document.Name, document.Extension)
	}
	if isTeX {
		if value, ok := latexpdf.FindCommandArgument(source, "title"); ok {
			if plain := strings.TrimSpace(latexpdf.PlainText(value)); plain != "" {
				title = plain
			}
		}
	}
	author := ""
	if isTeX {
		if value, ok := latexpdf.FindCommandArgument(source, "author"); ok {
			author = strings.TrimSpace(latexpdf.PlainText(value))
		}
	}
	modified := time.Unix(0, document.ModTimeNS)
	pdfResult, err := previewer.renderer.RenderPDFResultWithOptions(latexpdf.Document{
		Source: source, Title: title, Author: author, Modified: modified,
	}, latexpdf.PDFOptions{PageWidth: pageWidth, PageHeight: pageHeight, FontScale: previewFontScale})
	if err != nil {
		return renderedPreview{}, err
	}
	pdf := pdfResult.PDF
	pageCount := len(pdfPageObject.FindAll(pdf, -1))
	if pageCount == 0 {
		return renderedPreview{}, fmt.Errorf("latexpdf returned no pages")
	}
	pdfPath := filepath.Join(previewer.cacheDir, "document-"+key[:20]+".pdf")
	if err := os.WriteFile(pdfPath, pdf, 0o600); err != nil {
		return renderedPreview{}, err
	}
	result := renderedPreview{
		path: pdfPath, pageCount: pageCount, mathErrors: pdfResult.MathErrors, pdf: true,
	}
	for _, section := range pdfResult.Sections {
		if section.Page < 1 || section.Page > pageCount {
			continue
		}
		result.toc = append(result.toc, previewTOCEntry{
			Title: section.Title, Number: section.Number,
			Level: section.Level, Page: section.Page,
		})
	}
	previewer.cache[key] = result
	return result, nil
}

func (previewer *documentPreviewer) materializeText(document documentRecord, key string) (renderedPreview, error) {
	extension := strings.ToLower(document.Extension)
	if extension == "" || len(extension) > 12 {
		extension = ".txt"
	}
	textPath := filepath.Join(previewer.cacheDir, "document-"+key[:20]+extension)
	if err := os.WriteFile(textPath, []byte(document.Content), 0o600); err != nil {
		return renderedPreview{}, err
	}
	result := renderedPreview{path: textPath, pageCount: 1}
	previewer.cache[key] = result
	return result, nil
}
