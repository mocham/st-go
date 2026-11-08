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
const maxPreviewVariants = 4
const maxDocumentPreviews = 32
const previewCacheVersion = "gols-preview-v2-expanded-source"

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
	sources  map[previewSourceID]*previewSourceCache
	order    []string
}

type previewSourceID struct {
	corpus string
	id     int64
}

type previewSourceCache struct {
	revision string
	keys     []string
}

func newDocumentPreviewer(cacheDir string) *documentPreviewer {
	return &documentPreviewer{
		cacheDir: cacheDir,
		renderer: latexpdf.NewRenderer(),
		cache:    make(map[string]renderedPreview),
		sources:  make(map[previewSourceID]*previewSourceCache),
	}
}

func (previewer *documentPreviewer) render(ctx context.Context, corpus *corpus, metadata documentMeta, pageWidth, pageHeight float64) (renderedPreview, error) {
	document, err := corpus.document(ctx, metadata.ID)
	if err != nil {
		return renderedPreview{}, err
	}
	if len(document.Content) > maxPreviewDocumentSize {
		return renderedPreview{}, fmt.Errorf("document exceeds the %d MiB preview limit", maxPreviewDocumentSize>>20)
	}
	isTeX := strings.EqualFold(document.Extension, ".tex")
	isMarkdown := strings.EqualFold(document.Extension, ".md") || strings.EqualFold(document.Extension, ".markdown")
	source := document.Content
	if isTeX {
		source = corpus.expandedSource(ctx, document)
	} else if isMarkdown {
		source = latexpdf.MarkdownToLaTeX(source)
	}
	if len(source) > maxPreviewDocumentSize {
		return renderedPreview{}, fmt.Errorf("expanded document exceeds the %d MiB preview limit", maxPreviewDocumentSize>>20)
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
	sourceID := previewSourceID{corpus: corpus.file, id: document.ID}
	revision := previewDigest(
		previewCacheVersion, document.Path, document.Extension, title, author,
		fmt.Sprintf("%d", document.ModTimeNS), document.SHA256, source,
	)
	previewer.prepareSource(sourceID, revision)
	key := previewDigest(
		revision, corpus.file, fmt.Sprintf("%d", document.ID),
		fmt.Sprintf("%.6f", pageWidth), fmt.Sprintf("%.6f", pageHeight),
		fmt.Sprintf("%g", previewFontScale),
	)
	if cached, ok := previewer.cache[key]; ok {
		previewer.touchPreview(key)
		return cached, nil
	}
	if !isTeX && !isMarkdown {
		result, materializeErr := previewer.materializeText(document, key)
		if materializeErr == nil {
			previewer.storeSourcePreview(sourceID, key, result)
		}
		return result, materializeErr
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
	previewer.storeSourcePreview(sourceID, key, result)
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
	return result, nil
}

func previewDigest(values ...string) string {
	digest := sha256.New()
	for _, value := range values {
		_, _ = digest.Write([]byte(value))
		_, _ = digest.Write([]byte{0})
	}
	return hex.EncodeToString(digest.Sum(nil))
}

func (previewer *documentPreviewer) prepareSource(sourceID previewSourceID, revision string) {
	if previewer.cache == nil {
		previewer.cache = make(map[string]renderedPreview)
	}
	if previewer.sources == nil {
		previewer.sources = make(map[previewSourceID]*previewSourceCache)
	}
	if source := previewer.sources[sourceID]; source != nil {
		if source.revision == revision {
			return
		}
		for _, key := range append([]string(nil), source.keys...) {
			previewer.removePreview(key)
		}
		previewer.renderer = latexpdf.NewRenderer()
	}
	previewer.sources[sourceID] = &previewSourceCache{revision: revision}
}

func (previewer *documentPreviewer) storeSourcePreview(sourceID previewSourceID, key string, preview renderedPreview) {
	previewer.cache[key] = preview
	source := previewer.sources[sourceID]
	if source == nil {
		source = &previewSourceCache{}
		previewer.sources[sourceID] = source
	}
	for _, existing := range source.keys {
		if existing == key {
			previewer.touchPreview(key)
			return
		}
	}
	source.keys = append(source.keys, key)
	for len(source.keys) > maxPreviewVariants {
		oldest := source.keys[0]
		source.keys = source.keys[1:]
		previewer.removePreview(oldest)
	}
	previewer.touchPreview(key)
	for len(previewer.order) > maxDocumentPreviews {
		previewer.removePreview(previewer.order[0])
	}
}

func (previewer *documentPreviewer) removePreview(key string) {
	preview, ok := previewer.cache[key]
	delete(previewer.cache, key)
	for sourceID, source := range previewer.sources {
		for index, sourceKey := range source.keys {
			if sourceKey == key {
				source.keys = append(source.keys[:index], source.keys[index+1:]...)
				break
			}
		}
		if len(source.keys) == 0 {
			delete(previewer.sources, sourceID)
		}
	}
	for index, cachedKey := range previewer.order {
		if cachedKey == key {
			previewer.order = append(previewer.order[:index], previewer.order[index+1:]...)
			break
		}
	}
	if ok && preview.path != "" {
		_ = os.Remove(preview.path)
	}
}

func (previewer *documentPreviewer) touchPreview(key string) {
	for index, cachedKey := range previewer.order {
		if cachedKey == key {
			previewer.order = append(previewer.order[:index], previewer.order[index+1:]...)
			break
		}
	}
	previewer.order = append(previewer.order, key)
}

func (previewer *documentPreviewer) invalidateCorpus(file string) {
	removed := false
	for sourceID, source := range previewer.sources {
		if sourceID.corpus != file {
			continue
		}
		for _, key := range append([]string(nil), source.keys...) {
			previewer.removePreview(key)
		}
		delete(previewer.sources, sourceID)
		removed = true
	}
	if removed {
		previewer.renderer = latexpdf.NewRenderer()
	}
}
