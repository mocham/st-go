package main

import (
	"archive/zip"
	"bytes"
	"encoding/xml"
	"fmt"
	"html"
	"io"
	"net/url"
	"os"
	pathpkg "path"
	"path/filepath"
	"strconv"
	"strings"

	"golang.org/x/net/html/charset"
)

const (
	officePreviewVersion = "office-text-v1"
	maxOfficeArchiveSize = 256 << 20
	maxOfficeEntries     = 65536
	maxOfficeTextSize    = 1 << 20
	maxOfficeStrings     = 100000
	maxOfficeSheets      = 256
)

// Office previews read only XML parts from the ZIP, never extract files or
// follow external relationships. Formatting, images and formula evaluation
// deliberately remain out of scope for this preliminary text-only reader.
type officeArchive struct {
	entries   map[string]*zip.File
	remaining int64
}

func (renderer *previewDocumentRenderer) renderOfficeSized(filePath string, pageWidth, pageHeight float64) (*documentPreview, error) {
	book, err := readOfficeBook(filePath)
	if err != nil {
		return nil, err
	}
	kind := strings.ToUpper(strings.TrimPrefix(filepath.Ext(filePath), "."))
	return renderer.renderSemanticHTMLSized(filePath, book, kind, officePreviewVersion, pageWidth, pageHeight)
}

func readOfficeBook(filePath string) (semanticHTMLBook, error) {
	info, err := os.Stat(filePath)
	if err != nil {
		return semanticHTMLBook{}, err
	}
	if info.Size() > maxOfficeArchiveSize {
		return semanticHTMLBook{}, fmt.Errorf("Office document is too large to preview")
	}
	zr, err := zip.OpenReader(filePath)
	if err != nil {
		return semanticHTMLBook{}, fmt.Errorf("open Office ZIP: %w", err)
	}
	defer zr.Close()
	if len(zr.File) > maxOfficeEntries {
		return semanticHTMLBook{}, fmt.Errorf("Office ZIP has too many entries")
	}
	archive := officeArchive{entries: make(map[string]*zip.File), remaining: maxPreviewDocumentSize}
	for _, file := range zr.File {
		name, err := cleanEPUBArchivePath(file.Name)
		if err != nil || file.FileInfo().IsDir() {
			continue
		}
		if archive.entries[name] != nil {
			return semanticHTMLBook{}, fmt.Errorf("duplicate Office ZIP part %q", name)
		}
		archive.entries[name] = file
	}
	var body string
	switch strings.ToLower(filepath.Ext(filePath)) {
	case ".docx":
		data, readErr := archive.read("word/document.xml")
		if readErr != nil {
			return semanticHTMLBook{}, readErr
		}
		body, err = extractDOCXText(data)
		if err == nil {
			body = officeTextHTML(body)
		}
	case ".xlsx":
		body, err = archive.extractXLSXHTML()
	default:
		err = fmt.Errorf("unsupported Office document")
	}
	if err != nil {
		return semanticHTMLBook{}, err
	}
	if len(body) > maxOfficeTextSize {
		return semanticHTMLBook{}, fmt.Errorf("Office preview text is too large")
	}
	if body == "" {
		body = "<p>No text found in this document.</p>"
	}
	return semanticHTMLBook{
		Title: strings.TrimSuffix(filepath.Base(filePath), filepath.Ext(filePath)),
		HTML:  "<p><em>Text-only preview; formatting and images are omitted.</em></p>" + body,
	}, nil
}

func (archive *officeArchive) read(name string) ([]byte, error) {
	file := archive.entries[name]
	if file == nil {
		return nil, fmt.Errorf("Office ZIP is missing %s", name)
	}
	data, err := readEPUBEntry(file, archive.remaining)
	if err != nil {
		return nil, fmt.Errorf("read Office part %s: %w", name, err)
	}
	archive.remaining -= int64(len(data))
	return data, nil
}

func officeXMLDecoder(data []byte, root string) (*xml.Decoder, error) {
	decoder := xml.NewDecoder(bytes.NewReader(data))
	decoder.CharsetReader = charset.NewReaderLabel
	for {
		token, err := decoder.Token()
		if err != nil {
			return nil, fmt.Errorf("parse Office XML: %w", err)
		}
		if element, ok := token.(xml.StartElement); ok {
			if element.Name.Local != root {
				return nil, fmt.Errorf("expected Office XML %s, found %s", root, element.Name.Local)
			}
			return decoder, nil
		}
	}
}

