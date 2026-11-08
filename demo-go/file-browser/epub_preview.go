package main

import (
	"archive/zip"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/xml"
	"fmt"
	"io"
	"net/url"
	"os"
	pathpkg "path"
	"path/filepath"
	"strings"
	"time"

	"golang.org/x/net/html"
	"golang.org/x/net/html/charset"
	"st-go/semhtml"
)

const (
	epubPreviewVersion       = "epub-semhtml-v1"
	maxEPUBArchiveSize       = 256 << 20
	maxEPUBPackageSize       = 2 << 20
	maxEPUBSpineItemSize     = 4 << 20
	maxEPUBSemanticSource    = maxPreviewDocumentSize
	maxEPUBSpineItems        = 4096
	maxEPUBArchiveEntries    = 65536
	epubContainerArchivePath = "META-INF/container.xml"
)

type semanticHTMLBook struct {
	Title  string
	Author string
	HTML   string
}

type epubContainer struct {
	Rootfiles []epubRootfile `xml:"rootfiles>rootfile"`
}

type epubRootfile struct {
	FullPath string `xml:"full-path,attr"`
	Media    string `xml:"media-type,attr"`
}

type epubPackage struct {
	Metadata epubMetadata `xml:"metadata"`
	Manifest epubManifest `xml:"manifest"`
	Spine    epubSpine    `xml:"spine"`
}

type epubMetadata struct {
	Titles   []string `xml:"title"`
	Creators []string `xml:"creator"`
}

type epubManifest struct {
	Items []epubManifestItem `xml:"item"`
}

type epubManifestItem struct {
	ID        string `xml:"id,attr"`
	Href      string `xml:"href,attr"`
	MediaType string `xml:"media-type,attr"`
}

type epubSpine struct {
	Items []epubSpineItem `xml:"itemref"`
}

type epubSpineItem struct {
	IDRef  string `xml:"idref,attr"`
	Linear string `xml:"linear,attr"`
}

type epubChapter struct {
	archivePath string
	mediaType   string
	data        []byte
}

func (renderer *previewDocumentRenderer) renderEPUBSized(filePath string, pageWidth, pageHeight float64) (*documentPreview, error) {
	book, err := readEPUBBook(filePath)
	if err != nil {
		return nil, err
	}
	return renderer.renderSemanticHTMLSized(filePath, book, "EPUB", epubPreviewVersion, pageWidth, pageHeight)
}

func (renderer *previewDocumentRenderer) renderSemanticHTMLSized(filePath string, book semanticHTMLBook, kind, cacheVersion string, pageWidth, pageHeight float64) (*documentPreview, error) {
	modified := fileModificationTime(filePath)
	document := semhtml.Document{
		HTML: book.HTML, Title: book.Title, Author: book.Author, Modified: modified,
	}
	options := semhtml.PDFOptions{PageWidth: pageWidth, PageHeight: pageHeight}
	logicalPath := renderer.prepareSource(filePath, previewCacheDigest(
		cacheVersion, semhtml.PDFCacheKey(document),
	))
	key := previewCacheDigest(cacheVersion, logicalPath, semhtml.PDFCacheKeyWithOptions(document, options))
	if cached, ok := renderer.docs[key]; ok {
		renderer.touchSourcePreview(key)
		return cached, nil
	}
	if renderer.semantic == nil {
		renderer.semantic = semhtml.NewRenderer()
	}

	pdf, err := renderer.semantic.RenderPDFWithOptions(document, options)
	if err != nil {
		return nil, err
	}
	pageCount := len(pdfPageObject.FindAll(pdf, -1))
	if pageCount == 0 {
		return nil, fmt.Errorf("semhtml returned a PDF with no pages")
	}
	pdfPath := filepath.Join(renderer.cacheDir, fmt.Sprintf("document-%s.pdf", key[:16]))
	if err := os.WriteFile(pdfPath, pdf, 0o600); err != nil {
		return nil, err
	}
	result := &documentPreview{PDFPath: pdfPath, PageCount: pageCount, Kind: kind}
	renderer.storeSourcePreview(logicalPath, key, result)
	return result, nil
}

func fileModificationTime(filePath string) (modified time.Time) {
	if info, err := os.Stat(filePath); err == nil {
		return info.ModTime()
	}
	return modified
}

