package latexpdf

import (
	"fmt"
	"html/template"
	"strings"
)

type beamerDocument struct {
	title, subtitle, author, institute, date string
}

func parseBeamerDocument(source string) *beamerDocument {
	source = stripLatexComments(source)
	if start := strings.Index(source, `\begin{document}`); start >= 0 {
		source = source[:start]
	}
	for position := 0; position < len(source); {
		relative := strings.IndexByte(source[position:], '\\')
		if relative < 0 {
			break
		}
		position += relative
		command, end := readTeXCommand(source, position)
		if end <= position {
			position++
			continue
		}
		position = end
		if command != "documentclass" {
			continue
		}
		cursor := skipOptionalArguments(source, end)
		name, _, ok := parseBraced(source, cursor)
		if !ok || strings.TrimSpace(name) != "beamer" {
			return nil
		}
		document := &beamerDocument{}
		for name, target := range map[string]*string{"title": &document.title, "subtitle": &document.subtitle, "author": &document.author, "institute": &document.institute, "date": &document.date} {
			*target = beamerMetadata(source, name)
		}
		return document
	}
	return nil
}

func beamerMetadata(source, name string) string {
	needle := `\` + name
	for position := 0; position < len(source); {
		relative := strings.Index(source[position:], needle)
		if relative < 0 {
			break
		}
		end := position + relative + len(needle)
		position = end
		if !commandBoundary(source, end) {
			continue
		}
		if value, _, ok := parseBraced(source, skipOptionalArguments(source, end)); ok {
			return value
		}
	}
	return ""
}

// Keep offsets intact when lifting explicit frame titles out of the body: the
// document index still needs the original positions for equations and labels.
func beamerFrameHeading(content string, titleArgument bool) (title, subtitle, body string, consumed int) {
	cursor := skipTeXSpace(content, 0)
	if value, end, ok := parseBraced(content, cursor); ok && titleArgument {
		title, consumed = value, end
		if value, end, ok = parseBraced(content, skipTeXSpace(content, end)); ok {
			subtitle, consumed = value, end
		}
	}
	body = content[consumed:]
	for position := 0; position < len(body); {
		if body[position] != '\\' {
			position++
			continue
		}
		command, end := readTeXCommand(body, position)
		if end <= position {
			position++
			continue
		}
		if command == "frametitle" || command == "framesubtitle" {
			cursor := skipOptionalArguments(body, skipOverlay(body, end))
			if value, finish, ok := parseBraced(body, cursor); ok {
				if command == "frametitle" {
					title = value
				} else {
					subtitle = value
				}
				body = body[:position] + strings.Repeat(" ", finish-position) + body[finish:]
				end = finish
			}
		}
		position = end
	}
	return
}

func (context *latexRenderContext) renderBeamerFrame(content string, offset, contentOffset int, titleArgument bool) string {
	context.frameDepth++
	defer func() { context.frameDepth-- }()
	title, subtitle, body, consumed := beamerFrameHeading(content, titleArgument)
	id := fmt.Sprintf("frame-%d", offset)
	label := collapseWhitespace(texFragmentToPlainText(title))
	if label == "" {
		label = "Slide"
	}
	context.sections = append(context.sections, documentSection{ID: id, Title: label, Level: 2})
	var output strings.Builder
	style := "border:1px solid #8092a1;padding:1em;margin:1.25em 0;box-sizing:border-box"
	output.WriteString(`<section class="beamer-frame" id="` + id + `" style="` + style + `">`)
	if title != "" {
		output.WriteString(`<h2 class="beamer-frametitle">` + context.renderInline(title) + `</h2>`)
	}
	if subtitle != "" {
		output.WriteString(`<h3 class="beamer-framesubtitle">` + context.renderInline(subtitle) + `</h3>`)
	}
	output.WriteString(context.renderBlocksAt(body, contentOffset+consumed))
	output.WriteString(`</section>`)
	return output.String()
}

func (context *latexRenderContext) renderBeamerTitle() string {
	if context.beamer == nil {
		return ""
	}
	var output strings.Builder
	output.WriteString(`<div class="beamer-titlepage" style="text-align:center">`)
	for _, entry := range []struct{ tag, text string }{{"h1", context.beamer.title}, {"h3", context.beamer.subtitle}, {"p", context.beamer.author}, {"p", context.beamer.institute}, {"p", context.beamer.date}} {
		if entry.text != "" {
			output.WriteString("<" + entry.tag + ">" + context.renderInline(entry.text) + "</" + entry.tag + ">")
		}
	}
	output.WriteString(`</div>`)
	return output.String()
}

func (context *latexRenderContext) renderColumn(content string, contentOffset int) string {
	width := "100%"
	if value, end, ok := parseBraced(content, skipTeXSpace(content, 0)); ok {
		if css, valid := texLengthCSS(value); valid {
			width = css
		}
		content, contentOffset = content[end:], contentOffset+end
	}
	return `<div class="beamer-column" style="min-width:0;flex:0 1 ` + width + `;width:` + width + `">` + context.renderBlocksAt(content, contentOffset) + `</div>`
}

func (context *latexRenderContext) renderBeamerBlock(base, content string, contentOffset int) string {
	title, end, ok := parseBraced(content, skipTeXSpace(content, 0))
	if !ok {
		return context.renderBlocksAt(content, contentOffset)
	}
	color := "#17344d"
	if base == "alertblock" {
		color = "#a83232"
	} else if base == "exampleblock" {
		color = "#20754a"
	}
	return `<section class="beamer-block ` + base + `" style="border:1px solid ` + color + `;padding:.5em;margin:.5em 0"><h3 style="color:` + color + `">` + context.renderInline(title) + `</h3>` + context.renderBlocksAt(content[end:], contentOffset+end) + `</section>`
}

func (context *latexRenderContext) renderColumns(content, options string, contentOffset int) string {
	align := "center"
	for _, option := range strings.Split(options, ",") {
		if strings.TrimSpace(option) == "T" || strings.TrimSpace(option) == "t" {
			align = "flex-start"
		}
		if strings.TrimSpace(option) == "b" {
			align = "flex-end"
		}
	}
	var rendered strings.Builder
	// Beamer accepts both column environments and \column[alignment]{width}.
	type columnStart struct{ start, body int }
	var columns []columnStart
	for position := 0; position < len(content); {
		if content[position] == '{' {
			if _, end, ok := parseBraced(content, position); ok {
				position = end
				continue
			}
		}
		if content[position] != '\\' {
			position++
			continue
		}
		if environment, beginEnd, ok := parseEnvironmentStart(content[position:]); ok {
			if _, end, found := extractEnvironment(content[position:], environment, beginEnd); found {
				position += end
				continue
			}
		}
		command, end := readTeXCommand(content, position)
		if end <= position {
			position++
			continue
		}
		if command == "column" {
			cursor := skipOptionalArguments(content, skipOverlay(content, end))
			if _, _, ok := parseBraced(content, cursor); ok {
				columns = append(columns, columnStart{position, cursor})
			}
		}
		position = end
	}
	if len(columns) == 0 {
		rendered.WriteString(context.renderBlocksAt(content, contentOffset))
	} else {
		rendered.WriteString(context.renderBlocksAt(content[:columns[0].start], contentOffset))
		for index, column := range columns {
			end := len(content)
			if index+1 < len(columns) {
				end = columns[index+1].start
			}
			rendered.WriteString(context.renderColumn(content[column.body:end], contentOffset+column.body))
		}
	}
	return `<div class="beamer-columns" style="display:flex;justify-content:space-between;gap:1%;align-items:` + template.HTMLEscapeString(align) + `">` + rendered.String() + `</div>`
}