func extractDOCXText(data []byte) (string, error) {
	decoder, err := officeXMLDecoder(data, "document")
	if err != nil {
		return "", err
	}
	var text strings.Builder
	inBody, foundBody, cellDepth := false, false, 0
	for {
		token, err := decoder.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return "", fmt.Errorf("parse DOCX text: %w", err)
		}
		switch element := token.(type) {
		case xml.StartElement:
			if element.Name.Local == "body" {
				inBody, foundBody = true, true
			}
			if !inBody {
				continue
			}
			switch element.Name.Local {
			case "drawing", "pict", "object", "del":
				if err := decoder.Skip(); err != nil {
					return "", err
				}
			case "tc":
				cellDepth++
			case "t":
				var value string
				if err := decoder.DecodeElement(&value, &element); err != nil {
					return "", err
				}
				text.WriteString(value)
			case "tab":
				text.WriteByte('\t')
			case "br", "cr":
				text.WriteByte('\n')
			}
		case xml.EndElement:
			if !inBody {
				continue
			}
			switch element.Name.Local {
			case "body":
				inBody = false
			case "p":
				if cellDepth > 0 {
					text.WriteByte(' ')
				} else {
					text.WriteByte('\n')
				}
			case "tc":
				cellDepth--
				text.WriteByte('\t')
			case "tr":
				text.WriteByte('\n')
			}
		}
		if text.Len() > maxOfficeTextSize {
			return "", fmt.Errorf("DOCX text is too large to preview")
		}
	}
	if !foundBody {
		return "", fmt.Errorf("DOCX document has no body")
	}
	return text.String(), nil
}

func officeTextHTML(text string) string {
	var body strings.Builder
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if line != "" {
			body.WriteString("<p>")
			body.WriteString(html.EscapeString(strings.ReplaceAll(line, "\t", " | ")))
			body.WriteString("</p>\n")
		}
	}
	return body.String()
}

type officeRelationship struct {
	ID         string `xml:"Id,attr"`
	Target     string `xml:"Target,attr"`
	Type       string `xml:"Type,attr"`
	TargetMode string `xml:"TargetMode,attr"`
}

type xlsxString struct {
	Text string `xml:"t"`
	Runs []struct {
		Text string `xml:"t"`
	} `xml:"r"`
}

func (value xlsxString) text() string {
	var text strings.Builder
	text.WriteString(value.Text)
	for _, run := range value.Runs {
		text.WriteString(run.Text)
	}
	return text.String()
}

type xlsxCell struct {
	Reference string     `xml:"r,attr"`
	Type      string     `xml:"t,attr"`
	Value     string     `xml:"v"`
	Formula   string     `xml:"f"`
	Inline    xlsxString `xml:"is"`
}

func (archive *officeArchive) extractXLSXHTML() (string, error) {
	data, err := archive.read("xl/workbook.xml")
	if err != nil {
		return "", err
	}
	var workbook struct {
		XMLName xml.Name `xml:"workbook"`
		Sheets  []struct {
			Name string `xml:"name,attr"`
			ID   string `xml:"id,attr"`
		} `xml:"sheets>sheet"`
	}
	if err := decodeEPUBXML(data, &workbook); err != nil {
		return "", fmt.Errorf("parse XLSX workbook: %w", err)
	}
	if len(workbook.Sheets) > maxOfficeSheets {
		return "", fmt.Errorf("XLSX has too many sheets")
	}
	data, err = archive.read("xl/_rels/workbook.xml.rels")
	if err != nil {
		return "", err
	}
	var relationships struct {
		XMLName xml.Name             `xml:"Relationships"`
		Items   []officeRelationship `xml:"Relationship"`
	}
	if err := decodeEPUBXML(data, &relationships); err != nil {
		return "", fmt.Errorf("parse XLSX relationships: %w", err)
	}
	byID := make(map[string]officeRelationship)
	var shared []string
	for _, relationship := range relationships.Items {
		byID[relationship.ID] = relationship
		if strings.HasSuffix(relationship.Type, "/sharedStrings") {
			name, err := officePartPath(relationship)
			if err != nil {
				return "", err
			}
			data, err := archive.read(name)
			if err != nil {
				return "", err
			}
			shared, err = extractXLSXSharedStrings(data)
			if err != nil {
				return "", err
			}
		}
	}
	var body strings.Builder
	body.WriteString("<p>Cell values are unformatted; formulas use saved results when available and are never evaluated.</p>")
	for _, sheet := range workbook.Sheets {
		relationship, ok := byID[sheet.ID]
		if !ok {
			return "", fmt.Errorf("XLSX sheet %q has no relationship", sheet.Name)
		}
		if !strings.HasSuffix(relationship.Type, "/worksheet") {
			continue // Chart sheets and other non-tabular parts have no cell text.
		}
		name, err := officePartPath(relationship)
		if err != nil {
			return "", err
		}
		data, err := archive.read(name)
		if err != nil {
			return "", err
		}
		text, err := extractXLSXSheetText(data, shared)
		if err != nil {
			return "", fmt.Errorf("read XLSX sheet %q: %w", sheet.Name, err)
		}
		body.WriteString("<h2>" + html.EscapeString(sheet.Name) + "</h2>")
		if text == "" {
			body.WriteString("<p>Empty worksheet.</p>")
		} else {
			body.WriteString(officeTextHTML(text))
		}
		if body.Len() > maxOfficeTextSize {
			return "", fmt.Errorf("XLSX text is too large to preview")
		}
	}
	return body.String(), nil
}

