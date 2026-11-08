package main

import (
	"encoding/binary"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf16"
)

const (
	docPreviewVersion = "doc-piece-table-v2-range-reads"
	maxDocPieces      = 65536
)

func (renderer *previewDocumentRenderer) renderDOCSized(filePath string, pageWidth, pageHeight float64) (*documentPreview, error) {
	file, err := os.Open(filePath)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("DOC must be a regular file")
	}
	ole, err := openDocOLE(file, info.Size())
	if err != nil {
		return nil, err
	}
	text, err := extractDOCText(ole)
	if err != nil {
		return nil, err
	}
	body := officeTextHTML(text)
	if len(body) > maxOfficeTextSize {
		return nil, fmt.Errorf("DOC preview text is too large")
	}
	if body == "" {
		body = "<p>No text found in this document.</p>"
	}
	book := semanticHTMLBook{
		Title: strings.TrimSuffix(info.Name(), filepath.Ext(info.Name())),
		HTML:  "<p><em>Text-only DOC preview; formatting, images, headers and footnotes are omitted.</em></p>" + body,
	}
	return renderer.renderSemanticHTMLSized(filePath, book, "DOC", docPreviewVersion, pageWidth, pageHeight)
}

// MS-DOC 2.4.1: follow fcClx (pair 33, NOT pair 66), then PlcPcd to
// reconstruct logical text order. fcMin/fcMac are reserved and not a fallback.
// https://learn.microsoft.com/en-us/openspecs/office_file_formats/ms-doc/01d5d8c4-cf9c-4ef9-80fd-439e763cfe01
func extractDOCText(ole *docOLE) (string, error) {
	word, err := ole.stream("WordDocument")
	if err != nil {
		return "", err
	}
	base, err := word.readRange(0, 32)
	if err != nil {
		return "", err
	}
	if binary.LittleEndian.Uint16(base) != 0xa5ec {
		return "", fmt.Errorf("unsupported DOC: not a Word 97–2003 document")
	}
	flags := binary.LittleEndian.Uint16(base[10:])
	if flags&0x0100 != 0 {
		return "", fmt.Errorf("encrypted or password-obfuscated DOC previews are not supported")
	}
	version := binary.LittleEndian.Uint16(base[2:])
	if version < 0x00c1 {
		return "", fmt.Errorf("unsupported DOC version: Word 97 or later is required")
	}
	// Walk the counted FIB sections rather than assuming constant offsets.
	cursor := uint64(32)
	section := func(width int) ([]byte, error) {
		header, err := word.readRange(cursor, 2)
		if err != nil {
			return nil, err
		}
		length := uint64(binary.LittleEndian.Uint16(header)) * uint64(width)
		cursor += 2
		data, err := word.readRange(cursor, length)
		cursor += length
		return data, err
	}
	w, err := section(2)
	if err != nil || len(w) < 28 {
		return "", fmt.Errorf("invalid DOC FibRgW section")
	}
	lw, err := section(4)
	if err != nil || len(lw) < 88 {
		return "", fmt.Errorf("invalid DOC FibRgLw section")
	}
	fcLcb, err := section(8)
	if err != nil || len(fcLcb) < 34*8 {
		return "", fmt.Errorf("DOC has no text piece-table pointer")
	}
	newVersion, err := section(2)
	if err != nil {
		return "", err
	}
	if len(newVersion) > 0 {
		version = binary.LittleEndian.Uint16(newVersion)
	}
	switch version {
	case 0x00c1, 0x00d9, 0x0101, 0x010c, 0x0112:
	default:
		return "", fmt.Errorf("unsupported DOC file-format version %#x", version)
	}
	characters := binary.LittleEndian.Uint32(lw[12:]) // ccpText: main document only.
	if characters > maxOfficeTextSize {
		return "", fmt.Errorf("DOC text is too large to preview")
	}
	tableName := "0Table"
	if flags&0x0200 != 0 {
		tableName = "1Table"
	}
	table, err := ole.stream(tableName)
	if err != nil {
		return "", err
	}
	offset := uint64(binary.LittleEndian.Uint32(fcLcb[33*8:]))
	length := uint64(binary.LittleEndian.Uint32(fcLcb[33*8+4:]))
	if length == 0 {
		return "", fmt.Errorf("invalid DOC text piece-table range")
	}
	clx, err := table.readRange(offset, length)
	if err != nil {
		return "", err
	}
	return extractDOCPieces(word, word.size, clx, characters)
}

