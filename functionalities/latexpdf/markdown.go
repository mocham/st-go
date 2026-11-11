package latexpdf

import (
	"strings"
	"unicode"
	"unicode/utf8"
)

// MarkdownToLaTeX converts common Markdown structure and TeX math delimiters
// into the LaTeX subset understood by Renderer. Inline $...$ and \(...\)
// expressions, display math, and document math-macro declarations are kept as
// TeX so they use the same MathJax and native vector-PDF pipeline as .tex files.
func MarkdownToLaTeX(source string) string {
	source = strings.ReplaceAll(source, "\r\n", "\n")
	source = strings.ReplaceAll(source, "\r", "\n")
	lines := strings.Split(source, "\n")

	var output strings.Builder
	output.WriteString("\\documentclass{article}\n\\begin{document}\n")
	var paragraph []string
	listEnvironment := ""
	frontMatter := len(lines) > 0 && strings.TrimSpace(lines[0]) == "---"

	closeList := func() {
		if listEnvironment != "" {
			output.WriteString("\\end{" + listEnvironment + "}\n")
			listEnvironment = ""
		}
	}
	flushParagraph := func() {
		if len(paragraph) == 0 {
			return
		}
		closeList()
		text := strings.TrimSpace(strings.Join(paragraph, " "))
		if text != "" {
			output.WriteString(markdownInlineToLaTeX(text))
			output.WriteString("\n\n")
		}
		paragraph = nil
	}
	writeBlock := func(value string) {
		flushParagraph()
		closeList()
		output.WriteString(value)
		if !strings.HasSuffix(value, "\n") {
			output.WriteByte('\n')
		}
	}

	for index := 0; index < len(lines); index++ {
		line := lines[index]
		trimmed := strings.TrimSpace(line)
		if frontMatter {
			if index > 0 && trimmed == "---" {
				frontMatter = false
			}
			continue
		}
		if strings.HasPrefix(trimmed, "```") || strings.HasPrefix(trimmed, "~~~") {
			flushParagraph()
			closeList()
			width := 3
			for width < len(trimmed) && trimmed[width] == trimmed[0] {
				width++
			}
			fence := trimmed[:width]
			var code []string
			for index++; index < len(lines); index++ {
				closing := strings.TrimSpace(lines[index])
				if strings.HasPrefix(closing, fence) && strings.TrimSpace(strings.TrimLeft(closing, string(fence[0]))) == "" {
					break
				}
				code = append(code, lines[index])
			}
			output.WriteString("\\begin{verbatim}\n")
			output.WriteString(strings.ReplaceAll(strings.Join(code, "\n"), `\end{verbatim}`, `\end\{verbatim\}`))
			output.WriteString("\n\\end{verbatim}\n")
			continue
		}
		if expression, end, ok := markdownDisplayMath(lines, index, "$$", "$$"); ok {
			writeBlock("\\[\n" + expression + "\n\\]\n")
			index = end
			continue
		}
		if expression, end, ok := markdownDisplayMath(lines, index, `\[`, `\]`); ok {
			writeBlock("\\[\n" + expression + "\n\\]\n")
			index = end
			continue
		}
		if isMarkdownMathDeclaration(trimmed) {
			writeBlock(trimmed + "\n")
			continue
		}
		if table, end, ok := markdownTable(lines, index); ok {
			writeBlock(table)
			index = end
			continue
		}
		if level, title, ok := markdownHeading(trimmed); ok {
			commands := []string{"section", "subsection", "subsubsection", "paragraph"}
			command := commands[min(level, len(commands))-1]
			writeBlock("\\" + command + "*{" + markdownInlineToLaTeX(title) + "}\n")
			continue
		}
		if marker, item, ok := markdownListItem(trimmed); ok {
			flushParagraph()
			environment := "itemize"
			if marker == "ordered" {
				environment = "enumerate"
			}
			if listEnvironment != environment {
				closeList()
				listEnvironment = environment
				output.WriteString("\\begin{" + environment + "}\n")
			}
			output.WriteString("\\item " + markdownInlineToLaTeX(item) + "\n")
			continue
		}
		if strings.HasPrefix(trimmed, ">") {
			writeBlock("\\begin{quote}\n" + markdownInlineToLaTeX(strings.TrimSpace(strings.TrimPrefix(trimmed, ">"))) + "\n\\end{quote}\n")
			continue
		}
		if markdownHorizontalRule(trimmed) {
			flushParagraph()
			closeList()
			output.WriteByte('\n')
			continue
		}
		if trimmed == "" {
			flushParagraph()
			closeList()
			continue
		}
		paragraph = append(paragraph, trimmed)
	}
	flushParagraph()
	closeList()
	output.WriteString("\\end{document}\n")
	return output.String()
}

