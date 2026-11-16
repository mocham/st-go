package latexpdf

import "strings"

// The engine treats text arguments as literal strings, including embedded math
// delimiters. Split explicit text/math switches into adjacent nodes so labels
// such as \text{for $n>0$} contain actual mathematics rather than dollar signs.
func rewriteMathText(source string) string {
	var output strings.Builder
	for position := 0; position < len(source); {
		if source[position] != '\\' {
			output.WriteByte(source[position])
			position++
			continue
		}
		command, end := readTeXCommand(source, position)
		if end <= position {
			output.WriteString(source[position:])
			break
		}
		if isMathTextCommand(command) {
			if content, next, ok := parseBraced(source, skipTeXSpace(source, end)); ok {
				rewritten, changed := rewriteMathTextContent(content, command)
				if changed {
					output.WriteString("{" + rewritten + "}")
				} else {
					output.WriteString(source[position:next])
				}
				position = next
				continue
			}
		}
		output.WriteString(source[position:end])
		position = end
	}
	return output.String()
}

func isMathTextCommand(command string) bool {
	switch command {
	case "text", "textrm", "textnormal", "textbf", "textit", "textsf", "texttt", "mbox", "hbox":
		return true
	}
	return false
}

func rewriteMathTextContent(source, command string) (string, bool) {
	var output strings.Builder
	start, position := 0, 0
	changed := false
	flush := func(end int) {
		if end > start {
			output.WriteString(`\` + command + `{` + source[start:end] + `}`)
		}
	}
	for position < len(source) {
		if span, ok := parseLatexMathSpan(source, position); ok && !span.display {
			flush(position)
			output.WriteString("{" + rewriteMathText(source[span.contentStart:span.contentEnd]) + "}")
			position, start, changed = span.end, span.end, true
			continue
		}
		if source[position] == '{' {
			if content, end, ok := parseBraced(source, position); ok {
				nested, nestedChanged := rewriteMathTextContent(content, command)
				if nestedChanged {
					flush(position)
					output.WriteString("{" + nested + "}")
					start, changed = end, true
				}
				position = end
				continue
			}
		}
		if source[position] == '\\' {
			name, end := readTeXCommand(source, position)
			if end > position {
				if name == "ensuremath" || isMathTextCommand(name) {
					if content, next, ok := parseBraced(source, skipTeXSpace(source, end)); ok {
						flush(position)
						if name == "ensuremath" {
							output.WriteString("{" + rewriteMathText(content) + "}")
						} else {
							nested, _ := rewriteMathTextContent(content, name)
							output.WriteString("{" + nested + "}")
						}
						position, start, changed = next, next, true
						continue
					}
				}
				position = end // In particular, an escaped dollar is prose.
				continue
			}
		}
		position++
	}
	flush(len(source))
	return output.String(), changed
}