func readEPUBBook(filePath string) (semanticHTMLBook, error) {
	info, err := os.Stat(filePath)
	if err != nil {
		return semanticHTMLBook{}, err
	}
	if info.Size() > maxEPUBArchiveSize {
		return semanticHTMLBook{}, fmt.Errorf("EPUB is too large to preview")
	}
	zr, err := zip.OpenReader(filePath)
	if err != nil {
		return semanticHTMLBook{}, fmt.Errorf("open EPUB: %w", err)
	}
	defer zr.Close()
	if len(zr.File) > maxEPUBArchiveEntries {
		return semanticHTMLBook{}, fmt.Errorf("EPUB archive has too many entries")
	}

	entries := make(map[string]*zip.File, len(zr.File))
	for _, file := range zr.File {
		name, cleanErr := cleanEPUBArchivePath(file.Name)
		if cleanErr == nil {
			if _, exists := entries[name]; !exists {
				entries[name] = file
			}
		}
	}
	containerFile := entries[epubContainerArchivePath]
	if containerFile == nil {
		return semanticHTMLBook{}, fmt.Errorf("EPUB has no %s", epubContainerArchivePath)
	}
	containerData, err := readEPUBEntry(containerFile, maxEPUBPackageSize)
	if err != nil {
		return semanticHTMLBook{}, fmt.Errorf("read EPUB container: %w", err)
	}
	var container epubContainer
	if err := decodeEPUBXML(containerData, &container); err != nil {
		return semanticHTMLBook{}, fmt.Errorf("parse EPUB container: %w", err)
	}
	if len(container.Rootfiles) == 0 {
		return semanticHTMLBook{}, fmt.Errorf("EPUB container has no package document")
	}
	opfPath, opfFile, err := findEPUBPackage(container.Rootfiles, entries)
	if err != nil {
		return semanticHTMLBook{}, err
	}
	opfData, err := readEPUBEntry(opfFile, maxEPUBPackageSize)
	if err != nil {
		return semanticHTMLBook{}, fmt.Errorf("read EPUB package: %w", err)
	}
	var publication epubPackage
	if err := decodeEPUBXML(opfData, &publication); err != nil {
		return semanticHTMLBook{}, fmt.Errorf("parse EPUB package: %w", err)
	}

	manifest := make(map[string]epubManifestItem, len(publication.Manifest.Items))
	for _, item := range publication.Manifest.Items {
		if item.ID != "" {
			manifest[item.ID] = item
		}
	}
	if len(publication.Spine.Items) > maxEPUBSpineItems {
		return semanticHTMLBook{}, fmt.Errorf("EPUB spine has too many items")
	}
	chapters := make([]epubChapter, 0, len(publication.Spine.Items))
	totalSource := 0
	for _, reference := range publication.Spine.Items {
		if strings.EqualFold(strings.TrimSpace(reference.Linear), "no") {
			continue
		}
		item, ok := manifest[reference.IDRef]
		if !ok || !isEPUBXHTMLItem(item) {
			continue
		}
		chapterPath, resolveErr := resolveEPUBArchivePath(pathpkg.Dir(opfPath), item.Href)
		if resolveErr != nil {
			return semanticHTMLBook{}, fmt.Errorf("invalid EPUB spine path %q: %w", item.Href, resolveErr)
		}
		chapterFile := entries[chapterPath]
		if chapterFile == nil {
			return semanticHTMLBook{}, fmt.Errorf("EPUB spine item %q is missing", chapterPath)
		}
		data, readErr := readEPUBEntry(chapterFile, maxEPUBSpineItemSize)
		if readErr != nil {
			return semanticHTMLBook{}, fmt.Errorf("read EPUB spine item %q: %w", chapterPath, readErr)
		}
		totalSource += len(data)
		if totalSource > maxEPUBSemanticSource {
			return semanticHTMLBook{}, fmt.Errorf("EPUB text is too large to preview")
		}
		chapters = append(chapters, epubChapter{archivePath: chapterPath, mediaType: item.MediaType, data: data})
	}
	if len(chapters) == 0 {
		return semanticHTMLBook{}, fmt.Errorf("EPUB spine has no readable XHTML documents")
	}

	semanticHTML, err := normalizeEPUBChapters(chapters)
	if err != nil {
		return semanticHTMLBook{}, err
	}
	if len(semanticHTML) > maxEPUBSemanticSource {
		return semanticHTMLBook{}, fmt.Errorf("EPUB semantic text is too large to preview")
	}
	title := firstNonemptyEPUBValue(publication.Metadata.Titles)
	if title == "" {
		title = strings.TrimSuffix(filepath.Base(filePath), filepath.Ext(filePath))
	}
	authors := nonemptyEPUBValues(publication.Metadata.Creators)
	return semanticHTMLBook{Title: title, Author: strings.Join(authors, ", "), HTML: semanticHTML}, nil
}