func markdownDisplayMath(lines []string, start int, opening, closing string) (string, int, bool) {
	trimmed := strings.TrimSpace(lines[start])
	if !strings.HasPrefix(trimmed, opening) {
		return "", start, false
	}
	rest := strings.TrimPrefix(trimmed, opening)
	if position := strings.Index(rest, closing); position >= 0 {
		return strings.TrimSpace(rest[:position]), start, true
	}
	content := make([]string, 0, 4)
	if strings.TrimSpace(rest) != "" {
		content = append(content, rest)
	}
	for index := start + 1; index < len(lines); index++ {
		if position := strings.Index(lines[index], closing); position >= 0 {
			content = append(content, lines[index][:position])
			return strings.TrimSpace(strings.Join(content, "\n")), index, true
		}
		content = append(content, lines[index])
	}
	return "", start, false
}

func markdownHeading(line string) (int, string, bool) {
	level := 0
	for level < len(line) && level < 6 && line[level] == '#' {
		level++
	}
	if level == 0 || level >= len(line) || !unicode.IsSpace(rune(line[level])) {
		return 0, "", false
	}
	title := strings.TrimSpace(line[level:])
	title = strings.TrimSpace(strings.TrimRight(title, "#"))
	return level, title, title != ""
}

func markdownListItem(line string) (marker, item string, ok bool) {
	for _, prefix := range []string{"- ", "* ", "+ "} {
		if strings.HasPrefix(line, prefix) {
			return "unordered", strings.TrimSpace(line[len(prefix):]), true
		}
	}
	position := 0
	for position < len(line) && line[position] >= '0' && line[position] <= '9' {
		position++
	}
	if position > 0 && position+1 < len(line) && line[position] == '.' && unicode.IsSpace(rune(line[position+1])) {
		return "ordered", strings.TrimSpace(line[position+1:]), true
	}
	return "", "", false
}

func markdownHorizontalRule(line string) bool {
	compact := strings.ReplaceAll(strings.ReplaceAll(line, " ", ""), "\t", "")
	if len(compact) < 3 {
		return false
	}
	return strings.Trim(compact, "-") == "" || strings.Trim(compact, "*") == "" || strings.Trim(compact, "_") == ""
}

func isMarkdownMathDeclaration(line string) bool {
	for _, prefix := range []string{`\newcommand`, `\renewcommand`, `\providecommand`, `\DeclareMathOperator`, `\def`, `\gdef`} {
		if strings.HasPrefix(line, prefix) && commandBoundary(line, len(prefix)) {
			return true
		}
	}
	return false
}

func markdownInlineToLaTeX(source string) string {
	return markdownInline(source, false)
}