func officePartPath(relationship officeRelationship) (string, error) {
	parsed, err := url.Parse(relationship.Target)
	if err != nil || parsed.Scheme != "" || parsed.Host != "" || parsed.Path == "" ||
		parsed.RawQuery != "" || parsed.Fragment != "" || strings.EqualFold(relationship.TargetMode, "External") {
		return "", fmt.Errorf("unsupported external or invalid Office relationship")
	}
	name := parsed.Path
	if strings.HasPrefix(name, "/") {
		name = strings.TrimPrefix(name, "/") // OPC absolute paths are ZIP-root-relative.
	} else {
		name = pathpkg.Join("xl", name)
	}
	return cleanEPUBArchivePath(name)
}

func extractXLSXSharedStrings(data []byte) ([]string, error) {
	decoder, err := officeXMLDecoder(data, "sst")
	if err != nil {
		return nil, err
	}
	var shared []string
	for {
		token, err := decoder.Token()
		if err == io.EOF {
			return shared, nil
		}
		if err != nil {
			return nil, fmt.Errorf("parse XLSX shared strings: %w", err)
		}
		if element, ok := token.(xml.StartElement); ok && element.Name.Local == "si" {
			if len(shared) >= maxOfficeStrings {
				return nil, fmt.Errorf("XLSX has too many shared strings")
			}
			var value xlsxString
			if err := decoder.DecodeElement(&value, &element); err != nil {
				return nil, err
			}
			shared = append(shared, value.text())
		}
	}
}

func extractXLSXSheetText(data []byte, shared []string) (string, error) {
	decoder, err := officeXMLDecoder(data, "worksheet")
	if err != nil {
		return "", err
	}
	var text strings.Builder
	rowNumber := 0
	for {
		token, err := decoder.Token()
		if err == io.EOF {
			return text.String(), nil
		}
		if err != nil {
			return "", fmt.Errorf("parse XLSX cells: %w", err)
		}
		element, ok := token.(xml.StartElement)
		if !ok || element.Name.Local != "row" {
			continue
		}
		var row struct {
			Number int        `xml:"r,attr"`
			Cells  []xlsxCell `xml:"c"`
		}
		if err := decoder.DecodeElement(&row, &element); err != nil {
			return "", err
		}
		rowNumber++
		if row.Number > 0 {
			rowNumber = row.Number
		}
		var cells strings.Builder
		for index, cell := range row.Cells {
			value := cell.Value
			switch cell.Type {
			case "s":
				index, err := strconv.Atoi(strings.TrimSpace(value))
				if err != nil || index < 0 || index >= len(shared) {
					return "", fmt.Errorf("invalid XLSX shared string index %q", value)
				}
				value = shared[index]
			case "inlineStr":
				value = cell.Inline.text()
			case "b":
				if value == "1" {
					value = "TRUE"
				} else if value == "0" {
					value = "FALSE"
				}
			}
			if value == "" && cell.Formula != "" {
				value = "=" + cell.Formula
			}
			if value == "" {
				continue
			}
			reference := cell.Reference
			if reference == "" {
				reference = fmt.Sprintf("cell %d", index+1)
			}
			if cells.Len() > 0 {
				cells.WriteString(" | ")
			}
			cells.WriteString(reference + ": " + strings.Join(strings.Fields(value), " "))
			if text.Len()+cells.Len() > maxOfficeTextSize {
				return "", fmt.Errorf("XLSX text is too large to preview")
			}
		}
		if cells.Len() > 0 {
			fmt.Fprintf(&text, "Row %d: %s\n", rowNumber, cells.String())
		}
		if text.Len() > maxOfficeTextSize {
			return "", fmt.Errorf("XLSX text is too large to preview")
		}
	}
}
