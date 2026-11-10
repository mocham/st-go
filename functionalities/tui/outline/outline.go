// Package outline extracts source locations without executing TeX or Markdown.
package outline

import (
	"strings"
	"unicode"
)

const MaxEntries = 4096
const MaxTitleRunes = 256

var texLevels = map[string]int{"chapter": 0, "section": 1, "subsection": 2, "subsubsection": 3, "subsubsubsection": 4, "paragraph": 4, "subparagraph": 4, "label": 4}

type Entry struct {
	Title, Kind         string
	Level, Offset, Line int // zero-based depth/rune offset; one-based source line
}

// TeX recognizes sectioning commands and labels in source order. Labels and
// subsubsubsections share the lowest display level. Comments and verbatim
// regions are not executable source and cannot create outline entries.
func TeX(text []rune) []Entry {
	var entries []Entry
	line := 1
	for i := 0; i < len(text) && len(entries) < MaxEntries; {
		if text[i] == '\n' {
			line++
			i++
			continue
		}
		if text[i] == '%' {
			for i < len(text) && text[i] != '\n' {
				i++
			}
			continue
		}
		if text[i] != '\\' {
			i++
			continue
		}
		start, sourceLine := i, line
		i++
		first := i
		for i < len(text) && (unicode.IsLetter(text[i]) || text[i] == '@') {
			i++
		}
		if first == i {
			if i < len(text) {
				i++
			}
			continue
		}
		name := string(text[first:i])
		if end := definitionEnd(text, i, name); end > i {
			for _, r := range text[i:end] {
				if r == '\n' {
					line++
				}
			}
			i = end
			continue
		}
		if name == "verb" || name == "lstinline" {
			if i < len(text) && text[i] == '*' {
				i++
			}
			if i < len(text) && text[i] != '\n' {
				delimiter := text[i]
				i++
				for i < len(text) && text[i] != delimiter && text[i] != '\n' {
					i++
				}
				if i < len(text) && text[i] == delimiter {
					i++
				}
			}
			continue
		}
		if name == "begin" {
			value, end, ok := argument(text, i, '{', '}')
			if ok {
				env := string(value)
				switch env {
				case "verbatim", "verbatim*", "Verbatim", "lstlisting", "minted", "comment":
					closing := []rune("\\end{" + env + "}")
					to := end
					for to < len(text) && !runePrefix(text[to:], closing) {
						to++
					}
					if to < len(text) {
						to += len(closing)
					}
					for _, r := range text[i:to] {
						if r == '\n' {
							line++
						}
					}
					i = to
				}
			}
			continue
		}
		level, ok := texLevels[name]
		if !ok {
			continue
		}
		at := i
		if at < len(text) && text[at] == '*' {
			at++
		}
		short, next, hasShort := argument(text, at, '[', ']')
		if hasShort {
			at = next
		}
		value, _, valid := argument(text, at, '{', '}')
		if !valid {
			continue
		}
		if hasShort && len(short) > 0 {
			value = short
		}
		title := cleanTitle(value)
		if title == "" {
			continue
		}
		if name == "label" {
			title = "label: " + title
		}
		if title != "" {
			entries = append(entries, Entry{title, name, level, start, sourceLine})
		}
	}
	return entries
}

func definitionEnd(text []rune, at int, name string) int {
	original := at
	switch name {
	case "newcommand", "renewcommand", "providecommand", "DeclareRobustCommand", "newenvironment", "renewenvironment":
		if at < len(text) && text[at] == '*' {
			at++
		}
		if _, end, ok := argument(text, at, '{', '}'); ok {
			at = end
		} else {
			for at < len(text) && unicode.IsSpace(text[at]) {
				at++
			}
			if at >= len(text) || text[at] != '\\' {
				return original
			}
			at++
			for at < len(text) && unicode.IsLetter(text[at]) {
				at++
			}
		}
		for count := 0; count < 2; count++ {
			if _, end, ok := argument(text, at, '[', ']'); ok {
				at = end
			}
		}
		if _, end, ok := argument(text, at, '{', '}'); ok {
			at = end
			if name == "newenvironment" || name == "renewenvironment" {
				if _, end, ok := argument(text, at, '{', '}'); ok {
					at = end
				}
			}
			return at
		}
	case "def", "gdef", "edef", "xdef":
		for at < len(text) && at-original < 1024 && text[at] != '{' {
			at++
		}
		if _, end, ok := argument(text, at, '{', '}'); ok {
			return end
		}
	}
	return original
}

