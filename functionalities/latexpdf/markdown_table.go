package latexpdf

import "strings"

// Pipe tables use a private environment in the renderer's LaTeX subset, so
// cells stay ordinary selectable text with inline math, not one giant formula.
func markdownTable(lines []string, start int) (string, int, bool) {
	if start+1 >= len(lines) {
		return "", start, false
	}
	header, headerPipes := markdownTableCells(lines[start])
	delimiter, delimiterPipes := markdownTableCells(lines[start+1])
	if (!headerPipes && !delimiterPipes) || len(header) == 0 || len(header) != len(delimiter) {
		return "", start, false
	}
	var alignment strings.Builder
	for _, cell := range delimiter {
		value := strings.TrimSuffix(strings.TrimPrefix(cell, ":"), ":")
		if value == "" || strings.Trim(value, "-") != "" {
			return "", start, false
		}
		align := byte('l')
		if strings.HasSuffix(cell, ":") {
			align = 'r'
			if strings.HasPrefix(cell, ":") {
				align = 'c'
			}
		}
		alignment.WriteByte(align)
	}
	var output strings.Builder
	output.WriteString("\\begin{markdowntable}{" + alignment.String() + "}\n")
	writeRow := func(cells []string) {
		output.WriteByte('{')
		for column := range header {
			cell := ""
			if column < len(cells) {
				cell = cells[column]
			}
			output.WriteString("{" + markdownInline(cell, true) + "}")
		}
		output.WriteString("}\n")
	}
	writeRow(header)
	end := start + 1
	for end+1 < len(lines) {
		cells, pipes := markdownTableCells(lines[end+1])
		if !pipes {
			break
		}
		writeRow(cells)
		end++
	}
	output.WriteString("\\end{markdowntable}\n")
	return output.String(), end, true
}

func markdownTableCells(line string) ([]string, bool) {
	line = strings.TrimSpace(line)
	var cells []string
	start, pipes, lastPipe := 0, false, -1
	for index := 0; index < len(line); index++ {
		// TeX math and code spans can contain literal pipes. Escaped pipes also
		// stay within their cell, including the GFM `a\|b` spelling for code.
		if line[index] == '$' && !isEscaped(line, index) {
			if end := findMathDollar(line, index+1); end >= 0 {
				index = end
				continue
			}
		}
		if strings.HasPrefix(line[index:], `\(`) {
			if end := strings.Index(line[index+2:], `\)`); end >= 0 {
				index += end + 3
				continue
			}
		}
		if line[index] == '\\' && index+1 < len(line) {
			index++
			continue
		}
		if line[index] == '`' {
			if _, end, ok := markdownCodeSpan(line, index); ok {
				index = end - 1
				continue
			}
		}
		if line[index] != '|' {
			continue
		}
		pipes, lastPipe = true, index
		if index != 0 {
			cells = append(cells, strings.TrimSpace(line[start:index]))
		}
		start = index + 1
	}
	if start < len(line) || lastPipe < 0 {
		cells = append(cells, strings.TrimSpace(line[start:]))
	}
	return cells, pipes
}

func markdownCodeSpan(source string, start int) (string, int, bool) {
	width := 0
	for start+width < len(source) && source[start+width] == '`' {
		width++
	}
	for index := start + width; index < len(source); {
		if source[index] != '`' {
			index++
			continue
		}
		end := index
		for end < len(source) && source[end] == '`' {
			end++
		}
		if end-index == width {
			text := strings.ReplaceAll(source[start+width:index], "\n", " ")
			if strings.HasPrefix(text, " ") && strings.HasSuffix(text, " ") && strings.TrimSpace(text) != "" {
				text = text[1 : len(text)-1]
			}
			return text, end, true
		}
		index = end
	}
	return "", start, false
}

func (context *latexRenderContext) renderMarkdownTable(content string) string {
	alignment, position, ok := parseBraced(content, skipTeXSpace(content, 0))
	if !ok {
		return ""
	}
	var output strings.Builder
	output.WriteString(`<div class="markdown-table-scroll"><table class="markdown-table"><thead>`)
	rows := 0
	for {
		row, end, ok := parseBraced(content, skipTeXSpace(content, position))
		if !ok {
			break
		}
		position = end
		if rows == 1 {
			output.WriteString("</thead><tbody>")
		}
		output.WriteString("<tr>")
		tag := "td"
		if rows == 0 {
			tag = "th"
		}
		cursor := 0
		for _, align := range alignment {
			cell, next, ok := parseBraced(row, skipTeXSpace(row, cursor))
			if !ok {
				break
			}
			cursor = next
			value := "left"
			if align == 'c' {
				value = "center"
			} else if align == 'r' {
				value = "right"
			}
			output.WriteString("<" + tag + ` style="text-align:` + value + `">` + context.renderInline(cell) + "</" + tag + ">")
		}
		output.WriteString("</tr>")
		rows++
	}
	if rows <= 1 {
		output.WriteString("</thead><tbody>")
	}
	output.WriteString("</tbody></table></div>")
	return output.String()
}
