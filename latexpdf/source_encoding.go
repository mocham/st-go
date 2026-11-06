package latexpdf

import (
	"strings"
	"unicode/utf8"

	"golang.org/x/text/encoding/charmap"
)

func normalizeLatexSourceEncoding(source string) string {
	return normalizeLatexSourceEncodingWithPreamble(source, source)
}

func normalizeLatexSourceEncodingWithPreamble(source, declarationSource string) string {
	if utf8.ValidString(source) {
		return source
	}
	preamble := declarationSource
	if len(preamble) > 16<<10 {
		preamble = preamble[:16<<10]
	}
	preamble = strings.ToLower(preamble)
	var characterMap *charmap.Charmap
	switch {
	case strings.Contains(preamble, "applemac"), strings.Contains(preamble, "macroman"), strings.Contains(preamble, "macintosh"):
		characterMap = charmap.Macintosh
	case strings.Contains(preamble, "ansinew"), strings.Contains(preamble, "cp1252"), strings.Contains(preamble, "windows-1252"):
		characterMap = charmap.Windows1252
	case strings.Contains(preamble, "latin1"), strings.Contains(preamble, "iso-8859-1"):
		characterMap = charmap.ISO8859_1
	}
	if characterMap == nil {
		return strings.ToValidUTF8(source, "�")
	}
	decoded, err := characterMap.NewDecoder().Bytes([]byte(source))
	if err != nil {
		return strings.ToValidUTF8(source, "�")
	}
	return string(decoded)
}
