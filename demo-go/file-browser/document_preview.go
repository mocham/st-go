package main

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"image/png"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	mathjax "github.com/d2lang/mathjax-go"
	"github.com/mocham/GoLS/latexpdf"
	"github.com/srwiley/oksvg"
	"github.com/srwiley/rasterx"
	"golang.org/x/image/font"
	"golang.org/x/image/font/basicfont"
	"golang.org/x/image/font/opentype"
	"golang.org/x/image/math/fixed"
)

const (
	documentPreviewWidth   = 1200
	documentPreviewHeight  = 1600
	documentPreviewMarginX = 76
	documentPreviewMarginY = 66
	documentPreviewVersion = "latexpdf-tex-v1+mathjax-markdown-v1"
	maxPreviewDocumentSize = 12 << 20
)

var (
	previewPaper         = color.RGBA{R: 250, G: 249, B: 246, A: 255}
	previewInk           = color.RGBA{R: 29, G: 36, B: 44, A: 255}
	previewMuted         = color.RGBA{R: 102, G: 111, B: 120, A: 255}
	previewHeading       = color.RGBA{R: 28, G: 73, B: 110, A: 255}
	previewCodeBack      = color.RGBA{R: 235, G: 238, B: 241, A: 255}
	previewQuoteBack     = color.RGBA{R: 237, G: 244, B: 248, A: 255}
	previewRule          = color.RGBA{R: 195, G: 202, B: 208, A: 255}
	previewMathError     = color.RGBA{R: 145, G: 45, B: 45, A: 255}
	previewSVGSize       = regexp.MustCompile(`<svg[^>]*\bwidth="([0-9.]+)ex"[^>]*\bheight="([0-9.]+)ex"`)
	previewSimpleScript  = regexp.MustCompile(`([_^])([A-Za-z0-9])`)
	previewCommandScript = regexp.MustCompile(`([_^])(\\[A-Za-z@]+)`)
	pdfPageObject        = regexp.MustCompile(`/Type\s*/Page\b`)
)

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
	formulae map[string]*image.RGBA
	fonts    previewFontBook
	latex    *latexpdf.Renderer
}

type previewFontBook struct {
	regular *opentype.Font
	bold    *opentype.Font
	faces   map[previewFaceKey]font.Face
}

type previewFaceKey struct {
	size int
	bold bool
}

type previewBlockKind uint8

const (
	previewParagraph previewBlockKind = iota
	previewHeadingBlock
	previewMathBlock
	previewCodeBlock
	previewListBlock
	previewQuoteBlock
	previewRuleBlock
)

type previewSourceKind uint8

const (
	previewMarkdown previewSourceKind = iota
)

type previewBlock struct {
	Kind   previewBlockKind
	Text   string
	Level  int
	Source previewSourceKind
}

type previewMathMacro struct {
	body      string
	arguments int
}

type previewInline struct {
	text string
	math string
}