func markdownInline(source string, tableCell bool) string {
	var output strings.Builder
	for position := 0; position < len(source); {
		switch {
		case strings.HasPrefix(source[position:], `\(`):
			if relative := strings.Index(source[position+2:], `\)`); relative >= 0 {
				end := position + 2 + relative
				output.WriteString(source[position : end+2])
				position = end + 2
				continue
			}
		case source[position] == '$' && !isEscaped(source, position):
			if end := findMathDollar(source, position+1); end >= 0 {
				output.WriteString(source[position : end+1])
				position = end + 1
				continue
			}
		case source[position] == '`':
			if code, end, ok := markdownCodeSpan(source, position); ok {
				if tableCell {
					code = strings.ReplaceAll(code, `\|`, `|`)
				}
				output.WriteString(`\texttt{` + escapeMarkdownText(code) + `}`)
				position = end
				continue
			}
		case strings.HasPrefix(source[position:], "!["):
			if label, _, end, ok := markdownLinkAt(source, position+1); ok {
				output.WriteString(markdownInline(label, tableCell))
				position = end
				continue
			}
		case source[position] == '[':
			if label, target, end, ok := markdownLinkAt(source, position); ok {
				output.WriteString(`\href{` + escapeMarkdownURL(target) + `}{` + markdownInline(label, tableCell) + `}`)
				position = end
				continue
			}
		case strings.HasPrefix(source[position:], "**") || strings.HasPrefix(source[position:], "__"):
			marker := source[position : position+2]
			if relative := strings.Index(source[position+2:], marker); relative >= 0 {
				end := position + 2 + relative
				output.WriteString(`\textbf{` + markdownInline(source[position+2:end], tableCell) + `}`)
				position = end + 2
				continue
			}
		case source[position] == '*' || source[position] == '_':
			marker := source[position]
			if relative := strings.IndexByte(source[position+1:], marker); relative >= 0 {
				end := position + 1 + relative
				output.WriteString(`\emph{` + markdownInline(source[position+1:end], tableCell) + `}`)
				position = end + 1
				continue
			}
		case source[position] == '<':
			if relative := strings.IndexByte(source[position+1:], '>'); relative >= 0 {
				tag := strings.ToLower(strings.ReplaceAll(source[position:position+relative+2], " ", ""))
				if tag == "<br>" || tag == "<br/>" {
					output.WriteString(`\\`)
				}
				position += relative + 2
				continue
			}
		case source[position] == '\\' && position+1 < len(source):
			next, size := utf8.DecodeRuneInString(source[position+1:])
			if strings.ContainsRune(`\`+"`*_{}[]()#+-.!|", next) {
				output.WriteString(escapeMarkdownText(string(next)))
				position += 1 + size
				continue
			}
		}
		_, size := utf8.DecodeRuneInString(source[position:])
		if size <= 0 {
			break
		}
		output.WriteString(escapeMarkdownText(source[position : position+size]))
		position += size
	}
	return output.String()
}

func markdownLinkAt(source string, start int) (label, target string, end int, ok bool) {
	if start >= len(source) || source[start] != '[' {
		return "", "", start, false
	}
	labelEnd := strings.IndexByte(source[start+1:], ']')
	if labelEnd < 0 {
		return "", "", start, false
	}
	labelEnd += start + 1
	if labelEnd+1 >= len(source) || source[labelEnd+1] != '(' {
		return "", "", start, false
	}
	targetEnd := strings.IndexByte(source[labelEnd+2:], ')')
	if targetEnd < 0 {
		return "", "", start, false
	}
	targetEnd += labelEnd + 2
	return source[start+1 : labelEnd], source[labelEnd+2 : targetEnd], targetEnd + 1, true
}

func escapeMarkdownText(source string) string {
	var output strings.Builder
	for _, character := range source {
		switch character {
		case '\\':
			output.WriteString(`\textbackslash{}`)
		case '%', '$', '&', '_', '#', '{', '}':
			output.WriteByte('\\')
			output.WriteRune(character)
		case '~':
			output.WriteString(`\textasciitilde{}`)
		case '^':
			output.WriteString(`\textasciicircum{}`)
		default:
			output.WriteRune(character)
		}
	}
	return output.String()
}

func escapeMarkdownURL(source string) string {
	return strings.NewReplacer(`\`, `/`, `{`, `%7B`, `}`, `%7D`).Replace(strings.TrimSpace(source))
}
