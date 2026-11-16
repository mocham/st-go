package latexpdf

import "strings"

type latexMathSpan struct {
	contentStart, contentEnd, end int
	display                       bool
}

// All rendering and pagination paths must agree on the end of a formula.
// Text arguments can switch back into math, so a delimiter inside a braced
// group (including \text{...}) cannot close the outer formula.
func parseLatexMathSpan(source string, start int) (latexMathSpan, bool) {
	if start < 0 || start >= len(source) || isEscaped(source, start) {
		return latexMathSpan{}, false
	}
	opening, closing, display := "", "", false
	switch {
	case strings.HasPrefix(source[start:], "$$"):
		opening, closing, display = "$$", "$$", true
	case source[start] == '$':
		opening, closing = "$", "$"
	case strings.HasPrefix(source[start:], `\(`):
		opening, closing = `\(`, `\)`
	case strings.HasPrefix(source[start:], `\[`):
		opening, closing, display = `\[`, `\]`, true
	case strings.HasPrefix(source[start:], `\begin{math}`):
		opening, closing = `\begin{math}`, `\end{math}`
	default:
		return latexMathSpan{}, false
	}
	contentStart := start + len(opening)
	contentEnd := findMathDelimiter(source, closing, contentStart)
	if contentEnd < 0 {
		return latexMathSpan{}, false
	}
	return latexMathSpan{contentStart, contentEnd, contentEnd + len(closing), display}, true
}

func findMathDelimiter(source, closing string, start int) int {
	depth := 0
	for position := start; position < len(source); {
		if depth == 0 && strings.HasPrefix(source[position:], closing) {
			return position
		}
		if source[position] == '\\' {
			// Consume escaped braces, dollars and backslashes as TeX tokens,
			// rather than treating their second byte as a delimiter or group.
			if _, end := readTeXCommand(source, position); end > position {
				position = end
				continue
			}
		}
		switch source[position] {
		case '{':
			depth++
		case '}':
			if depth > 0 {
				depth--
			}
		}
		position++
	}
	return -1
}

func findMathDollar(source string, start int) int {
	return findMathDelimiter(source, "$", start)
}