func findEPUBPackage(rootfiles []epubRootfile, entries map[string]*zip.File) (string, *zip.File, error) {
	ordered := make([]epubRootfile, 0, len(rootfiles))
	for _, rootfile := range rootfiles {
		if strings.EqualFold(strings.TrimSpace(rootfile.Media), "application/oebps-package+xml") {
			ordered = append(ordered, rootfile)
		}
	}
	for _, rootfile := range rootfiles {
		if !strings.EqualFold(strings.TrimSpace(rootfile.Media), "application/oebps-package+xml") {
			ordered = append(ordered, rootfile)
		}
	}
	var firstMissing string
	for _, rootfile := range ordered {
		packagePath, err := cleanEPUBArchivePath(rootfile.FullPath)
		if err != nil {
			return "", nil, fmt.Errorf("invalid EPUB package path: %w", err)
		}
		if file := entries[packagePath]; file != nil {
			return packagePath, file, nil
		}
		if firstMissing == "" {
			firstMissing = packagePath
		}
	}
	return "", nil, fmt.Errorf("EPUB package %q is missing", firstMissing)
}

func decodeEPUBXML(data []byte, target any) error {
	decoder := xml.NewDecoder(bytes.NewReader(data))
	decoder.CharsetReader = charset.NewReaderLabel
	return decoder.Decode(target)
}