func newPreviewDocumentRenderer(cacheDir string) *previewDocumentRenderer {
	return &previewDocumentRenderer{
		cacheDir: cacheDir,
		docs:     make(map[string]*documentPreview),
		formulae: make(map[string]*image.RGBA),
		fonts:    loadPreviewFonts(),
		latex:    latexpdf.NewRenderer(),
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
	return hasSuffixAny(strings.ToLower(path), ".tex", ".md", ".markdown")
}

func (renderer *previewDocumentRenderer) render(path string) (*documentPreview, error) {
	return renderer.renderSized(path, 0, 0)
}

func (renderer *previewDocumentRenderer) renderSized(path string, pageWidth, pageHeight float64) (*documentPreview, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	if len(data) > maxPreviewDocumentSize {
		return nil, fmt.Errorf("document is too large to preview")
	}
	extension := strings.ToLower(filepath.Ext(path))
	version := documentPreviewVersion + "\x00" + extension + "\x00"
	if extension == ".tex" {
		version += fmt.Sprintf("%.6f\x00%.6f\x00", pageWidth, pageHeight)
	}
	digest := sha256.Sum256(append([]byte(version), data...))
	key := hex.EncodeToString(digest[:])
	if cached, ok := renderer.docs[key]; ok {
		return cached, nil
	}
	if extension == ".tex" {
		return renderer.renderTeX(path, data, key, pageWidth, pageHeight)
	}

	source := string(data)
	macros := extractPreviewMathMacros(source)
	blocks := parseMarkdownPreview(source)
	if len(blocks) == 0 {
		blocks = []previewBlock{{Kind: previewParagraph, Text: "(empty document)"}}
	}

	composer := newPreviewPageComposer(renderer, macros)
	for _, block := range blocks {
		composer.drawBlock(block)
	}
	composer.finish()
	pages, err := composer.writePages(renderer.cacheDir, key)
	if err != nil {
		return nil, err
	}
	result := &documentPreview{Pages: pages, PageCount: len(pages), MathErrors: composer.mathErrors, Kind: "Markdown"}
	renderer.docs[key] = result
	return result, nil
}

func (renderer *previewDocumentRenderer) renderTeX(path string, data []byte, key string, pageWidth, pageHeight float64) (*documentPreview, error) {
	source := string(data)
	title := strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
	if value, ok := latexpdf.FindCommandArgument(source, "title"); ok {
		if value = strings.TrimSpace(latexpdf.PlainText(value)); value != "" {
			title = value
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

	sourceDigest := sha256.Sum256(data)
	rendered := renderer.latex.RenderHTML(source, "pdf-html-"+hex.EncodeToString(sourceDigest[:]))
	pdf, err := renderer.latex.RenderPDFWithOptions(latexpdf.Document{
		Source: source, Title: title, Author: author, Modified: modified,
	}, latexpdf.PDFOptions{
		PageWidth: pageWidth, PageHeight: pageHeight,
	})
	if err != nil {
		return nil, err
	}
	pageCount := len(pdfPageObject.FindAll(pdf, -1))
	if pageCount == 0 {
		return nil, fmt.Errorf("latexpdf returned a PDF with no pages")
	}
	pdfPath := filepath.Join(renderer.cacheDir, fmt.Sprintf("document-%s.pdf", key[:16]))
	if err := os.WriteFile(pdfPath, pdf, 0600); err != nil {
		return nil, err
	}
	result := &documentPreview{
		PDFPath: pdfPath, PageCount: pageCount, MathErrors: rendered.MathErrors, Kind: "TeX",
	}
	renderer.docs[key] = result
	return result, nil
}

func loadPreviewFonts() previewFontBook {
	book := previewFontBook{faces: make(map[previewFaceKey]font.Face)}
	book.regular = loadPreviewFont(false)
	book.bold = loadPreviewFont(true)
	if book.bold == nil {
		book.bold = book.regular
	}
	return book
}

func loadPreviewFont(bold bool) *opentype.Font {
	var candidates []string
	if bold {
		candidates = append(candidates, os.Getenv("ST_FILE_BROWSER_DOCUMENT_BOLD_FONT"))
	} else {
		candidates = append(candidates, os.Getenv("ST_FILE_BROWSER_DOCUMENT_FONT"))
	}
	home, _ := os.UserHomeDir()
	if bold {
		candidates = append(candidates,
			filepath.Join(home, ".fonts", "msyhbd.ttc"),
			"/usr/share/fonts/opentype/noto/NotoSansCJK-Bold.ttc",
			"/usr/share/fonts/truetype/dejavu/DejaVuSans-Bold.ttf",
		)
	} else {
		candidates = append(candidates,
			filepath.Join(home, ".fonts", "msyh.ttc"),
			filepath.Join(home, ".fonts", "simsun.ttc"),
			"/usr/share/fonts/opentype/noto/NotoSansCJK-Regular.ttc",
			"/usr/share/fonts/truetype/dejavu/DejaVuSans.ttf",
			"Monaco_Linux.ttf",
		)
	}
	if executable, err := os.Executable(); err == nil {
		candidates = append(candidates, filepath.Join(filepath.Dir(executable), "Monaco_Linux.ttf"))
	}
	for _, candidate := range candidates {
		if strings.TrimSpace(candidate) == "" {
			continue
		}
		data, err := os.ReadFile(candidate)
		if err != nil {
			continue
		}
		collection, err := opentype.ParseCollection(data)
		if err != nil || collection.NumFonts() == 0 {
			continue
		}
		parsed, err := collection.Font(0)
		if err == nil {
			return parsed
		}
	}
	return nil
}

func (book *previewFontBook) face(size int, bold bool) font.Face {
	key := previewFaceKey{size: size, bold: bold}
	if face, ok := book.faces[key]; ok {
		return face
	}
	parsed := book.regular
	if bold && book.bold != nil {
		parsed = book.bold
	}
	if parsed == nil {
		book.faces[key] = basicfont.Face7x13
		return basicfont.Face7x13
	}
	face, err := opentype.NewFace(parsed, &opentype.FaceOptions{
		Size: float64(size), DPI: 96, Hinting: font.HintingFull,
	})
	if err != nil {
		book.faces[key] = basicfont.Face7x13
		return basicfont.Face7x13
	}
	book.faces[key] = face
	return face
}

type previewPageComposer struct {
	renderer   *previewDocumentRenderer
	macros     map[string]previewMathMacro
	pages      []*image.RGBA
	page       *image.RGBA
	y          int
	mathErrors int
}

func newPreviewPageComposer(renderer *previewDocumentRenderer, macros map[string]previewMathMacro) *previewPageComposer {
	composer := &previewPageComposer{renderer: renderer, macros: macros}
	composer.newPage()
	return composer
}

func (composer *previewPageComposer) newPage() {
	page := image.NewRGBA(image.Rect(0, 0, documentPreviewWidth, documentPreviewHeight))
	draw.Draw(page, page.Bounds(), &image.Uniform{C: previewPaper}, image.Point{}, draw.Src)
	composer.pages = append(composer.pages, page)
	composer.page = page
	composer.y = documentPreviewMarginY
}

func (composer *previewPageComposer) ensureSpace(height int) {
	if composer.y+height <= documentPreviewHeight-documentPreviewMarginY {
		return
	}
	composer.newPage()
}

func (composer *previewPageComposer) finish() {
	for index, page := range composer.pages {
		face := composer.renderer.fonts.face(16, false)
		label := fmt.Sprintf("%d / %d", index+1, len(composer.pages))
		drawer := font.Drawer{Dst: page, Src: image.NewUniform(previewMuted), Face: face}
		width := drawer.MeasureString(label).Ceil()
		drawer.Dot = fixed.P(documentPreviewWidth-documentPreviewMarginX-width, documentPreviewHeight-28)
		drawer.DrawString(label)
	}
}

func (composer *previewPageComposer) writePages(cacheDir, key string) ([]string, error) {
	paths := make([]string, 0, len(composer.pages))
	for index, page := range composer.pages {
		path := filepath.Join(cacheDir, fmt.Sprintf("document-%s-%03d.png", key[:16], index+1))
		file, err := os.Create(path)
		if err != nil {
			return nil, err
		}
		err = png.Encode(file, page)
		closeErr := file.Close()
		if err != nil {
			return nil, err
		}
		if closeErr != nil {
			return nil, closeErr
		}
		paths = append(paths, path)
	}
	return paths, nil
}

func (composer *previewPageComposer) drawBlock(block previewBlock) {
	switch block.Kind {
	case previewHeadingBlock:
		composer.drawHeading(block)
	case previewMathBlock:
		composer.drawDisplayMath(block.Text)
	case previewCodeBlock:
		composer.drawCode(block.Text)
	case previewListBlock:
		composer.drawParagraph(block.Text, block.Source, 36, 24, false, previewInk)
	case previewQuoteBlock:
		composer.ensureSpace(54)
		x := documentPreviewMarginX
		draw.Draw(composer.page, image.Rect(x, composer.y, documentPreviewWidth-documentPreviewMarginX, composer.y+4), &image.Uniform{C: previewQuoteBack}, image.Point{}, draw.Src)
		composer.y += 15
		composer.drawParagraph(block.Text, block.Source, 24, 23, false, previewMuted)
	case previewRuleBlock:
		composer.ensureSpace(28)
		y := composer.y + 8
		draw.Draw(composer.page, image.Rect(documentPreviewMarginX, y, documentPreviewWidth-documentPreviewMarginX, y+2), &image.Uniform{C: previewRule}, image.Point{}, draw.Src)
		composer.y += 28
	default:
		composer.drawParagraph(block.Text, block.Source, 0, 25, false, previewInk)
	}
}

func (composer *previewPageComposer) drawHeading(block previewBlock) {
	size := 36
	lineHeight := 52
	before := 24
	if block.Level <= 1 {
		size, lineHeight, before = 43, 61, 8
	} else if block.Level >= 3 {
		size, lineHeight, before = 30, 44, 18
	}
	composer.ensureSpace(before + lineHeight + 20)
	composer.y += before
	composer.drawParagraphWithMetrics(block.Text, block.Source, 0, size, lineHeight, true, previewHeading, 14)
}

func (composer *previewPageComposer) drawParagraph(text string, source previewSourceKind, indent, size int, bold bool, ink color.Color) {
	lineHeight := size + 15
	composer.drawParagraphWithMetrics(text, source, indent, size, lineHeight, bold, ink, 14)
}

func (composer *previewPageComposer) drawParagraphWithMetrics(text string, source previewSourceKind, indent, size, lineHeight int, bold bool, ink color.Color, after int) {
	text = strings.TrimSpace(text)
	if text == "" {
		return
	}
	face := composer.renderer.fonts.face(size, bold)
	parts := splitPreviewInlineMath(text)
	x := documentPreviewMarginX + indent
	lineStart := x
	lineLimit := documentPreviewWidth - documentPreviewMarginX
	lineTop := composer.y
	composer.ensureSpace(lineHeight)
	lineTop = composer.y
	drawer := font.Drawer{Dst: composer.page, Src: image.NewUniform(ink), Face: face}
	lineEmpty := true

	newLine := func() int {
		composer.y = lineTop + lineHeight
		composer.ensureSpace(lineHeight)
		lineTop = composer.y
		x = lineStart
		lineEmpty = true
		return lineTop
	}

	for _, part := range parts {
		if part.math != "" {
			formula, err := composer.formula(part.math, false, lineHeight-5, lineLimit-lineStart)
			if err != nil {
				composer.mathErrors++
				fallback := "[" + strings.TrimSpace(part.math) + "]"
				for _, token := range previewWrapTokens(fallback) {
					x, lineEmpty = drawPreviewTextToken(composer, &drawer, token, x, lineStart, lineLimit, lineTop, lineHeight, lineEmpty, newLine)
				}
				continue
			}
			if !lineEmpty && x+formula.Bounds().Dx() > lineLimit {
				newLine()
			}
			if formula.Bounds().Dx() > lineLimit-lineStart {
				newLine()
			}
			y := lineTop + (lineHeight-formula.Bounds().Dy())/2
			draw.Draw(composer.page, image.Rect(x, y, x+formula.Bounds().Dx(), y+formula.Bounds().Dy()), formula, formula.Bounds().Min, draw.Over)
			x += formula.Bounds().Dx() + 5
			lineEmpty = false
			continue
		}
		plain := previewPlainMarkdown(part.text)
		for _, token := range previewWrapTokens(plain) {
			x, lineEmpty = drawPreviewTextToken(composer, &drawer, token, x, lineStart, lineLimit, lineTop, lineHeight, lineEmpty, newLine)
		}
	}
	composer.y = lineTop + lineHeight + after
}

func drawPreviewTextToken(composer *previewPageComposer, drawer *font.Drawer, token string, x, lineStart, lineLimit, lineTop, lineHeight int, lineEmpty bool, newLine func() int) (int, bool) {
	if token == "" {
		return x, lineEmpty
	}
	if token == "\n" {
		lineTop = newLine()
		return lineStart, true
	}
	if token == " " && lineEmpty {
		return x, lineEmpty
	}
	width := drawer.MeasureString(token).Ceil()
	if !lineEmpty && x+width > lineLimit {
		lineTop = newLine()
		x, lineEmpty = lineStart, true
	}
	if width > lineLimit-lineStart {
		for _, r := range token {
			piece := string(r)
			pieceWidth := drawer.MeasureString(piece).Ceil()
			if !lineEmpty && x+pieceWidth > lineLimit {
				lineTop = newLine()
				x, lineEmpty = lineStart, true
			}
			drawer.Dot = fixed.P(x, lineTop+drawer.Face.Metrics().Ascent.Ceil()+2)
			drawer.DrawString(piece)
			x += pieceWidth
			lineEmpty = false
		}
		return x, lineEmpty
	}
	drawer.Dot = fixed.P(x, lineTop+drawer.Face.Metrics().Ascent.Ceil()+2)
	drawer.DrawString(token)
	return x + width, false
}

func (composer *previewPageComposer) drawDisplayMath(expression string) {
	formula, err := composer.formula(expression, true, 230, documentPreviewWidth-2*documentPreviewMarginX)
	if err != nil {
		composer.mathErrors++
		composer.drawParagraph("Math rendering failed: "+strings.TrimSpace(expression), previewMarkdown, 20, 21, false, previewMathError)
		return
	}
	height := formula.Bounds().Dy() + 34
	composer.ensureSpace(height)
	x := (documentPreviewWidth - formula.Bounds().Dx()) / 2
	y := composer.y + 12
	draw.Draw(composer.page, image.Rect(x, y, x+formula.Bounds().Dx(), y+formula.Bounds().Dy()), formula, formula.Bounds().Min, draw.Over)
	composer.y += height
}

func (composer *previewPageComposer) drawCode(source string) {
	face := composer.renderer.fonts.face(19, false)
	lineHeight := 29
	lines := strings.Split(strings.TrimSuffix(source, "\n"), "\n")
	if len(lines) == 0 {
		return
	}
	for _, line := range lines {
		wrapped := wrapPreviewString(line, face, documentPreviewWidth-2*documentPreviewMarginX-34)
		if len(wrapped) == 0 {
			wrapped = []string{" "}
		}
		for _, part := range wrapped {
			composer.ensureSpace(lineHeight + 4)
			draw.Draw(composer.page, image.Rect(documentPreviewMarginX, composer.y, documentPreviewWidth-documentPreviewMarginX, composer.y+lineHeight+2), &image.Uniform{C: previewCodeBack}, image.Point{}, draw.Src)
			drawer := font.Drawer{Dst: composer.page, Src: image.NewUniform(previewInk), Face: face, Dot: fixed.P(documentPreviewMarginX+16, composer.y+face.Metrics().Ascent.Ceil()+3)}
			drawer.DrawString(strings.ReplaceAll(part, "\t", "    "))
			composer.y += lineHeight + 2
		}
	}
	composer.y += 14
}

func (composer *previewPageComposer) formula(expression string, display bool, maxHeight, maxWidth int) (*image.RGBA, error) {
	expanded := expandPreviewMathMacros(strings.TrimSpace(expression), composer.macros)
	expanded = normalizePreviewMath(expanded)
	return composer.renderer.formulaExpanded(expanded, display, maxHeight, maxWidth)
}

func (renderer *previewDocumentRenderer) formulaExpanded(expression string, display bool, maxHeight, maxWidth int) (*image.RGBA, error) {
	keyBytes := sha256.Sum256([]byte(fmt.Sprintf("%t\x00%d\x00%d\x00%s", display, maxHeight, maxWidth, expression)))
	key := hex.EncodeToString(keyBytes[:])
	if cached, ok := renderer.formulae[key]; ok {
		return cached, nil
	}
	options := mathjax.DefaultOptions()
	options.Display = display
	options.Em = 32
	options.Ex = 16
	svg, err := mathjax.RenderWithOptions(expression, options)
	if err != nil {
		return nil, err
	}
	if strings.Contains(svg, `data-mml-node="merror"`) || strings.Contains(svg, `data-mjx-error`) {
		return nil, fmt.Errorf("MathJax rejected expression")
	}
	matches := previewSVGSize.FindStringSubmatch(svg)
	if len(matches) != 3 {
		return nil, fmt.Errorf("MathJax SVG has no dimensions")
	}
	widthEx, err := strconv.ParseFloat(matches[1], 64)
	if err != nil {
		return nil, err
	}
	heightEx, err := strconv.ParseFloat(matches[2], 64)
	if err != nil || heightEx <= 0 || widthEx <= 0 {
		return nil, fmt.Errorf("invalid MathJax SVG dimensions")
	}
	width := int(widthEx * options.Ex)
	height := int(heightEx * options.Ex)
	if width < 1 {
		width = 1
	}
	if height < 1 {
		height = 1
	}
	scale := 1.0
	if height > maxHeight {
		scale = float64(maxHeight) / float64(height)
	}
	if int(float64(width)*scale) > maxWidth {
		scale = float64(maxWidth) / float64(width)
	}
	width = maxInt(1, int(float64(width)*scale))
	height = maxInt(1, int(float64(height)*scale))
	// MathJax serializes the root dimensions in ex units. oksvg correctly uses
	// the viewBox for geometry, but accepts only unitless/px root dimensions;
	// normalize those two attributes before handing the SVG to the rasterizer.
	// oksvg also does not recognize CSS's currentColor keyword, so resolve it
	// to the document ink color used for formulas.
	svg = regexp.MustCompile(`\bwidth="[0-9.]+ex"`).ReplaceAllString(svg, fmt.Sprintf(`width="%d"`, width))
	svg = regexp.MustCompile(`\bheight="[0-9.]+ex"`).ReplaceAllString(svg, fmt.Sprintf(`height="%d"`, height))
	svg = strings.ReplaceAll(svg, "currentColor", "#1d242c")
	icon, err := oksvg.ReadIconStream(strings.NewReader(svg), oksvg.IgnoreErrorMode)
	if err != nil {
		return nil, err
	}
	imageOut := image.NewRGBA(image.Rect(0, 0, width, height))
	// SvgIcon.SetTarget applies a non-zero viewBox origin after scaling. MathJax
	// uses a negative Y origin, so build the equivalent SVG viewBox transform in
	// the correct order: scale * translate(-origin).
	icon.Transform = rasterx.Identity.
		Scale(float64(width)/icon.ViewBox.W, float64(height)/icon.ViewBox.H).
		Translate(-icon.ViewBox.X, -icon.ViewBox.Y)
	scanner := rasterx.NewScannerGV(width, height, imageOut, imageOut.Bounds())
	dasher := rasterx.NewDasher(width, height, scanner)
	icon.Draw(dasher, 1)
	renderer.formulae[key] = imageOut
	return imageOut, nil
}

func parseMarkdownPreview(source string) []previewBlock {
	source = strings.ReplaceAll(source, "\r\n", "\n")
	source = stripPreviewMathDefinitions(source)
	lines := strings.Split(source, "\n")
	var blocks []previewBlock
	var paragraph []string
	flushParagraph := func() {
		text := strings.TrimSpace(strings.Join(paragraph, " "))
		if text != "" {
			blocks = append(blocks, previewBlock{Kind: previewParagraph, Text: text, Source: previewMarkdown})
		}
		paragraph = nil
	}
	inFence := false
	fence := ""
	var code []string
	frontMatter := len(lines) > 0 && strings.TrimSpace(lines[0]) == "---"
	for i := 0; i < len(lines); i++ {
		line := lines[i]
		trimmed := strings.TrimSpace(line)
		if frontMatter {
			if i > 0 && trimmed == "---" {
				frontMatter = false
			}
			continue
		}
		if inFence {
			if strings.HasPrefix(trimmed, fence) {
				blocks = append(blocks, previewBlock{Kind: previewCodeBlock, Text: strings.Join(code, "\n"), Source: previewMarkdown})
				inFence, fence, code = false, "", nil
			} else {
				code = append(code, line)
			}
			continue
		}
		if strings.HasPrefix(trimmed, "```") || strings.HasPrefix(trimmed, "~~~") {
			flushParagraph()
			inFence = true
			fence = trimmed[:3]
			continue
		}
		if expression, end, ok := collectPreviewDelimitedMath(lines, i, "$$", "$$"); ok {
			flushParagraph()
			blocks = append(blocks, previewBlock{Kind: previewMathBlock, Text: expression, Source: previewMarkdown})
			i = end
			continue
		}
		if expression, end, ok := collectPreviewDelimitedMath(lines, i, `\[`, `\]`); ok {
			flushParagraph()
			blocks = append(blocks, previewBlock{Kind: previewMathBlock, Text: expression, Source: previewMarkdown})
			i = end
			continue
		}
		if level, title, ok := markdownPreviewHeading(trimmed); ok {
			flushParagraph()
			blocks = append(blocks, previewBlock{Kind: previewHeadingBlock, Text: title, Level: level, Source: previewMarkdown})
			continue
		}
		if trimmed == "---" || trimmed == "***" || trimmed == "___" {
			flushParagraph()
			blocks = append(blocks, previewBlock{Kind: previewRuleBlock})
			continue
		}
		if item, ok := markdownPreviewListItem(trimmed); ok {
			flushParagraph()
			blocks = append(blocks, previewBlock{Kind: previewListBlock, Text: item, Source: previewMarkdown})
			continue
		}
		if strings.HasPrefix(trimmed, ">") {
			flushParagraph()
			blocks = append(blocks, previewBlock{Kind: previewQuoteBlock, Text: strings.TrimSpace(strings.TrimPrefix(trimmed, ">")), Source: previewMarkdown})
			continue
		}
		if trimmed == "" {
			flushParagraph()
			continue
		}
		paragraph = append(paragraph, trimmed)
	}
	if inFence {
		blocks = append(blocks, previewBlock{Kind: previewCodeBlock, Text: strings.Join(code, "\n"), Source: previewMarkdown})
	}
	flushParagraph()
	return blocks
}

func markdownPreviewHeading(line string) (int, string, bool) {
	level := 0
	for level < len(line) && level < 6 && line[level] == '#' {
		level++
	}
	if level == 0 || level >= len(line) || !unicode.IsSpace(rune(line[level])) {
		return 0, "", false
	}
	return level, strings.TrimSpace(line[level:]), true
}

func markdownPreviewListItem(line string) (string, bool) {
	for _, prefix := range []string{"- ", "* ", "+ "} {
		if strings.HasPrefix(line, prefix) {
			return "•  " + strings.TrimSpace(line[len(prefix):]), true
		}
	}
	index := 0
	for index < len(line) && line[index] >= '0' && line[index] <= '9' {
		index++
	}
	if index > 0 && index+1 < len(line) && line[index] == '.' && unicode.IsSpace(rune(line[index+1])) {
		return line[:index+1] + "  " + strings.TrimSpace(line[index+1:]), true
	}
	return "", false
}

func collectPreviewDelimitedMath(lines []string, start int, opening, closing string) (string, int, bool) {
	trimmed := strings.TrimSpace(lines[start])
	if !strings.HasPrefix(trimmed, opening) {
		return "", start, false
	}
	rest := strings.TrimPrefix(trimmed, opening)
	if opening == closing {
		if position := strings.Index(rest, closing); position >= 0 {
			return strings.TrimSpace(rest[:position]), start, true
		}
	} else if position := strings.Index(rest, closing); position >= 0 {
		return strings.TrimSpace(rest[:position]), start, true
	}
	var content []string
	if strings.TrimSpace(rest) != "" {
		content = append(content, rest)
	}
	for i := start + 1; i < len(lines); i++ {
		line := lines[i]
		if position := strings.Index(line, closing); position >= 0 {
			content = append(content, line[:position])
			return strings.TrimSpace(strings.Join(content, "\n")), i, true
		}
		content = append(content, line)
	}
	return "", start, false
}

func splitPreviewInlineMath(source string) []previewInline {
	var parts []previewInline
	textStart := 0
	flushText := func(end int) {
		if end > textStart {
			parts = append(parts, previewInline{text: source[textStart:end]})
		}
	}
	for position := 0; position < len(source); {
		switch {
		case strings.HasPrefix(source[position:], `\(`):
			if relative := strings.Index(source[position+2:], `\)`); relative >= 0 {
				flushText(position)
				end := position + 2 + relative
				parts = append(parts, previewInline{math: source[position+2 : end]})
				position = end + 2
				textStart = position
				continue
			}
		case source[position] == '$' && !previewEscaped(source, position):
			if position+1 < len(source) && source[position+1] == '$' {
				position += 2
				continue
			}
			if end := previewFindMathDollar(source, position+1); end >= 0 {
				flushText(position)
				parts = append(parts, previewInline{math: source[position+1 : end]})
				position = end + 1
				textStart = position
				continue
			}
		}
		_, size := utf8.DecodeRuneInString(source[position:])
		if size <= 0 {
			break
		}
		position += size
	}
	flushText(len(source))
	return parts
}

func previewWrapTokens(source string) []string {
	source = strings.ReplaceAll(source, "\r", "")
	var tokens []string
	var word strings.Builder
	flushWord := func() {
		if word.Len() > 0 {
			tokens = append(tokens, word.String())
			word.Reset()
		}
	}
	for _, r := range source {
		switch {
		case r == '\n':
			flushWord()
			tokens = append(tokens, "\n")
		case unicode.IsSpace(r):
			flushWord()
			if len(tokens) == 0 || tokens[len(tokens)-1] != " " {
				tokens = append(tokens, " ")
			}
		case unicode.In(r, unicode.Han, unicode.Hiragana, unicode.Katakana, unicode.Hangul):
			flushWord()
			tokens = append(tokens, string(r))
		default:
			word.WriteRune(r)
		}
	}
	flushWord()
	return tokens
}

func wrapPreviewString(source string, face font.Face, width int) []string {
	drawer := font.Drawer{Face: face}
	var lines []string
	var current strings.Builder
	for _, r := range source {
		candidate := current.String() + string(r)
		if current.Len() > 0 && drawer.MeasureString(candidate).Ceil() > width {
			lines = append(lines, current.String())
			current.Reset()
		}
		current.WriteRune(r)
	}
	if current.Len() > 0 {
		lines = append(lines, current.String())
	}
	return lines
}

func previewPlainMarkdown(source string) string {
	link := regexp.MustCompile(`!?(\[([^\]]*)\])\([^)]*\)`)
	source = link.ReplaceAllString(source, `$2`)
	source = strings.NewReplacer("**", "", "__", "", "*", "", "_", "", "~~", "", "`", "").Replace(source)
	source = regexp.MustCompile(`<[^>]+>`).ReplaceAllString(source, "")
	return source
}

func defaultPreviewMathMacros() map[string]previewMathMacro {
	return map[string]previewMathMacro{
		"Z": {body: `\mathbb{Z}`}, "Q": {body: `\mathbb{Q}`}, "R": {body: `\mathbb{R}`},
		"C": {body: `\mathbb{C}`}, "F": {body: `\mathbb{F}`}, "N": {body: `\mathbb{N}`},
		"Hom": {body: `\operatorname{Hom}`}, "Aut": {body: `\operatorname{Aut}`},
		"Gal": {body: `\operatorname{Gal}`}, "Spec": {body: `\operatorname{Spec}`},
		"im": {body: `\operatorname{im}`}, "ker": {body: `\operatorname{ker}`},
		"coker": {body: `\operatorname{coker}`}, "Frac": {body: `\operatorname{Frac}`},
		"pmod":       {body: `\;({\rm mod}\;#1)`, arguments: 1},
		"varprojlim": {body: `\mathop{\mathrm{lim}\limits_{\longleftarrow}}`},
		"varinjlim":  {body: `\mathop{\mathrm{lim}\limits_{\longrightarrow}}`},
	}
}

func extractPreviewMathMacros(source string) map[string]previewMathMacro {
	macros := defaultPreviewMathMacros()
	for position := 0; position < len(source); {
		next, command := nextPreviewMacroCommand(source, position)
		if next < 0 {
			break
		}
		if command == "def" {
			cursor := previewSkipSpace(source, next+len(`\def`))
			name, end := previewReadCommand(source, cursor)
			if end <= cursor {
				position = cursor + 1
				continue
			}
			cursor = end
			arguments := 0
			for cursor+1 < len(source) && source[cursor] == '#' && source[cursor+1] >= '1' && source[cursor+1] <= '9' {
				arguments = maxInt(arguments, int(source[cursor+1]-'0'))
				cursor += 2
			}
			cursor = previewSkipSpace(source, cursor)
			body, bodyEnd, ok := previewParseBraced(source, cursor)
			if ok && name != "" {
				macros[name] = previewMathMacro{body: body, arguments: arguments}
				position = bodyEnd
			} else {
				position = cursor + 1
			}
			continue
		}
		cursor := next + len(command) + 1
		if cursor < len(source) && source[cursor] == '*' {
			cursor++
		}
		cursor = previewSkipSpace(source, cursor)
		name := ""
		if cursor < len(source) && source[cursor] == '{' {
			value, end, ok := previewParseBraced(source, cursor)
			if !ok {
				position = cursor + 1
				continue
			}
			name = strings.TrimPrefix(strings.TrimSpace(value), `\`)
			cursor = end
		} else if cursor < len(source) && source[cursor] == '\\' {
			name, cursor = previewReadCommand(source, cursor)
		} else {
			position = cursor + 1
			continue
		}
		cursor = previewSkipSpace(source, cursor)
		arguments := 0
		if cursor < len(source) && source[cursor] == '[' {
			value, end, ok := previewParseDelimited(source, cursor, '[', ']')
			if ok {
				arguments, _ = strconv.Atoi(strings.TrimSpace(value))
				cursor = previewSkipSpace(source, end)
				if cursor < len(source) && source[cursor] == '[' {
					_, cursor, _ = previewParseDelimited(source, cursor, '[', ']')
					cursor = previewSkipSpace(source, cursor)
				}
			}
		}
		body, bodyEnd, ok := previewParseBraced(source, cursor)
		if !ok {
			position = cursor + 1
			continue
		}
		if strings.HasPrefix(command, "DeclareMathOperator") {
			body = `\operatorname{` + body + `}`
			arguments = 0
		}
		if name != "" && arguments <= 9 {
			macros[name] = previewMathMacro{body: body, arguments: arguments}
		}
		position = bodyEnd
	}
	return macros
}

func expandPreviewMathMacros(source string, macros map[string]previewMathMacro) string {
	for pass := 0; pass < 32; pass++ {
		var output strings.Builder
		changed := false
		for position := 0; position < len(source); {
			if source[position] != '\\' {
				_, size := utf8.DecodeRuneInString(source[position:])
				if size <= 0 {
					break
				}
				output.WriteString(source[position : position+size])
				position += size
				continue
			}
			name, commandEnd := previewReadCommand(source, position)
			definition, ok := macros[name]
			if commandEnd <= position || !ok {
				if commandEnd <= position {
					commandEnd = position + 1
				}
				output.WriteString(source[position:commandEnd])
				position = commandEnd
				continue
			}
			cursor := commandEnd
			arguments := make([]string, 0, definition.arguments)
			valid := true
			for index := 0; index < definition.arguments; index++ {
				cursor = previewSkipSpace(source, cursor)
				if cursor >= len(source) {
					valid = false
					break
				}
				if source[cursor] == '{' {
					argument, end, parsed := previewParseBraced(source, cursor)
					if !parsed {
						valid = false
						break
					}
					arguments = append(arguments, argument)
					cursor = end
				} else if source[cursor] == '\\' {
					_, end := previewReadCommand(source, cursor)
					arguments = append(arguments, source[cursor:end])
					cursor = end
				} else {
					_, size := utf8.DecodeRuneInString(source[cursor:])
					arguments = append(arguments, source[cursor:cursor+size])
					cursor += size
				}
			}
			if !valid {
				output.WriteString(source[position:commandEnd])
				position = commandEnd
				continue
			}
			replacement := definition.body
			for index, argument := range arguments {
				replacement = strings.ReplaceAll(replacement, fmt.Sprintf("#%d", index+1), "{"+argument+"}")
			}
			output.WriteString("{" + replacement + "}")
			position = cursor
			changed = true
		}
		source = output.String()
		if !changed {
			break
		}
	}
	return source
}

func normalizePreviewMath(source string) string {
	source = regexp.MustCompile(`\\label\s*\{[^{}]*\}`).ReplaceAllString(source, "")
	source = strings.NewReplacer(`\qedhere`, "", `\hline`, "", `\cr`, `\\`).Replace(source)
	source = previewCommandScript.ReplaceAllString(source, `$1{$2}`)
	return previewSimpleScript.ReplaceAllString(source, `$1{$2}`)
}

func stripPreviewMathDefinitions(source string) string {
	for {
		next, command := nextPreviewMacroCommand(source, 0)
		if next < 0 {
			return source
		}
		cursor := next
		if command == "def" {
			cursor = previewSkipSpace(source, next+len(`\def`))
			_, cursor = previewReadCommand(source, cursor)
			for cursor < len(source) && source[cursor] != '{' {
				cursor++
			}
			if _, end, ok := previewParseBraced(source, cursor); ok {
				source = source[:next] + source[end:]
				continue
			}
			return source
		}
		cursor = next + len(command) + 1
		if cursor < len(source) && source[cursor] == '*' {
			cursor++
		}
		cursor = previewSkipSpace(source, cursor)
		if cursor < len(source) && source[cursor] == '{' {
			_, cursor, _ = previewParseBraced(source, cursor)
		} else if cursor < len(source) && source[cursor] == '\\' {
			_, cursor = previewReadCommand(source, cursor)
		} else {
			return source
		}
		cursor = previewSkipSpace(source, cursor)
		for cursor < len(source) && source[cursor] == '[' {
			_, cursor, _ = previewParseDelimited(source, cursor, '[', ']')
			cursor = previewSkipSpace(source, cursor)
		}
		if _, end, ok := previewParseBraced(source, cursor); ok {
			source = source[:next] + source[end:]
			continue
		}
		return source
	}
}

func nextPreviewMacroCommand(source string, start int) (int, string) {
	commands := []string{"newcommand", "renewcommand", "providecommand", "DeclareMathOperator", "DeclareMathOperator*", "def"}
	best, selected := -1, ""
	for _, command := range commands {
		position := strings.Index(source[start:], `\`+command)
		if position < 0 {
			continue
		}
		position += start
		if best < 0 || position < best {
			best, selected = position, command
		}
	}
	return best, selected
}

func previewReadCommand(source string, start int) (string, int) {
	if start >= len(source) || source[start] != '\\' || start+1 >= len(source) {
		return "", start
	}
	position := start + 1
	if !unicode.IsLetter(rune(source[position])) && source[position] != '@' {
		return source[position : position+1], position + 1
	}
	for position < len(source) {
		r, size := utf8.DecodeRuneInString(source[position:])
		if !unicode.IsLetter(r) && r != '@' {
			break
		}
		position += size
	}
	return source[start+1 : position], position
}

func previewParseBraced(source string, start int) (string, int, bool) {
	return previewParseDelimited(source, start, '{', '}')
}

func previewParseDelimited(source string, start int, open, close byte) (string, int, bool) {
	if start >= len(source) || source[start] != open {
		return "", start, false
	}
	depth := 1
	for position := start + 1; position < len(source); position++ {
		if source[position] == open && !previewEscaped(source, position) {
			depth++
		} else if source[position] == close && !previewEscaped(source, position) {
			depth--
			if depth == 0 {
				return source[start+1 : position], position + 1, true
			}
		}
	}
	return "", start, false
}

func previewSkipSpace(source string, position int) int {
	for position < len(source) && unicode.IsSpace(rune(source[position])) {
		position++
	}
	return position
}

func previewEscaped(source string, position int) bool {
	backslashes := 0
	for position > 0 && source[position-1] == '\\' {
		backslashes++
		position--
	}
	return backslashes%2 == 1
}

func previewFindMathDollar(source string, start int) int {
	depth := 0
	for position := start; position < len(source); position++ {
		if previewEscaped(source, position) {
			continue
		}
		switch source[position] {
		case '{':
			depth++
		case '}':
			if depth > 0 {
				depth--
			}
		case '$':
			if depth == 0 {
				return position
			}
		}
	}
	return -1
}

func maxInt(left, right int) int {
	if left > right {
		return left
	}
	return right
}
