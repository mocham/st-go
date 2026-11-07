package main

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/net/html"
	"golang.org/x/net/html/charset"
)

const htmlPreviewVersion = "html-semhtml-v1"

func (renderer *previewDocumentRenderer) renderHTMLSized(filePath string, pageWidth, pageHeight float64) (*documentPreview, error) {
	book, err := readHTMLBook(filePath)
	if err != nil {
		return nil, err
	}
	return renderer.renderSemanticHTMLSized(filePath, book, "HTML", htmlPreviewVersion, pageWidth, pageHeight)
}

func readHTMLBook(filePath string) (semanticHTMLBook, error) {
	info, err := os.Stat(filePath)
	if err != nil {
		return semanticHTMLBook{}, err
	}
	if info.Size() > maxPreviewDocumentSize {
		return semanticHTMLBook{}, fmt.Errorf("HTML document is too large to preview")
	}
	data, err := os.ReadFile(filePath)
	if err != nil {
		return semanticHTMLBook{}, err
	}
	mediaType := "text/html"
	if strings.EqualFold(filepath.Ext(filePath), ".xhtml") {
		mediaType = "application/xhtml+xml"
	}
	reader, err := charset.NewReader(bytes.NewReader(data), mediaType)
	if err != nil {
		return semanticHTMLBook{}, fmt.Errorf("decode HTML document: %w", err)
	}
	parsed, err := html.Parse(reader)
	if err != nil {
		return semanticHTMLBook{}, fmt.Errorf("parse HTML document: %w", err)
	}
	title := collapseEPUBWhitespace(epubElementText(findEPUBElement(parsed, "title")))
	if title == "" {
		title = strings.TrimSuffix(filepath.Base(filePath), filepath.Ext(filePath))
	}
	author := htmlDocumentAuthor(parsed)
	semanticHTML, err := normalizeSemanticHTMLChapters([]epubChapter{{
		archivePath: filepath.ToSlash(filepath.Base(filePath)),
		mediaType:   mediaType,
		data:        data,
	}}, semanticHTMLOptions{})
	if err != nil {
		return semanticHTMLBook{}, err
	}
	if len(semanticHTML) > maxPreviewDocumentSize {
		return semanticHTMLBook{}, fmt.Errorf("HTML semantic text is too large to preview")
	}
	return semanticHTMLBook{Title: title, Author: author, HTML: semanticHTML}, nil
}

func htmlDocumentAuthor(root *html.Node) string {
	if root == nil {
		return ""
	}
	if root.Type == html.ElementNode && strings.EqualFold(root.Data, "meta") {
		name := strings.ToLower(strings.TrimSpace(firstEPUBAttribute(root, "name", "property")))
		switch name {
		case "author", "dc.creator", "dcterms.creator":
			if author := collapseEPUBWhitespace(htmlAttributeValue(root, "content")); author != "" {
				return author
			}
		}
	}
	for child := root.FirstChild; child != nil; child = child.NextSibling {
		if author := htmlDocumentAuthor(child); author != "" {
			return author
		}
	}
	return ""
}