func readEPUBEntry(file *zip.File, limit int64) ([]byte, error) {
	if file.UncompressedSize64 > uint64(limit) {
		return nil, fmt.Errorf("entry exceeds %d bytes", limit)
	}
	reader, err := file.Open()
	if err != nil {
		return nil, err
	}
	defer reader.Close()
	data, err := io.ReadAll(io.LimitReader(reader, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		return nil, fmt.Errorf("entry exceeds %d bytes", limit)
	}
	return data, nil
}

func cleanEPUBArchivePath(value string) (string, error) {
	if value == "" || strings.Contains(value, "\\") || strings.ContainsRune(value, '\x00') || strings.HasPrefix(value, "/") {
		return "", fmt.Errorf("unsafe archive path %q", value)
	}
	cleaned := pathpkg.Clean(value)
	if cleaned == "." || cleaned == ".." || strings.HasPrefix(cleaned, "../") {
		return "", fmt.Errorf("unsafe archive path %q", value)
	}
	return cleaned, nil
}

func resolveEPUBArchivePath(base, reference string) (string, error) {
	parsed, err := url.Parse(strings.TrimSpace(reference))
	if err != nil || parsed.Scheme != "" || parsed.Host != "" || parsed.Path == "" {
		return "", fmt.Errorf("invalid relative reference")
	}
	decoded, err := url.PathUnescape(parsed.Path)
	if err != nil {
		return "", err
	}
	if strings.HasPrefix(decoded, "/") {
		return "", fmt.Errorf("absolute archive path")
	}
	return cleanEPUBArchivePath(pathpkg.Join(base, decoded))
}

func isEPUBXHTMLItem(item epubManifestItem) bool {
	mediaType := strings.ToLower(strings.TrimSpace(item.MediaType))
	if mediaType == "application/xhtml+xml" || mediaType == "text/html" {
		return true
	}
	extension := strings.ToLower(pathpkg.Ext(strings.SplitN(item.Href, "#", 2)[0]))
	return extension == ".xhtml" || extension == ".html" || extension == ".htm"
}

func firstNonemptyEPUBValue(values []string) string {
	for _, value := range values {
		if value = collapseEPUBWhitespace(value); value != "" {
			return value
		}
	}
	return ""
}

func nonemptyEPUBValues(values []string) []string {
	result := make([]string, 0, len(values))
	for _, value := range values {
		if value = collapseEPUBWhitespace(value); value != "" {
			result = append(result, value)
		}
	}
	return result
}

func normalizeEPUBChapters(chapters []epubChapter) (string, error) {
	return normalizeSemanticHTMLChapters(chapters, semanticHTMLOptions{
		chapterBreaks:          true,
		synthesizeChapterTitle: true,
	})
}

type semanticHTMLOptions struct {
	chapterBreaks          bool
	synthesizeChapterTitle bool
}

func normalizeSemanticHTMLChapters(chapters []epubChapter, options semanticHTMLOptions) (string, error) {
	chapterIndexes := make(map[string]int, len(chapters))
	for index, chapter := range chapters {
		if _, exists := chapterIndexes[chapter.archivePath]; !exists {
			chapterIndexes[chapter.archivePath] = index
		}
	}
	root := elementNode("main")
	for index, chapter := range chapters {
		mediaType := chapter.mediaType
		if mediaType == "" {
			mediaType = "application/xhtml+xml"
		}
		reader, err := charset.NewReader(bytes.NewReader(chapter.data), mediaType)
		if err != nil {
			return "", fmt.Errorf("decode semantic HTML source %q: %w", chapter.archivePath, err)
		}
		parsed, err := html.Parse(reader)
		if err != nil {
			return "", fmt.Errorf("parse semantic HTML source %q: %w", chapter.archivePath, err)
		}
		body := findEPUBElement(parsed, "body")
		if body == nil {
			return "", fmt.Errorf("semantic HTML source %q has no body", chapter.archivePath)
		}
		section := elementNode("section")
		className := "html-document"
		if options.chapterBreaks {
			className = "epub-chapter"
		}
		section.Attr = []html.Attribute{
			{Key: "class", Val: className},
			{Key: "id", Val: epubAnchorID(index, "")},
		}
		chapterTitle := collapseEPUBWhitespace(epubElementText(findEPUBElement(parsed, "title")))
		if options.synthesizeChapterTitle && !hasEPUBHeading(body) && chapterTitle != "" {
			heading := elementNode("h1")
			heading.AppendChild(textNode(chapterTitle))
			section.AppendChild(heading)
		}
		context := epubNormalizeContext{
			chapterIndex:  index,
			chapterPath:   chapter.archivePath,
			chapterByPath: chapterIndexes,
		}
		appendNormalizedEPUBChildren(section, body, context)
		if strings.TrimSpace(epubElementText(section)) == "" {
			paragraph := elementNode("p")
			paragraph.AppendChild(textNode("[Empty chapter]"))
			section.AppendChild(paragraph)
		}
		root.AppendChild(section)
	}
	var output bytes.Buffer
	for child := root.FirstChild; child != nil; child = child.NextSibling {
		if err := html.Render(&output, child); err != nil {
			return "", fmt.Errorf("serialize EPUB semantic HTML: %w", err)
		}
	}
	return output.String(), nil
}

type epubNormalizeContext struct {
	chapterIndex  int
	chapterPath   string
	chapterByPath map[string]int
}

func appendNormalizedEPUBChildren(target, source *html.Node, context epubNormalizeContext) {
	for child := source.FirstChild; child != nil; child = child.NextSibling {
		for _, normalized := range normalizeEPUBNode(child, context) {
			target.AppendChild(normalized)
		}
	}
}

func normalizeEPUBNode(source *html.Node, context epubNormalizeContext) []*html.Node {
	if source == nil {
		return nil
	}
	if source.Type == html.TextNode {
		return []*html.Node{textNode(source.Data)}
	}
	if source.Type != html.ElementNode || epubNodeIsHidden(source) {
		return nil
	}
	tag := strings.ToLower(source.Data)
	switch tag {
	case "script", "style", "template", "head", "title", "meta", "link", "base", "object", "embed", "iframe", "canvas", "audio", "video", "source", "track", "form", "input", "button", "select", "option", "textarea", "noscript":
		return nil
	case "table":
		if table := normalizeEPUBTable(source, context); table != nil {
			return []*html.Node{table}
		}
		return nil
	case "img", "image":
		label := collapseEPUBWhitespace(firstEPUBAttribute(source, "alt", "title", "aria-label"))
		if label == "" {
			return nil
		}
		span := elementNode("span")
		copyEPUBAnchor(span, source, context.chapterIndex)
		emphasis := elementNode("em")
		emphasis.AppendChild(textNode("[Image: " + label + "]"))
		span.AppendChild(emphasis)
		return []*html.Node{span}
	case "hr":
		paragraph := elementNode("p")
		copyEPUBAnchor(paragraph, source, context.chapterIndex)
		paragraph.AppendChild(textNode("—"))
		return []*html.Node{paragraph}
	}

	targetTag := epubSemanticTag(tag)
	target := elementNode(targetTag)
	copyEPUBAnchor(target, source, context.chapterIndex)
	if tag == "a" {
		if href := normalizeEPUBHref(context, htmlAttributeValue(source, "href")); href != "" {
			target.Attr = append(target.Attr, html.Attribute{Key: "href", Val: href})
		}
	}
	if tag == "blockquote" {
		target.Attr = append(target.Attr, html.Attribute{Key: "class", Val: "epub-blockquote"})
	}
	if tag == "dt" {
		strong := elementNode("strong")
		appendNormalizedEPUBChildren(strong, source, context)
		target.AppendChild(strong)
		return []*html.Node{target}
	}
	if tag == "figcaption" {
		emphasis := elementNode("em")
		appendNormalizedEPUBChildren(emphasis, source, context)
		target.AppendChild(emphasis)
		return []*html.Node{target}
	}
	appendNormalizedEPUBChildren(target, source, context)
	return []*html.Node{target}
}

func epubSemanticTag(tag string) string {
	switch tag {
	case "h1", "h2", "h3", "h4", "p", "pre", "ul", "ol", "li", "section", "article", "main", "nav", "details", "summary", "strong", "b", "em", "i", "code", "small", "u", "del", "s", "a", "span", "br":
		return tag
	case "h5", "h6":
		return "h4"
	case "address", "aside", "blockquote", "div", "dl", "figure", "footer", "header":
		return "div"
	case "dt", "dd", "figcaption":
		return "p"
	default:
		return "span"
	}
}

func normalizeEPUBTable(source *html.Node, context epubNormalizeContext) *html.Node {
	table := elementNode("div")
	table.Attr = append(table.Attr, html.Attribute{Key: "class", Val: "epub-table"})
	copyEPUBAnchor(table, source, context.chapterIndex)
	for child := source.FirstChild; child != nil; child = child.NextSibling {
		if child.Type == html.ElementNode && strings.EqualFold(child.Data, "caption") {
			caption := collapseEPUBWhitespace(epubSemanticText(child))
			if caption != "" {
				paragraph := elementNode("p")
				emphasis := elementNode("em")
				emphasis.AppendChild(textNode(caption))
				paragraph.AppendChild(emphasis)
				table.AppendChild(paragraph)
			}
		}
	}
	var rows []*html.Node
	collectEPUBElements(source, "tr", &rows)
	for _, row := range rows {
		var cells []*html.Node
		for cell := row.FirstChild; cell != nil; cell = cell.NextSibling {
			if cell.Type == html.ElementNode && (strings.EqualFold(cell.Data, "td") || strings.EqualFold(cell.Data, "th")) {
				cells = append(cells, cell)
			}
		}
		if len(cells) == 0 {
			continue
		}
		paragraph := elementNode("p")
		copyEPUBAnchor(paragraph, row, context.chapterIndex)
		for index, cell := range cells {
			if index > 0 {
				paragraph.AppendChild(textNode("  |  "))
			}
			text := collapseEPUBWhitespace(epubSemanticText(cell))
			if strings.EqualFold(cell.Data, "th") {
				strong := elementNode("strong")
				strong.AppendChild(textNode(text))
				paragraph.AppendChild(strong)
			} else {
				paragraph.AppendChild(textNode(text))
			}
		}
		table.AppendChild(paragraph)
	}
	if table.FirstChild == nil {
		return nil
	}
	return table
}

func normalizeEPUBHref(context epubNormalizeContext, value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	parsed, err := url.Parse(value)
	if err != nil {
		return ""
	}
	if parsed.Scheme != "" {
		switch strings.ToLower(parsed.Scheme) {
		case "http", "https", "mailto":
			return value
		default:
			return ""
		}
	}
	if parsed.Host != "" {
		return ""
	}
	targetPath := context.chapterPath
	if parsed.Path != "" {
		decoded, decodeErr := url.PathUnescape(parsed.Path)
		if decodeErr != nil || strings.HasPrefix(decoded, "/") {
			return ""
		}
		resolved, resolveErr := cleanEPUBArchivePath(pathpkg.Join(pathpkg.Dir(context.chapterPath), decoded))
		if resolveErr != nil {
			return ""
		}
		targetPath = resolved
	}
	targetIndex, ok := context.chapterByPath[targetPath]
	if !ok {
		return ""
	}
	fragment, err := url.PathUnescape(parsed.Fragment)
	if err != nil {
		fragment = parsed.Fragment
	}
	return "#" + epubAnchorID(targetIndex, fragment)
}

func copyEPUBAnchor(target, source *html.Node, chapterIndex int) {
	identifier := htmlAttributeValue(source, "id")
	if identifier == "" && strings.EqualFold(source.Data, "a") {
		identifier = htmlAttributeValue(source, "name")
	}
	if identifier != "" {
		target.Attr = append(target.Attr, html.Attribute{Key: "id", Val: epubAnchorID(chapterIndex, identifier)})
	}
}

func epubAnchorID(chapterIndex int, identifier string) string {
	prefix := fmt.Sprintf("epub-chapter-%d", chapterIndex+1)
	identifier = strings.TrimSpace(identifier)
	if identifier == "" {
		return prefix
	}
	digest := sha256.Sum256([]byte(identifier))
	return prefix + "-anchor-" + hex.EncodeToString(digest[:8])
}

func epubNodeIsHidden(node *html.Node) bool {
	if hasHTMLAttribute(node, "hidden") || strings.EqualFold(htmlAttributeValue(node, "aria-hidden"), "true") {
		return true
	}
	style := strings.ToLower(strings.ReplaceAll(htmlAttributeValue(node, "style"), " ", ""))
	return strings.Contains(style, "display:none") || strings.Contains(style, "visibility:hidden")
}

func hasEPUBHeading(node *html.Node) bool {
	if node == nil {
		return false
	}
	if node.Type == html.ElementNode && epubNodeIsHidden(node) {
		return false
	}
	if node.Type == html.ElementNode && len(node.Data) == 2 && node.Data[0] == 'h' && node.Data[1] >= '1' && node.Data[1] <= '6' {
		return true
	}
	for child := node.FirstChild; child != nil; child = child.NextSibling {
		if hasEPUBHeading(child) {
			return true
		}
	}
	return false
}

func collectEPUBElements(node *html.Node, name string, result *[]*html.Node) {
	for child := node.FirstChild; child != nil; child = child.NextSibling {
		if child.Type == html.ElementNode && strings.EqualFold(child.Data, name) {
			*result = append(*result, child)
		}
		collectEPUBElements(child, name, result)
	}
}

func findEPUBElement(node *html.Node, name string) *html.Node {
	if node == nil {
		return nil
	}
	if node.Type == html.ElementNode && strings.EqualFold(node.Data, name) {
		return node
	}
	for child := node.FirstChild; child != nil; child = child.NextSibling {
		if found := findEPUBElement(child, name); found != nil {
			return found
		}
	}
	return nil
}

func epubElementText(node *html.Node) string {
	if node == nil {
		return ""
	}
	if node.Type == html.TextNode {
		return node.Data
	}
	var result strings.Builder
	for child := node.FirstChild; child != nil; child = child.NextSibling {
		result.WriteString(epubElementText(child))
	}
	return result.String()
}

func epubSemanticText(node *html.Node) string {
	if node == nil {
		return ""
	}
	if node.Type == html.TextNode {
		return node.Data
	}
	if node.Type != html.ElementNode || epubNodeIsHidden(node) {
		return ""
	}
	tag := strings.ToLower(node.Data)
	if tag == "script" || tag == "style" || tag == "template" {
		return ""
	}
	if tag == "img" || tag == "image" {
		return firstEPUBAttribute(node, "alt", "title", "aria-label")
	}
	var result strings.Builder
	for child := node.FirstChild; child != nil; child = child.NextSibling {
		result.WriteString(epubSemanticText(child))
	}
	return result.String()
}

func htmlAttributeValue(node *html.Node, name string) string {
	if node == nil {
		return ""
	}
	for _, attribute := range node.Attr {
		if strings.EqualFold(attribute.Key, name) {
			return attribute.Val
		}
	}
	return ""
}

func hasHTMLAttribute(node *html.Node, name string) bool {
	if node == nil {
		return false
	}
	for _, attribute := range node.Attr {
		if strings.EqualFold(attribute.Key, name) {
			return true
		}
	}
	return false
}

func firstEPUBAttribute(node *html.Node, names ...string) string {
	for _, name := range names {
		if value := htmlAttributeValue(node, name); value != "" {
			return value
		}
	}
	return ""
}

func collapseEPUBWhitespace(value string) string {
	return strings.Join(strings.Fields(value), " ")
}

func elementNode(name string) *html.Node {
	return &html.Node{Type: html.ElementNode, Data: name}
}

func textNode(value string) *html.Node {
	return &html.Node{Type: html.TextNode, Data: value}
}