func extractDOCPieces(word io.ReaderAt, wordSize uint64, clx []byte, characters uint32) (string, error) {
	// Optional Prc records precede the Pcdt; their formatting is irrelevant.
	for len(clx) > 0 && clx[0] == 1 {
		if len(clx) < 3 {
			return "", fmt.Errorf("truncated DOC formatting record")
		}
		length := int(binary.LittleEndian.Uint16(clx[1:]))
		if length > len(clx)-3 {
			return "", fmt.Errorf("truncated DOC formatting record")
		}
		clx = clx[3+length:]
	}
	if len(clx) < 5 || clx[0] != 2 || uint64(binary.LittleEndian.Uint32(clx[1:])) != uint64(len(clx)-5) {
		return "", fmt.Errorf("invalid DOC piece table")
	}
	pieces := clx[5:]
	if len(pieces) < 4 || (len(pieces)-4)%12 != 0 {
		return "", fmt.Errorf("invalid DOC piece-table size")
	}
	count := (len(pieces) - 4) / 12
	if count > maxDocPieces || binary.LittleEndian.Uint32(pieces) != 0 ||
		binary.LittleEndian.Uint32(pieces[count*4:]) < characters || characters > maxOfficeTextSize {
		return "", fmt.Errorf("invalid or oversized DOC character ranges")
	}
	units := make([]uint16, 0, int(characters))
	for index := 0; index < count; index++ {
		startCP := binary.LittleEndian.Uint32(pieces[index*4:])
		endCP := binary.LittleEndian.Uint32(pieces[(index+1)*4:])
		if endCP < startCP || endCP > 0x7fffffff {
			return "", fmt.Errorf("invalid DOC piece character order")
		}
		if startCP >= characters {
			continue
		}
		pcd := pieces[(count+1)*4+index*8:]
		fc := binary.LittleEndian.Uint32(pcd[2:])
		offset, width := uint64(fc&0x3fffffff), uint64(2)
		if fc&0x40000000 != 0 {
			offset, width = offset/2, 1
		}
		length := uint64(min(endCP, characters)-startCP) * width
		data, err := readDOCRange(word, wordSize, offset, length)
		if err != nil {
			return "", err
		}
		if width == 1 {
			for _, value := range data {
				units = append(units, docCompressedCharacter(value))
			}
		} else {
			for i := 0; i < len(data); i += 2 {
				units = append(units, binary.LittleEndian.Uint16(data[i:]))
			}
		}
	}
	return normalizeDOCText(units)
}

// MS-DOC FcCompressed maps these bytes specially; all others are their
// same-valued Unicode characters (not a locale-dependent ANSI code page).
func docCompressedCharacter(value byte) uint16 {
	if value >= 0x80 && value <= 0x9f {
		mapped := [...]uint16{
			0x0080, 0x0081, 0x201a, 0x0192, 0x201e, 0x2026, 0x2020, 0x2021,
			0x02c6, 0x2030, 0x0160, 0x2039, 0x0152, 0x008d, 0x008e, 0x008f,
			0x0090, 0x2018, 0x2019, 0x201c, 0x201d, 0x2022, 0x2013, 0x2014,
			0x02dc, 0x2122, 0x0161, 0x203a, 0x0153, 0x009d, 0x009e, 0x0178,
		}
		return mapped[value-0x80]
	}
	return uint16(value)
}

func normalizeDOCText(units []uint16) (string, error) {
	var text strings.Builder
	var fields []bool // false = field instruction, true = saved display result.
	hidden := 0
	// Decode together so UTF-16 surrogate pairs may straddle piece boundaries.
	for _, value := range utf16.Decode(units) {
		switch value {
		case 0x13:
			if len(fields) >= 64 {
				return "", fmt.Errorf("DOC fields are nested too deeply")
			}
			fields = append(fields, false)
			hidden++
			continue
		case 0x14:
			if len(fields) > 0 && !fields[len(fields)-1] {
				fields[len(fields)-1] = true
				hidden--
			}
			continue
		case 0x15:
			if len(fields) > 0 {
				if !fields[len(fields)-1] {
					hidden--
				}
				fields = fields[:len(fields)-1]
			}
			continue
		}
		if hidden > 0 {
			continue
		}
		switch value {
		case '\r', '\n', '\v', '\f', 0x0e:
			text.WriteByte('\n')
		case '\t', 0x07:
			text.WriteByte('\t')
		case 0x1e:
			text.WriteRune('\u2011') // Nonbreaking hyphen.
		case 0x1f:
			text.WriteRune('\u00ad') // Optional hyphen.
		default:
			if value >= 0x20 && !(value >= 0x7f && value <= 0x9f) {
				text.WriteRune(value)
			}
		}
		if text.Len() > maxOfficeTextSize {
			return "", fmt.Errorf("DOC text is too large to preview")
		}
	}
	return text.String(), nil
}