func runePrefix(text, prefix []rune) bool {
	if len(text) < len(prefix) {
		return false
	}
	for i, r := range prefix {
		if text[i] != r {
			return false
		}
	}
	return true
}

func argument(text []rune, at int, open, close rune) ([]rune, int, bool) {
	for at < len(text) {
		if unicode.IsSpace(text[at]) {
			at++
			continue
		}
		if text[at] == '%' {
			for at < len(text) && text[at] != '\n' {
				at++
			}
			continue
		}
		break
	}
	if at >= len(text) || text[at] != open {
		return nil, at, false
	}
	start, depth, braces := at+1, 1, 0
	for at++; at < len(text) && at-start <= 8192; at++ {
		r := text[at]
		if r == '\\' {
			at++
			continue
		}
		if r == '%' {
			for at < len(text) && text[at] != '\n' {
				at++
			}
			continue
		}
		if open == '[' {
			if r == '{' {
				braces++
			}
			if r == '}' {
				braces--
			}
			if braces != 0 {
				continue
			}
		}
		if r == open {
			depth++
		}
		if r == close {
			depth--
			if depth == 0 {
				return text[start:at], at + 1, true
			}
		}
	}
	return nil, at, false
}

func cleanTitle(text []rune) string {
	var out []rune
	for i := 0; i < len(text) && len(out) < MaxTitleRunes; i++ {
		r := text[i]
		if r == '%' {
			for i < len(text) && text[i] != '\n' {
				i++
			}
			r = ' '
		}
		if r == '\\' && i+1 < len(text) {
			i++
			if unicode.IsLetter(text[i]) {
				first := i
				for i < len(text) && unicode.IsLetter(text[i]) {
					i++
				}
				if string(text[first:i]) == "label" {
					if _, end, ok := argument(text, i, '{', '}'); ok {
						i = end
					}
				}
				i--
				continue
			}
			r = text[i]
		}
		if r == '{' || r == '}' || unicode.IsControl(r) && !unicode.IsSpace(r) || unicode.Is(unicode.Cf, r) {
			continue
		}
		if unicode.IsSpace(r) {
			r = ' '
			if len(out) == 0 || out[len(out)-1] == ' ' {
				continue
			}
		}
		out = append(out, r)
	}
	return strings.TrimSpace(string(out))
}

// Markdown accepts ATX headings, ignoring fenced code blocks.
func Markdown(text []rune) []Entry {
	var entries []Entry
	offset, line, fence := 0, 1, ""
	for _, source := range strings.Split(string(text), "\n") {
		trim := strings.TrimSpace(source)
		if strings.HasPrefix(trim, "```") || strings.HasPrefix(trim, "~~~") {
			marker := trim[:3]
			if fence == "" {
				fence = marker
			} else if marker == fence {
				fence = ""
			}
		} else if fence == "" {
			count := len(trim) - len(strings.TrimLeft(trim, "#"))
			if count > 0 && count <= 6 && (len(trim) == count || trim[count] == ' ' || trim[count] == '\t') {
				title := cleanTitle([]rune(strings.TrimSpace(strings.TrimRight(trim[count:], "#"))))
				entries = append(entries, Entry{title, "heading", count - 1, offset, line})
				if len(entries) == MaxEntries {
					break
				}
			}
		}
		offset += len([]rune(source)) + 1
		line++
	}
	return entries
}
