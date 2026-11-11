package latexpdf

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"html/template"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"unicode"
	"unicode/utf8"

	mathjax "github.com/d2lang/mathjax-go"
	"golang.org/x/text/unicode/norm"
)

type latexRenderer struct {
	documents *boundedCache[renderedLatex]
	formulae  *boundedCache[renderedFormula]
	mathMu    sync.Mutex
}

type latexRenderContext struct {
	renderer         *latexRenderer
	macros           map[string]mathMacro
	primitiveMacros  map[string]bool
	colors           map[string]renderColor
	errors           int
	errorExpressions []string
	sections         []documentSection
	flowcharts       int
	index            *latexDocumentIndex
	bodyOffset       int
}

type mathMacro struct {
	body            string
	arguments       int
	optionalDefault string
	hasOptional     bool
}

type renderedFormula struct {
	html     string
	hadError bool
}

func newLatexRenderer() *latexRenderer {
	return &latexRenderer{
		documents: newBoundedCache[renderedLatex](32),
		formulae:  newBoundedCache[renderedFormula](1024),
	}
}

func (renderer *latexRenderer) render(source, digest string) renderedLatex {
	source = normalizeLatexSourceEncoding(source)
	prepared := preprocessKnownConditionals(stripLatexComments(source))
	prepared = rewriteEditorialCommands(prepared)
	return renderer.renderPrepared(prepared, documentBody(prepared), digest)
}

func (renderer *latexRenderer) renderPrepared(prepared, body, digest string) renderedLatex {
	if cached, ok := renderer.documents.Load(digest); ok {
		return cached
	}
	body = normalizeLatexSourceEncodingWithPreamble(body, prepared)
	prepared = normalizeLatexSourceEncoding(prepared)
	if strings.Contains(prepared, `\DIF`) {
		prepared = rewriteLatexDiffDocument(prepared)
		body = rewriteLatexDiffCommands(body)
	}
	prepared, body = rewriteTableMacroDocuments(prepared, body)
	index := buildLatexDocumentIndex(prepared)
	bodyOffset := strings.Index(index.body, body)
	if bodyOffset < 0 {
		bodyOffset = 0
	}
	context := &latexRenderContext{
		renderer:        renderer,
		macros:          extractMathMacros(prepared),
		primitiveMacros: extractPrimitiveMacroNames(prepared),
		colors:          extractLatexColors(prepared),
		index:           index,
		bodyOffset:      bodyOffset,
	}
	html := context.renderBlocksAt(body, bodyOffset)
	result := renderedLatex{
		Body: template.HTML(html), MathErrors: context.errors,
		MathErrorExpressions: context.errorExpressions, Sections: context.sections,
	}
	renderer.documents.Store(digest, result)
	return result
}

func rewriteLatexDiffDocument(source string) string {
	const begin = `\begin{document}`
	const end = `\end{document}`
	start := strings.Index(source, begin)
	if start < 0 {
		return rewriteLatexDiffCommands(source)
	}
	start += len(begin)
	finish := strings.LastIndex(source[start:], end)
	if finish < 0 {
		finish = len(source)
	} else {
		finish += start
	}
	return source[:start] + rewriteLatexDiffCommands(source[start:finish]) + source[finish:]
}

func rewriteLatexDiffCommands(source string) string {
	wrappers := map[string]bool{
		"DIFadd": true, "DIFdel": true, "DIFaddtex": true, "DIFdeltex": true,
		"DIFaddFL": true, "DIFdelFL": true,
	}
	markers := map[string]bool{
		"DIFaddbegin": true, "DIFaddend": true, "DIFdelbegin": true, "DIFdelend": true,
		"DIFaddbeginFL": true, "DIFaddendFL": true, "DIFdelbeginFL": true, "DIFdelendFL": true,
	}
	for pass := 0; pass < 32; pass++ {
		var output strings.Builder
		changed := false
		for position := 0; position < len(source); {
			relative := strings.Index(source[position:], `\DIF`)
			if relative < 0 {
				output.WriteString(source[position:])
				break
			}
			start := position + relative
			output.WriteString(source[position:start])
			name, commandEnd := readTeXCommand(source, start)
			if wrappers[name] {
				cursor := skipTeXSpace(source, commandEnd)
				if argument, end, ok := parseBraced(source, cursor); ok {
					output.WriteByte('{')
					output.WriteString(argument)
					output.WriteByte('}')
					position = end
					changed = true
					continue
				}
			}
			if markers[name] {
				position = commandEnd
				changed = true
				continue
			}
			if commandEnd <= start {
				output.WriteByte(source[start])
				position = start + 1
			} else {
				output.WriteString(source[start:commandEnd])
				position = commandEnd
			}
		}
		source = output.String()
		if !changed {
			break
		}
	}
	return source
}

func preprocessKnownConditionals(source string) string {
	source = selectTeXConditional(source, `\iffalse`, false)
	source = selectTeXConditional(source, `\iftrue`, true)
	source = selectDefinedTeXConditionals(source)
	showSolutions := strings.Contains(source, `\showsolutionstrue`) && !strings.Contains(source, `\showsolutionsfalse`)
	source = regexp.MustCompile(`\\newif\s*\\ifshowsolutions`).ReplaceAllString(source, "")
	source = selectTeXConditional(source, `\ifshowsolutions`, showSolutions)
	source = strings.NewReplacer(
		`\showsolutionstrue`, "",
		`\showsolutionsfalse`, "",
	).Replace(source)
	return source
}

func selectDefinedTeXConditionals(source string) string {
	for {
		start := strings.Index(source, `\ifdefined`)
		if start < 0 {
			return source
		}
		cursor := skipTeXSpace(source, start+len(`\ifdefined`))
		if cursor >= len(source) || source[cursor] != '\\' {
			return source
		}
		name, end := readTeXCommand(source, cursor)
		if name == "" || end <= cursor {
			return source
		}
		opening := source[start:end]
		selected := selectTeXConditional(source, opening, texMacroDefined(source[:start], name))
		if selected == source {
			return source
		}
		source = selected
	}
}

func texMacroDefined(source, name string) bool {
	escaped := regexp.QuoteMeta(name)
	direct := regexp.MustCompile(`\\(?:def|gdef|edef|xdef|let)\s*\\` + escaped + `(?:[^A-Za-z@]|$)`)
	if direct.MatchString(source) {
		return true
	}
	command := regexp.MustCompile(`\\(?:newcommand|renewcommand|providecommand)\*?\s*(?:\{\s*\\` + escaped + `\s*\}|\\` + escaped + `(?:[^A-Za-z@]|$))`)
	return command.MatchString(source)
}

func rewriteEditorialCommands(source string) string {
	for position := 0; position < len(source); {
		relative := strings.Index(source[position:], `\AUTHOR`)
		if relative < 0 {
			break
		}
		start := position + relative
		name, commandEnd := readTeXCommand(source, start)
		if name != "AUTHOR" && name != "AUTHORREMARK" {
			position = commandEnd
			continue
		}
		cursor := skipTeXSpace(source, commandEnd)
		if name == "AUTHORREMARK" && cursor < len(source) && source[cursor] == '[' {
			_, end, ok := parseDelimited(source, cursor, '[', ']')
			if !ok {
				position = commandEnd
				continue
			}
			cursor = skipTeXSpace(source, end)
		}
		first, end, ok := parseBraced(source, cursor)
		if !ok {
			position = commandEnd
			continue
		}
		if name == "AUTHORREMARK" {
			source = source[:start] + source[end:]
			position = start
			continue
		}
		cursor = skipTeXSpace(source, end)
		body, bodyEnd, ok := parseBraced(source, cursor)
		if !ok {
			position = commandEnd
			continue
		}
		_ = first // The stable author ID is source metadata, not rendered text.
		source = source[:start] + body + source[bodyEnd:]
		position = start + len(body)
	}
	return source
}

func selectTeXConditional(source, opening string, condition bool) string {
	for {
		start := strings.Index(source, opening)
		if start < 0 {
			return source
		}
		position := start + len(opening)
		depth := 1
		alternative := -1
		end := -1
		for position < len(source) {
			next := strings.IndexByte(source[position:], '\\')
			if next < 0 {
				break
			}
			position += next
			command, commandEnd := readTeXCommand(source, position)
			if commandEnd <= position {
				position++
				continue
			}
			if strings.HasPrefix(command, "if") {
				depth++
			} else if command == "fi" {
				depth--
				if depth == 0 {
					end = commandEnd
					break
				}
			} else if command == "else" && depth == 1 {
				alternative = position
			}
			position = commandEnd
		}
		if end < 0 {
			return source
		}
		trueStart := start + len(opening)
		trueEnd := end - len(`\fi`)
		falseStart := trueEnd
		if alternative >= 0 {
			trueEnd = alternative
			falseStart = alternative + len(`\else`)
		}
		selected := ""
		if condition {
			selected = source[trueStart:trueEnd]
		} else if alternative >= 0 {
			selected = source[falseStart : end-len(`\fi`)]
		}
		source = source[:start] + selected + source[end:]
	}
}

func (context *latexRenderContext) renderBlocks(source string) string {
	return context.renderBlocksAt(source, context.bodyOffset)
}

func (context *latexRenderContext) renderBlocksAt(source string, sourceOffset int) string {
	var output strings.Builder
	for len(source) > 0 {
		trimmed := strings.TrimLeftFunc(source, unicode.IsSpace)
		sourceOffset += len(source) - len(trimmed)
		source = trimmed
		if source == "" {
			break
		}

		if strings.HasPrefix(source, `\maketitle`) {
			source = source[len(`\maketitle`):]
			sourceOffset += len(`\maketitle`)
			continue
		}
		if strings.HasPrefix(source, `\tableofcontents`) && commandBoundary(source, len(`\tableofcontents`)) {
			output.WriteString(context.renderTableOfContents())
			source = source[len(`\tableofcontents`):]
			sourceOffset += len(`\tableofcontents`)
			continue
		}
		if strings.HasPrefix(source, `\appendix`) && commandBoundary(source, len(`\appendix`)) {
			source = source[len(`\appendix`):]
			sourceOffset += len(`\appendix`)
			continue
		}
		if strings.HasPrefix(source, `\attentionwindow`) {
			position := len(`\attentionwindow`)
			position = skipTeXSpace(source, position)
			first, next, ok := parseBraced(source, position)
			if ok {
				next = skipTeXSpace(source, next)
				second, end, secondOK := parseBraced(source, next)
				if secondOK {
					section, _ := context.addSection(second, 2, sourceOffset)
					output.WriteString(`<section class="attention-window" id="` + section.ID + `"><div class="window-time">`)
					output.WriteString(context.renderInline(first))
					output.WriteString(`</div><h2>`)
					output.WriteString(context.renderInline(second))
					output.WriteString(`</h2></section>`)
					source = source[end:]
					sourceOffset += end
					continue
				}
			}
		}
		if command, argument, end, ok := parseHeading(source); ok {
			tag := "h2"
			if command == "subsection" {
				tag = "h3"
			} else if command == "subsubsection" || command == "paragraph" {
				tag = "h4"
			}
			section, number := context.addSection(argument, map[string]int{"h2": 2, "h3": 3, "h4": 4}[tag], sourceOffset)
			output.WriteString("<" + tag + ` id="` + section.ID + `">`)
			if number != "" {
				output.WriteString(`<span class="section-number">` + template.HTMLEscapeString(number) + `</span> `)
			}
			output.WriteString(context.renderInline(argument))
			output.WriteString("</" + tag + ">")
			source = source[end:]
			sourceOffset += end
			continue
		}
		if strings.HasPrefix(source, `\[`) {
			if end := findUnescaped(source, `\]`, 2); end >= 0 {
				output.WriteString(context.renderDisplayMath(source[2:end]))
				source = source[end+2:]
				sourceOffset += end + 2
				continue
			}
		}
		if strings.HasPrefix(source, `$$`) {
			if end := findUnescaped(source, `$$`, 2); end >= 0 {
				output.WriteString(context.renderDisplayMath(source[2:end]))
				source = source[end+2:]
				sourceOffset += end + 2
				continue
			}
		}
		if strings.HasPrefix(source, `\begin{`) {
			environment, beginEnd, ok := parseEnvironmentStart(source)
			if ok {
				content, end, found := extractEnvironment(source, environment, beginEnd)
				if found {
					options := environmentOptions(source, environment, beginEnd)
					output.WriteString(context.renderEnvironment(environment, options, content, sourceOffset, sourceOffset+beginEnd))
					source = source[end:]
					sourceOffset += end
					continue
				}
			}
		}

		end := paragraphBoundary(source)
		if end <= 0 {
			_, runeSize := utf8.DecodeRuneInString(source)
			if runeSize <= 0 {
				break
			}
			output.WriteString(context.renderInline(source[:runeSize]))
			source = source[runeSize:]
			sourceOffset += runeSize
			continue
		}
		paragraph := strings.TrimSpace(source[:end])
		if paragraph != "" {
			rendered := context.renderInline(paragraph)
			if strings.TrimSpace(rendered) != "" {
				output.WriteString(`<p>`)
				output.WriteString(rendered)
				output.WriteString(`</p>`)
			}
		}
		source = source[end:]
		sourceOffset += end
	}
	return output.String()
}

func (context *latexRenderContext) addSection(source string, level, sourceOffset int) (documentSection, string) {
	title := collapseWhitespace(texFragmentToPlainText(source))
	if title == "" {
		title = "Untitled section"
	}
	section := documentSection{
		ID:    fmt.Sprintf("section-%d", len(context.sections)+1),
		Title: title,
		Level: level,
	}
	number := ""
	if occurrence, ok := context.index.occurrence(sourceOffset, ""); ok && occurrence.Kind == "section" {
		section.ID = occurrence.Anchor
		section.Number = occurrence.Number
		if indexedTitle := collapseWhitespace(texFragmentToPlainText(occurrence.Title)); indexedTitle != "" {
			section.Title = indexedTitle
		}
		number = occurrence.Number
	}
	context.sections = append(context.sections, section)
	return section, number
}

func (context *latexRenderContext) renderTableOfContents() string {
	if len(context.index.tableOfContents) == 0 {
		return `<nav class="document-toc" aria-label="Table of contents"><h2>Contents</h2></nav>`
	}
	var output strings.Builder
	output.WriteString(`<nav class="document-toc" aria-label="Table of contents"><h2>Contents</h2><div class="toc-entries">`)
	for _, section := range context.index.tableOfContents {
		output.WriteString(`<div class="toc-entry level-` + strconv.Itoa(section.Level) + `" data-target="` + template.HTMLEscapeString(section.Anchor) + `"><a href="#` + template.HTMLEscapeString(section.Anchor) + `">`)
		if section.Number != "" {
			output.WriteString(`<span class="toc-number">` + template.HTMLEscapeString(section.Number) + `</span>`)
		}
		output.WriteString(`<span class="toc-title">` + context.renderInline(section.Title) + `</span><span class="toc-page" aria-hidden="true"></span></a></div>`)
	}
	output.WriteString(`</div></nav>`)
	return output.String()
}

func (context *latexRenderContext) renderEnvironment(environment, options, content string, environmentOffset, contentOffset int) string {
	base := strings.TrimSuffix(environment, "*")
	if declaration, declared := context.index.theorems[base]; declared {
		return context.renderStatement(base, options, content, contentOffset, environmentOffset, declaration.Title)
	}
	switch base {
	case "boardnotes":
		return `<section class="lecture-box board"><h3>板书</h3>` + context.renderBlocksAt(content, contentOffset) + `</section>`
	case "oralnotes":
		return `<section class="lecture-box oral"><h3>口头讲解（不抄）</h3>` + context.renderBlocksAt(content, contentOffset) + `</section>`
	case "studentinput":
		return `<section class="lecture-box student"><h3>学生输入</h3>` + context.renderBlocksAt(content, contentOffset) + `</section>`
	case "itemize":
		return context.renderList(content, false, contentOffset, "")
	case "enumerate":
		return context.renderList(content, true, contentOffset, options)
	case "description":
		return context.renderList(content, false, contentOffset, "")
	case "thebibliography":
		return context.renderBibliography(content, contentOffset)
	case "equation", "displaymath":
		return context.renderNumberedMath(content, environmentOffset)
	case "align", "alignat", "flalign", "split", "eqnarray":
		mathContent := stripMathTags(stripAlignatArgument(content))
		return context.wrapNumberedMath(context.renderMath(`\begin{aligned}`+mathContent+`\end{aligned}`, true), environmentOffset, content)
	case "gather", "multline":
		return context.wrapNumberedMath(context.renderMath(`\begin{gathered}`+stripMathTags(content)+`\end{gathered}`, true), environmentOffset, content)
	case "CD":
		return context.renderMath(`\begin{CD}`+content+`\end{CD}`, true)
	case "xy":
		return context.renderMath(content, true)
	case "tikzcd":
		return context.renderTikZCD(content, `\begin{tikzcd}`+content+`\end{tikzcd}`)
	case "verbatim", "lstlisting", "minted":
		return `<pre class="source-fragment"><code>` + template.HTMLEscapeString(strings.TrimSpace(content)) + `</code></pre>`
	case "tikzpicture":
		return context.renderTikZPicture(content, options)
	case "flowchart":
		return context.renderFlowchart(content, options)
	case "tabular":
		return context.renderTabular(content, 1)
	case "markdowntable":
		return context.renderMarkdownTable(content)
	case "tabularx":
		return context.renderTabular(content, 2)
	case "array":
		return context.renderTabular(content, 1)
	case "center", "flushleft", "flushright":
		return `<div class="` + base + `">` + context.renderBlocksAt(content, contentOffset) + `</div>`
	case "abstract":
		return `<section class="abstract"><h3>Abstract</h3>` + context.renderBlocksAt(content, contentOffset) + `</section>`
	case "theorem", "lemma", "proposition", "corollary", "definition", "example", "remark", "proof":
		return context.renderStatement(base, options, content, contentOffset, environmentOffset, titleCase(base))
	case "document":
		return context.renderBlocksAt(content, contentOffset)
	default:
		return `<div class="latex-environment" data-environment="` + template.HTMLEscapeString(environment) + `">` + context.renderBlocksAt(content, contentOffset) + `</div>`
	}
}

var tableRulePattern = regexp.MustCompile(`\\(?:toprule|midrule|bottomrule|hline)\b|\\(?:cline|cmidrule)(?:\s*\([^)]*\))?\s*\{[^{}]*\}`)
var tableMacroDefinitionPattern = regexp.MustCompile(`\\(?:newcommand|renewcommand|providecommand)\*?\s*\{\s*\\([A-Za-z@]+)\s*\}\s*\{`)

type tableMacroDefinition struct {
	name       string
	body       string
	start, end int
}

func rewriteTableMacroDocuments(prepared, body string) (string, string) {
	definitions := findTableMacroDefinitions(prepared)
	if len(definitions) == 0 {
		return prepared, body
	}
	macros := make(map[string]string, len(definitions))
	for _, definition := range definitions {
		macros[definition.name] = definition.body
	}
	showDefinitions := !strings.Contains(prepared, `\begin{document}`)
	return rewriteTableMacroSource(prepared, macros, showDefinitions), rewriteTableMacroSource(body, macros, showDefinitions)
}

func findTableMacroDefinitions(source string) []tableMacroDefinition {
	matches := tableMacroDefinitionPattern.FindAllStringSubmatchIndex(source, -1)
	definitions := make([]tableMacroDefinition, 0, len(matches))
	for _, match := range matches {
		if len(match) < 4 {
			continue
		}
		body, end, ok := parseBraced(source, match[1]-1)
		if !ok || !strings.Contains(body, `\begin{tabular`) {
			continue
		}
		definitions = append(definitions, tableMacroDefinition{
			name: source[match[2]:match[3]], body: body, start: match[0], end: end,
		})
	}
	return definitions
}

func supplementTableMacros(source, definitions string) string {
	definedTables := findTableMacroDefinitions(stripLatexComments(definitions))
	if len(definedTables) == 0 {
		return source
	}
	visibleSource := stripLatexComments(source)
	needed := false
	for _, definition := range definedTables {
		if texMacroDefined(visibleSource, definition.name) {
			continue
		}
		for position := 0; position < len(visibleSource); {
			relative := strings.IndexByte(visibleSource[position:], '\\')
			if relative < 0 {
				break
			}
			position += relative
			name, end := readTeXCommand(visibleSource, position)
			if name == definition.name {
				needed = true
				break
			}
			if end <= position {
				position++
			} else {
				position = end
			}
		}
		if needed {
			break
		}
	}
	if !needed {
		return source
	}
	prefix := definitions + "\n"
	if strings.Contains(visibleSource, `\begin{document}`) {
		return prefix + source
	}
	return prefix + `\begin{document}` + "\n" + source + "\n" + `\end{document}`
}

func rewriteTableMacroSource(source string, macros map[string]string, showDefinitions bool) string {
	definitions := findTableMacroDefinitions(source)
	for index := len(definitions) - 1; index >= 0; index-- {
		definition := definitions[index]
		if _, ok := macros[definition.name]; !ok {
			continue
		}
		replacement := ""
		if showDefinitions {
			replacement = definition.body
		}
		source = source[:definition.start] + replacement + source[definition.end:]
	}
	for pass := 0; pass < 16; pass++ {
		var output strings.Builder
		changed := false
		for position := 0; position < len(source); {
			if source[position] != '\\' {
				_, size := utf8.DecodeRuneInString(source[position:])
				if size <= 0 {
					break
				}
				output.WriteString(source[position : position+size])
				position += size
				continue
			}
			name, end := readTeXCommand(source, position)
			if replacement, ok := macros[name]; ok {
				output.WriteString(replacement)
				position = end
				changed = true
				continue
			}
			if end <= position {
				output.WriteByte(source[position])
				position++
				continue
			}
			output.WriteString(source[position:end])
			position = end
		}
		source = output.String()
		if !changed {
			break
		}
	}
	return source
}

func (context *latexRenderContext) renderTabular(content string, preambleArguments int) string {
	for index := 0; index < preambleArguments; index++ {
		start := skipTeXSpace(content, 0)
		if _, end, ok := parseBraced(content, start); ok {
			content = content[end:]
		}
	}
	content = tableRulePattern.ReplaceAllString(content, "")
	rows := make([][]string, 0)
	width := 0
	for _, row := range splitTopLevelTeXRows(content) {
		row = strings.TrimSpace(row)
		if strings.HasPrefix(row, "[") {
			if _, end, ok := parseDelimited(row, 0, '[', ']'); ok {
				row = strings.TrimSpace(row[end:])
			}
		}
		if row == "" {
			continue
		}
		cells := splitTopLevelTeXCells(row)
		for index := range cells {
			cells[index] = tabularCellMath(cells[index])
		}
		if len(cells) > width {
			width = len(cells)
		}
		rows = append(rows, cells)
	}
	if len(rows) == 0 || width == 0 {
		return ""
	}
	var expression strings.Builder
	expression.WriteString(`\begin{array}{`)
	expression.WriteString(strings.Repeat("l", width))
	expression.WriteByte('}')
	for rowIndex, row := range rows {
		if rowIndex > 0 {
			expression.WriteString(`\\`)
		}
		for column := 0; column < width; column++ {
			if column > 0 {
				expression.WriteByte('&')
			}
			if column < len(row) && row[column] != "" {
				expression.WriteString(row[column])
			} else {
				expression.WriteString(`{}`)
			}
		}
	}
	expression.WriteString(`\end{array}`)
	return context.renderMath(expression.String(), true)
}

func splitTopLevelTeXCells(source string) []string {
	var cells []string
	start, braceDepth, environmentDepth := 0, 0, 0
	for position := 0; position < len(source); {
		if strings.HasPrefix(source[position:], `\begin{`) {
			if _, end, ok := parseEnvironmentStart(source[position:]); ok {
				environmentDepth++
				position += end
				continue
			}
		}
		if strings.HasPrefix(source[position:], `\end{`) {
			if _, end, ok := parseBraced(source[position:], len(`\end`)); ok {
				if environmentDepth > 0 {
					environmentDepth--
				}
				position += end
				continue
			}
		}
		switch {
		case source[position] == '{' && !isEscaped(source, position):
			braceDepth++
		case source[position] == '}' && !isEscaped(source, position) && braceDepth > 0:
			braceDepth--
		case source[position] == '&' && !isEscaped(source, position) && braceDepth == 0 && environmentDepth == 0:
			cells = append(cells, source[start:position])
			start = position + 1
		}
		position++
	}
	return append(cells, source[start:])
}

func tabularCellMath(source string) string {
	var output strings.Builder
	textStart := 0
	for position := 0; position < len(source); position++ {
		if source[position] != '$' || isEscaped(source, position) {
			continue
		}
		mathEnd := findMathDollar(source, position+1)
		if mathEnd < 0 {
			break
		}
		appendTabularText(&output, source[textStart:position])
		if math := strings.TrimSpace(source[position+1 : mathEnd]); math != "" {
			output.WriteByte('{')
			output.WriteString(math)
			output.WriteByte('}')
		}
		position = mathEnd
		textStart = mathEnd + 1
	}
	appendTabularText(&output, source[textStart:])
	if output.Len() == 0 {
		return `{}`
	}
	return output.String()
}

func appendTabularText(output *strings.Builder, source string) {
	text := strings.TrimSpace(texFragmentToPlainText(source))
	if text == "" {
		return
	}
	text = strings.NewReplacer(
		`\`, `\backslash `,
		`{`, `\{`,
		`}`, `\}`,
		`&`, `\&`,
		`%`, `\%`,
		`#`, `\#`,
		`_`, `\_`,
	).Replace(text)
	output.WriteString(`\text{`)
	output.WriteString(text)
	output.WriteByte('}')
}

func (context *latexRenderContext) renderStatement(base, options, content string, contentOffset, environmentOffset int, fallbackTitle string) string {
	occurrence, numbered := context.index.occurrence(environmentOffset, content)
	if numbered && (occurrence.Kind != "statement" || occurrence.Environment != base) {
		numbered = false
	}
	title := fallbackTitle
	anchor := ""
	if numbered {
		title = formatOccurrenceTitle(occurrence)
		anchor = occurrence.Anchor
	}
	heading := template.HTMLEscapeString(title)
	if options != "" {
		heading += ` <span class="statement-option">(` + context.renderInline(options) + `)</span>`
	}
	id := ""
	if anchor != "" {
		id = ` id="` + template.HTMLEscapeString(anchor) + `"`
	}
	return `<section class="statement ` + base + `"` + id + `><h3>` + heading + `</h3>` + context.renderBlocksAt(content, contentOffset) + `</section>`
}

func (context *latexRenderContext) renderNumberedMath(content string, environmentOffset int) string {
	formula, footnotes := extractMathFootnotes(stripMathTags(content))
	rendered := context.renderMath(formula, true) + context.renderMathFootnotes(footnotes)
	return context.wrapNumberedMath(rendered, environmentOffset, content)
}

func (context *latexRenderContext) renderDisplayMath(content string) string {
	formula, footnotes := extractMathFootnotes(stripMathTags(content))
	rendered := context.renderMath(formula, true) + context.renderMathFootnotes(footnotes)
	return context.wrapManualEquationTags(rendered, content)
}

func extractMathFootnotes(source string) (string, []string) {
	var notes []string
	for searchFrom := 0; searchFrom < len(source); {
		relative := strings.Index(source[searchFrom:], `\footnote`)
		if relative < 0 {
			break
		}
		position := searchFrom + relative
		commandEnd := position + len(`\footnote`)
		if !commandBoundary(source, commandEnd) {
			searchFrom = commandEnd
			continue
		}
		cursor := skipTeXSpace(source, commandEnd)
		if cursor < len(source) && source[cursor] == '[' {
			if _, end, ok := parseDelimited(source, cursor, '[', ']'); ok {
				cursor = skipTeXSpace(source, end)
			}
		}
		note, end, ok := parseBraced(source, cursor)
		if !ok {
			searchFrom = commandEnd
			continue
		}
		notes = append(notes, note)
		source = source[:position] + source[end:]
		searchFrom = position
	}
	return source, notes
}

func (context *latexRenderContext) renderMathFootnotes(notes []string) string {
	var output strings.Builder
	for _, note := range notes {
		output.WriteString(`<span class="footnote">`)
		output.WriteString(context.renderInline(note))
		output.WriteString(`</span>`)
	}
	return output.String()
}

func (context *latexRenderContext) wrapNumberedMath(rendered string, environmentOffset int, content string) string {
	occurrence, ok := context.index.occurrence(environmentOffset, content)
	if !ok || occurrence.Kind != "equation" || occurrence.DisplayNumber == "" {
		return context.wrapManualEquationTags(rendered, content)
	}
	tags := occurrence.DisplayTags
	if len(tags) == 0 {
		for _, number := range occurrence.DisplayNumbers {
			tags = append(tags, "("+number+")")
		}
	}
	if len(tags) == 0 {
		tags = []string{"(" + occurrence.DisplayNumber + ")"}
	}
	if len(tags) == 1 {
		return `<div class="numbered-equation" id="` + template.HTMLEscapeString(occurrence.Anchor) + `">` + rendered + `<span class="equation-number">` + template.HTMLEscapeString(tags[0]) + `</span></div>`
	}
	var numberList strings.Builder
	numberList.WriteString(`<span class="equation-numbers">`)
	for _, tag := range tags {
		numberList.WriteString(`<span>` + template.HTMLEscapeString(tag) + `</span>`)
	}
	numberList.WriteString(`</span>`)
	return `<div class="numbered-equation" id="` + template.HTMLEscapeString(occurrence.Anchor) + `">` + rendered + numberList.String() + `</div>`
}

func (context *latexRenderContext) wrapManualEquationTags(rendered, content string) string {
	var tags []string
	for _, row := range splitTopLevelTeXRows(content) {
		tag, starred := equationTag(row)
		if tag == "" {
			continue
		}
		_, display := equationRowNumber(latexEquationRow{tag: tag, tagStar: starred}, "")
		tags = append(tags, display)
	}
	if len(tags) == 0 {
		return rendered
	}
	if len(tags) == 1 {
		return `<div class="numbered-equation">` + rendered + `<span class="equation-number">` + template.HTMLEscapeString(tags[0]) + `</span></div>`
	}
	var numberList strings.Builder
	numberList.WriteString(`<span class="equation-numbers">`)
	for _, tag := range tags {
		numberList.WriteString(`<span>` + template.HTMLEscapeString(tag) + `</span>`)
	}
	numberList.WriteString(`</span>`)
	return `<div class="numbered-equation">` + rendered + numberList.String() + `</div>`
}

func (context *latexRenderContext) renderList(content string, ordered bool, contentOffset int, options string) string {
	items := splitListItems(content)
	if len(items) == 0 {
		return context.renderBlocksAt(content, contentOffset)
	}
	tag := "ul"
	if ordered {
		tag = "ol"
	}
	customMarkers := ordered && enumerateLabelTemplate(options) != ""
	var output strings.Builder
	output.WriteString("<" + tag)
	if customMarkers {
		output.WriteString(` class="custom-enumerate"`)
	}
	output.WriteString(">")
	for itemIndex, item := range items {
		itemID := ""
		if occurrence, ok := context.index.occurrence(contentOffset+item.Offset, item.Content); ok && occurrence.Kind == "item" {
			itemID = ` id="` + template.HTMLEscapeString(occurrence.Anchor) + `"`
		}
		marker := ""
		if customMarkers {
			marker, _ = enumerateItemNumber(itemIndex+1, 1, "", options)
		}
		itemClass := ""
		if item.Label != "" {
			marker = collapseWhitespace(texFragmentToPlainText(item.Label))
			if marker == "" {
				marker = item.Label
			}
			itemClass = ` class="explicit-item-label"`
		}
		markerAttribute := ""
		if marker != "" {
			markerAttribute = ` data-marker="` + template.HTMLEscapeString(marker) + `"`
		}
		output.WriteString("<li" + itemClass + itemID + markerAttribute + ">")
		output.WriteString(context.renderBlocksAt(item.Content, contentOffset+item.Offset))
		output.WriteString("</li>")
	}
	output.WriteString("</" + tag + ">")
	return output.String()
}

func (context *latexRenderContext) renderBibliography(content string, contentOffset int) string {
	items := splitBibliographyItems(content)
	if len(items) == 0 {
		return context.renderBlocksAt(content, contentOffset)
	}
	var output strings.Builder
	output.WriteString(`<section class="bibliography"><h2>References</h2><ol class="bibliography-list">`)
	for _, item := range items {
		entry, ok := context.index.bibliography[item.Key]
		if !ok {
			entry = latexBibliographyEntry{Key: item.Key, Label: item.Label, Anchor: latexBibliographyAnchor(item.Key)}
		}
		plainLabel := collapseWhitespace(texFragmentToPlainText(entry.Label))
		if plainLabel == "" {
			plainLabel = entry.Key
		}
		output.WriteString(`<li id="` + template.HTMLEscapeString(entry.Anchor) + `" data-marker="[` + template.HTMLEscapeString(plainLabel) + `]"><span class="bibliography-marker">[`)
		output.WriteString(context.renderInline(entry.Label))
		output.WriteString(`]</span>`)
		output.WriteString(context.renderBlocksAt(item.Content, contentOffset+item.Offset))
		output.WriteString(`</li>`)
	}
	output.WriteString(`</ol></section>`)
	return output.String()
}

func parseCitationArguments(source string, commandEnd int) (keys, notes []string, end int, ok bool) {
	cursor := skipTeXSpace(source, commandEnd)
	for len(notes) < 2 && cursor < len(source) && source[cursor] == '[' {
		note, optionalEnd, optionalOK := parseDelimited(source, cursor, '[', ']')
		if !optionalOK {
			return nil, nil, commandEnd, false
		}
		notes = append(notes, strings.TrimSpace(note))
		cursor = skipTeXSpace(source, optionalEnd)
	}
	keySource, end, ok := parseBraced(source, cursor)
	if !ok {
		return nil, nil, commandEnd, false
	}
	for _, key := range strings.Split(keySource, ",") {
		if key = strings.TrimSpace(key); key != "" {
			keys = append(keys, key)
		}
	}
	return keys, notes, end, true
}

func (context *latexRenderContext) renderCitation(keys, notes []string) string {
	unresolved := len(keys) == 0
	var citations strings.Builder
	for index, key := range keys {
		if index > 0 {
			citations.WriteString(", ")
		}
		entry, ok := context.index.bibliography[key]
		if !ok {
			unresolved = true
			citations.WriteString(`<span class="citation-key unresolved" title="Unresolved citation: ` + template.HTMLEscapeString(key) + `">?</span>`)
			continue
		}
		citations.WriteString(`<a class="citation-link" href="#` + template.HTMLEscapeString(entry.Anchor) + `">`)
		citations.WriteString(context.renderInline(entry.Label))
		citations.WriteString(`</a>`)
	}
	className := "citation"
	if unresolved {
		className += " unresolved"
	}
	var output strings.Builder
	output.WriteString(`<span class="` + className + `">[`)
	if len(notes) == 2 && notes[0] != "" {
		output.WriteString(context.renderInline(notes[0]))
		if citations.Len() > 0 {
			output.WriteByte(' ')
		}
	}
	output.WriteString(citations.String())
	if len(notes) == 1 && notes[0] != "" {
		if citations.Len() > 0 {
			output.WriteString(", ")
		}
		output.WriteString(context.renderInline(notes[0]))
	} else if len(notes) == 2 && notes[1] != "" {
		if citations.Len() > 0 {
			output.WriteString(", ")
		}
		output.WriteString(context.renderInline(notes[1]))
	}
	output.WriteString(`]</span>`)
	return output.String()
}

func (context *latexRenderContext) renderInline(source string) string {
	var output strings.Builder
	macroExpansions := 0
	for len(source) > 0 {
		switch {
		case strings.HasPrefix(source, `$$`):
			if end := findUnescaped(source, `$$`, 2); end >= 0 {
				output.WriteString(context.renderDisplayMath(source[2:end]))
				source = source[end+2:]
				continue
			}
		case source[0] == '$' && !isEscaped(source, 0):
			if end := findMathDollar(source, 1); end >= 0 {
				output.WriteString(context.renderMath(source[1:end], false))
				source = source[end+1:]
				continue
			}
		case strings.HasPrefix(source, `\(`):
			if end := findUnescaped(source, `\)`, 2); end >= 0 {
				output.WriteString(context.renderMath(source[2:end], false))
				source = source[end+2:]
				continue
			}
		case strings.HasPrefix(source, `\[`):
			if end := findUnescaped(source, `\]`, 2); end >= 0 {
				output.WriteString(context.renderDisplayMath(source[2:end]))
				source = source[end+2:]
				continue
			}
		case strings.HasPrefix(source, `\\`):
			output.WriteString(`<br>`)
			source = source[2:]
			continue
		case source[0] == '\\':
			command, commandEnd := readTeXCommand(source, 0)
			if commandEnd == 0 {
				output.WriteString(`\`)
				source = source[1:]
				continue
			}
			if replacement, ok := escapedTextCommand(command); ok {
				output.WriteString(replacement)
				source = source[commandEnd:]
				continue
			}
			if end, ok := consumeNonRenderingTeXCommand(source, command, commandEnd); ok {
				source = source[end:]
				continue
			}
			position := skipTeXSpace(source, commandEnd)
			if isTextAccentCommand(command) {
				if argument, end, ok := parseTextAccentArgument(source, position); ok {
					output.WriteString(template.HTMLEscapeString(applyTextAccent(command, argument)))
					source = source[end:]
					continue
				}
			}
			if isTextStrikeCommand(command, context.macros) {
				if argument, end, ok := parseBraced(source, position); ok {
					output.WriteString("<del>" + context.renderInline(argument) + "</del>")
					source = source[end:]
					continue
				}
			}
			switch command {
			case "tableofcontents":
				output.WriteString(context.renderTableOfContents())
				source = source[commandEnd:]
				continue
			case "textbf", "bfseries":
				if argument, end, ok := parseBraced(source, position); ok {
					output.WriteString("<strong>" + context.renderInline(argument) + "</strong>")
					source = source[end:]
					continue
				}
				if command == "bfseries" {
					output.WriteString("<strong>" + context.renderInline(source[commandEnd:]) + "</strong>")
					return output.String()
				}
			case "emph", "textit", "textsl":
				if argument, end, ok := parseBraced(source, position); ok {
					output.WriteString("<em>" + context.renderInline(argument) + "</em>")
					source = source[end:]
					continue
				}
			case "em", "it", "itshape", "sl", "slshape":
				output.WriteString("<em>" + context.renderInline(source[commandEnd:]) + "</em>")
				return output.String()
			case "texttt":
				if argument, end, ok := parseBraced(source, position); ok {
					output.WriteString("<code>" + template.HTMLEscapeString(texLiteralText(argument)) + "</code>")
					source = source[end:]
					continue
				}
			case "tt", "ttfamily":
				output.WriteString("<code>" + template.HTMLEscapeString(texLiteralText(source[commandEnd:])) + "</code>")
				return output.String()
			case "rm", "rmfamily", "normalfont":
				source = source[commandEnd:]
				continue
			case "newblock":
				output.WriteByte(' ')
				source = source[commandEnd:]
				continue
			case "bysame":
				output.WriteString("—")
				source = source[commandEnd:]
				continue
			case "etalchar":
				if argument, end, ok := parseBraced(source, position); ok {
					output.WriteString(context.renderMath(`^{`+argument+`}`, false))
					source = source[end:]
					continue
				}
			case "MR":
				if argument, end, ok := parseBraced(source, position); ok {
					output.WriteString(" MR " + context.renderInline(argument))
					source = source[end:]
					continue
				}
			case "MRhref":
				if _, next, ok := parseBraced(source, position); ok {
					next = skipTeXSpace(source, next)
					if label, end, labelOK := parseBraced(source, next); labelOK {
						output.WriteString(context.renderInline(label))
						source = source[end:]
						continue
					}
				}
			case "dynkin":
				if rendered, end, ok := context.renderDynkinCommand(source, commandEnd); ok {
					output.WriteString(rendered)
					source = source[end:]
					continue
				}
			case "textsc":
				if argument, end, ok := parseBraced(source, position); ok {
					output.WriteString(`<span class="smallcaps">` + context.renderInline(argument) + `</span>`)
					source = source[end:]
					continue
				}
			case "underline", "uline":
				if argument, end, ok := parseBraced(source, position); ok {
					output.WriteString("<u>" + context.renderInline(argument) + "</u>")
					source = source[end:]
					continue
				}
			case "textcolor":
				color, next, syntaxOK, resolved := parseLatexColorArgument(source, commandEnd, context.colors)
				if syntaxOK {
					next = skipTeXSpace(source, next)
					if argument, end, argumentOK := parseBraced(source, next); argumentOK {
						rendered := context.renderInline(argument)
						if resolved {
							output.WriteString(`<span style="color:` + color.hex() + `">` + rendered + `</span>`)
						} else {
							output.WriteString(rendered)
						}
						source = source[end:]
						continue
					}
				}
			case "texorpdfstring":
				if visible, next, ok := parseBraced(source, position); ok {
					next = skipTeXSpace(source, next)
					if _, end, fallbackOK := parseBraced(source, next); fallbackOK {
						output.WriteString(context.renderInline(visible))
						source = source[end:]
						continue
					}
				}
			case "href":
				if target, next, ok := parseBraced(source, position); ok {
					next = skipTeXSpace(source, next)
					if label, end, labelOK := parseBraced(source, next); labelOK {
						if safeURL(target) {
							output.WriteString(`<a href="` + template.HTMLEscapeString(target) + `" rel="noreferrer">` + context.renderInline(label) + `</a>`)
						} else {
							output.WriteString(context.renderInline(label))
						}
						source = source[end:]
						continue
					}
				}
			case "url":
				if target, end, ok := parseBraced(source, position); ok {
					if safeURL(target) {
						output.WriteString(`<a href="` + template.HTMLEscapeString(target) + `" rel="noreferrer"><code>` + template.HTMLEscapeString(target) + `</code></a>`)
					} else {
						output.WriteString(`<code>` + template.HTMLEscapeString(target) + `</code>`)
					}
					source = source[end:]
					continue
				}
			case "footnote":
				if argument, end, ok := parseBraced(source, position); ok {
					output.WriteString(`<span class="footnote">` + context.renderInline(argument) + `</span>`)
					source = source[end:]
					continue
				}
			case "cite":
				if keys, notes, end, ok := parseCitationArguments(source, commandEnd); ok {
					output.WriteString(context.renderCitation(keys, notes))
					source = source[end:]
					continue
				}
			case "ref", "eqref", "label":
				if argument, end, ok := parseBraced(source, position); ok {
					if command == "ref" || command == "eqref" {
						key := strings.TrimSpace(argument)
						if reference, found := context.index.references[key]; found {
							output.WriteString(renderResolvedReference(reference, command == "eqref"))
						} else {
							output.WriteString(`<span class="reference unresolved" title="Unresolved reference: ` + template.HTMLEscapeString(key) + `">??</span>`)
						}
					} else if command != "label" {
						output.WriteString(`<span class="reference">` + template.HTMLEscapeString(argument) + `</span>`)
					}
					source = source[end:]
					continue
				}
			case "vspace", "hspace", "vskip", "hskip":
				if _, end, ok := parseBraced(source, position); ok {
					source = source[end:]
					continue
				}
			case "color":
				color, end, syntaxOK, resolved := parseLatexColorArgument(source, commandEnd, context.colors)
				if syntaxOK {
					if resolved {
						output.WriteString(`<span style="color:` + color.hex() + `">`)
						output.WriteString(context.renderInline(source[end:]))
						output.WriteString(`</span>`)
						return output.String()
					}
					source = source[end:]
					continue
				}
			case "quad", "qquad", "enspace", "hfill":
				output.WriteString("&emsp;")
				source = source[commandEnd:]
				continue
			case "noindent", "small", "footnotesize", "large", "Large", "centering", "raggedright", "raggedleft", "clearpage", "newpage", "par", "relax":
				source = source[commandEnd:]
				continue
			}
			if definition, ok := context.macros[command]; ok && macroExpansions < 32 {
				if replacement, end, valid := expandMathMacroInvocation(source, commandEnd, definition); valid {
					source = "{" + replacement + "}" + source[end:]
					macroExpansions++
					continue
				}
			}
			output.WriteString(`<span class="unknown-command" style="color:#ff0000">\` + template.HTMLEscapeString(command) + `</span>`)
			// Preserve a readable argument after the explicit command literal.
			if argument, end, ok := parseBraced(source, position); ok {
				output.WriteString(context.renderInline(argument))
				source = source[end:]
				continue
			}
			source = source[commandEnd:]
			continue
		case source[0] == '{':
			if argument, end, ok := parseBraced(source, 0); ok {
				output.WriteString(context.renderInline(argument))
				source = source[end:]
				continue
			}
			source = source[1:]
			continue
		case source[0] == '}':
			source = source[1:]
			continue
		case source[0] == '~':
			output.WriteString("&nbsp;")
			source = source[1:]
			continue
		case source[0] == '\n' || source[0] == '\r' || source[0] == '\t':
			output.WriteByte(' ')
			source = source[1:]
			continue
		}

		next := strings.IndexAny(source, `\${}~`+"\n\r\t")
		if next < 0 {
			next = len(source)
		}
		if next == 0 {
			_, size := utf8.DecodeRuneInString(source)
			if size <= 0 {
				break
			}
			next = size
		}
		output.WriteString(template.HTMLEscapeString(source[:next]))
		source = source[next:]
	}
	return output.String()
}

func parseTextAccentArgument(source string, position int) (string, int, bool) {
	if argument, end, ok := parseBraced(source, position); ok {
		return argument, end, true
	}
	if position >= len(source) {
		return "", position, false
	}
	_, size := utf8.DecodeRuneInString(source[position:])
	if size <= 0 {
		return "", position, false
	}
	return source[position : position+size], position + size, true
}

var textAccentMarks = map[string]string{
	"'": "\u0301", "`": "\u0300", `"`: "\u0308", "^": "\u0302", "~": "\u0303",
	"=": "\u0304", ".": "\u0307", "u": "\u0306", "v": "\u030c", "H": "\u030b",
	"c": "\u0327", "k": "\u0328", "b": "\u0331", "d": "\u0323", "r": "\u030a", "t": "\u0361",
}

func applyTextAccent(accent, argument string) string {
	argument = strings.TrimSpace(argument)
	argument = strings.NewReplacer(`\i`, "i", `\j`, "j").Replace(argument)
	mark, ok := textAccentMarks[accent]
	if !ok {
		return argument
	}
	if argument == "" {
		return map[string]string{"~": "~", "^": "^", `"`: `"`, "'": "´", "`": "`", "=": "¯", ".": "˙"}[accent]
	}
	_, size := utf8.DecodeRuneInString(argument)
	if size <= 0 {
		return argument
	}
	return norm.NFC.String(argument[:size]+mark) + argument[size:]
}

func isTextAccentCommand(command string) bool {
	_, ok := textAccentMarks[command]
	return ok
}

func consumeNonRenderingTeXCommand(source, command string, commandEnd int) (int, bool) {
	if commandEnd <= 0 || commandEnd > len(source) {
		return 0, false
	}
	cursor := commandEnd
	if cursor < len(source) && source[cursor] == '*' {
		cursor++
	}
	consumeOptional := func(position int) int {
		position = skipTeXSpace(source, position)
		for position < len(source) && source[position] == '[' {
			_, end, ok := parseDelimited(source, position, '[', ']')
			if !ok {
				break
			}
			position = skipTeXSpace(source, end)
		}
		return position
	}
	consumeBraced := func(position, count int) (int, bool) {
		for range count {
			position = consumeOptional(position)
			_, end, ok := parseBraced(source, position)
			if !ok {
				return position, false
			}
			position = end
		}
		return position, true
	}

	switch command {
	case "makeatletter", "makeatother", "ExplSyntaxOn", "ExplSyntaxOff", "appendix",
		"sloppy", "fussy", "onecolumn", "twocolumn", "frontmatter", "mainmatter", "backmatter",
		"raggedbottom", "flushbottom":
		return cursor, true
	case "allowdisplaybreaks":
		return consumeOptional(cursor), true
	case "newif":
		cursor = skipTeXSpace(source, cursor)
		if cursor < len(source) && source[cursor] == '\\' {
			_, end := readTeXCommand(source, cursor)
			if end > cursor {
				return end, true
			}
		}
		return cursor, true
	case "def", "gdef", "edef", "xdef":
		cursor = skipTeXSpace(source, cursor)
		if cursor < len(source) && source[cursor] == '\\' {
			_, cursor = readTeXCommand(source, cursor)
		}
		for cursor < len(source) && source[cursor] != '{' && source[cursor] != '\n' && source[cursor] != '\r' {
			cursor++
		}
		if cursor < len(source) && source[cursor] == '{' {
			if _, end, ok := parseBraced(source, cursor); ok {
				return end, true
			}
		}
		return cursor, true
	case "let", "futurelet":
		for cursor < len(source) && source[cursor] != '\n' && source[cursor] != '\r' {
			cursor++
		}
		return cursor, true
	case "newcommand", "renewcommand", "providecommand", "DeclareRobustCommand":
		cursor = skipTeXSpace(source, cursor)
		if cursor < len(source) && source[cursor] == '{' {
			_, end, ok := parseBraced(source, cursor)
			if !ok {
				return cursor, true
			}
			cursor = end
		} else if cursor < len(source) && source[cursor] == '\\' {
			_, cursor = readTeXCommand(source, cursor)
		} else {
			return cursor, true
		}
		cursor = consumeOptional(cursor)
		if _, end, ok := parseBraced(source, cursor); ok {
			return end, true
		}
		return cursor, true
	case "newenvironment", "renewenvironment", "provideenvironment":
		cursor = consumeOptional(cursor)
		if end, ok := consumeBraced(cursor, 1); ok {
			cursor = consumeOptional(end)
			if end, ok = consumeBraced(cursor, 2); ok {
				return end, true
			}
		}
		return cursor, true
	case "newtheorem":
		cursor = consumeOptional(cursor)
		if end, ok := consumeBraced(cursor, 1); ok {
			cursor = consumeOptional(end)
			if end, ok = consumeBraced(cursor, 1); ok {
				return consumeOptional(end), true
			}
		}
		return cursor, true
	case "DeclareMathOperator", "DeclareMathOperatorWithLimits", "DeclarePairedDelimiter", "numberwithin",
		"counterwithin", "PassOptionsToPackage", "setlength", "addtolength", "setcounter", "addtocounter",
		"newcolumntype", "colorlet", "setkomafont", "addtokomafont", "DeclareUnicodeCharacter", "DeclareSymbolFontAlphabet":
		cursor = consumeOptional(cursor)
		if end, ok := consumeBraced(cursor, 2); ok {
			return end, true
		}
		return cursor, true
	case "definecolor", "providecolor", "DeclarePairedDelimiterX":
		cursor = consumeOptional(cursor)
		if end, ok := consumeBraced(cursor, 3); ok {
			return end, true
		}
		return cursor, true
	case "addcontentsline":
		cursor = consumeOptional(cursor)
		if end, ok := consumeBraced(cursor, 3); ok {
			return end, true
		}
		return cursor, true
	case "DeclareMathAlphabet":
		cursor = consumeOptional(cursor)
		if end, ok := consumeBraced(cursor, 5); ok {
			return end, true
		}
		return cursor, true
	case "documentclass", "usepackage", "RequirePackage", "title", "author", "date", "thanks",
		"subject", "keywords", "subtitle", "institute", "geometry", "hypersetup", "pagestyle",
		"thispagestyle", "bibliography", "bibliographystyle", "graphicspath", "theoremstyle",
		"AtBeginDocument", "AtEndDocument", "includeonly", "setlist", "tikzset", "pgfplotsset",
		"mathtoolsset", "sisetup", "lstset", "captionsetup", "microtypesetup", "usetikzlibrary",
		"usepgfplotslibrary", "pdfstringdefDisableCommands", "setstretch", "linespread":
		cursor = consumeOptional(cursor)
		if end, ok := consumeBraced(cursor, 1); ok {
			return end, true
		}
		return cursor, true
	default:
		return 0, false
	}
}

func (context *latexRenderContext) renderMath(expression string, display bool) string {
	expression = strings.TrimSpace(expression)
	if expression == "" {
		return ""
	}
	expression = stripMathDefinitions(expression)
	diagramExpression := stripMathCommandArguments(expression, `\label`)
	if rendered, ok := context.renderXYMatrixExpression(diagramExpression, display); ok {
		return rendered
	}
	if environment, content, ok := standaloneMathEnvironment(diagramExpression); ok && environment == "tikzcd" {
		return context.renderTikZCD(content, strings.TrimSpace(diagramExpression))
	}
	expanded := normalizeMathForEngineProtected(expandMathMacros(expression, context.macros), context.primitiveMacros)
	expanded = rewriteMathStrikes(expanded)
	expanded = rewriteMathColors(expanded, context.colors)
	diagnosticExpression := expanded
	keyBytes := sha256.Sum256([]byte(fmt.Sprintf("%t\x00%s", display, expanded)))
	key := hex.EncodeToString(keyBytes[:])
	if cached, ok := context.renderer.formulae.Load(key); ok {
		formula := cached
		if formula.hadError {
			context.errors++
			context.errorExpressions = append(context.errorExpressions, expanded)
		}
		return formula.html
	}
	options := mathjax.DefaultOptions()
	options.Display = display
	renderMathJax := func(source string) (string, error) {
		context.renderer.mathMu.Lock()
		defer context.renderer.mathMu.Unlock()
		return mathjax.RenderWithOptions(source, options)
	}
	svg, err := renderMathJax(expanded)
	unknownCommands := make([]string, 0)
	undefinedFallback := false
	for attempt := 0; attempt < 32 && err == nil && safeMathSVG(svg); attempt++ {
		commands := mathJaxUndefinedCommands(svg)
		if len(commands) == 0 {
			break
		}
		rewritten, changed := rewriteUnknownMathCommands(expanded, commands)
		if !changed {
			break
		}
		for _, command := range commands {
			if !stringSliceContains(unknownCommands, command) {
				unknownCommands = append(unknownCommands, command)
			}
		}
		undefinedFallback = true
		expanded = rewritten
		svg, err = renderMathJax(expanded)
	}
	className := "math-inline"
	if display {
		className = "math-display"
	}
	if err != nil || !safeMathSVG(svg) {
		context.errors++
		context.errorExpressions = append(context.errorExpressions, diagnosticExpression)
		result := `<code class="math-error" title="Math rendering failed">` + template.HTMLEscapeString(expression) + `</code>`
		context.renderer.formulae.Store(key, renderedFormula{html: result, hadError: true})
		return result
	}
	hadError := undefinedFallback || strings.Contains(svg, `data-mml-node="merror"`) || strings.Contains(svg, `data-mjx-error`)
	if hadError {
		context.errors++
		context.errorExpressions = append(context.errorExpressions, diagnosticExpression)
	}
	unknownAttribute := ""
	if len(unknownCommands) > 0 {
		values := make([]string, len(unknownCommands))
		for index, command := range unknownCommands {
			values[index] = `\` + command
		}
		unknownAttribute = ` data-unknown-commands="` + template.HTMLEscapeString(strings.Join(values, " ")) + `"`
	}
	result := `<span class="` + className + `" data-tex="` + template.HTMLEscapeString(expression) + `"` + unknownAttribute + `>` + svg + `</span>`
	context.renderer.formulae.Store(key, renderedFormula{html: result, hadError: hadError})
	return result
}

var mathJaxUndefinedCommandPattern = regexp.MustCompile(`data-mjx-error="Undefined control sequence \\([A-Za-z@]+)"`)

func mathJaxUndefinedCommands(svg string) []string {
	var commands []string
	for _, match := range mathJaxUndefinedCommandPattern.FindAllStringSubmatch(svg, -1) {
		if len(match) == 2 && !stringSliceContains(commands, match[1]) {
			commands = append(commands, match[1])
		}
	}
	return commands
}

func rewriteUnknownMathCommands(source string, commands []string) (string, bool) {
	unknown := make(map[string]bool, len(commands))
	for _, command := range commands {
		unknown[command] = true
	}
	var output strings.Builder
	changed := false
	for position := 0; position < len(source); {
		if source[position] != '\\' {
			_, size := utf8.DecodeRuneInString(source[position:])
			if size <= 0 {
				break
			}
			output.WriteString(source[position : position+size])
			position += size
			continue
		}
		name, end := readTeXCommand(source, position)
		if unknown[name] {
			output.WriteString(`{\color[RGB]{255,0,0}{\backslash\mathtt{` + name + `}}}`)
			changed = true
		} else if end > position {
			output.WriteString(source[position:end])
		} else {
			output.WriteByte(source[position])
			end = position + 1
		}
		position = end
	}
	return output.String(), changed
}

func stringSliceContains(values []string, wanted string) bool {
	for _, value := range values {
		if value == wanted {
			return true
		}
	}
	return false
}

func safeMathSVG(svg string) bool {
	lower := strings.ToLower(svg)
	return strings.HasPrefix(strings.TrimSpace(lower), "<svg") &&
		!strings.Contains(lower, "<script") &&
		!strings.Contains(lower, "<foreignobject") &&
		!strings.Contains(lower, "javascript:") &&
		!regexp.MustCompile(`\son[a-z]+\s*=`).MatchString(lower)
}

func isTextStrikeCommand(command string, macros map[string]mathMacro) bool {
	switch strings.ToLower(command) {
	case "strikethrough", "strikeout", "sout":
		return true
	case "st":
		_, defined := macros[command]
		return !defined
	default:
		return false
	}
}

func rewriteMathStrikes(source string) string {
	var output strings.Builder
	for position := 0; position < len(source); {
		relative := strings.IndexByte(source[position:], '\\')
		if relative < 0 {
			output.WriteString(source[position:])
			break
		}
		start := position + relative
		output.WriteString(source[position:start])
		command, commandEnd := readTeXCommand(source, start)
		switch strings.ToLower(command) {
		case "strikethrough", "strikeout", "sout", "st":
			cursor := skipTeXSpace(source, commandEnd)
			argument, end, ok := parseBraced(source, cursor)
			if ok {
				output.WriteString(`\enclose{horizontalstrike}{`)
				output.WriteString(rewriteMathStrikes(argument))
				output.WriteByte('}')
				position = end
				continue
			}
		}
		if commandEnd <= start {
			output.WriteByte(source[start])
			position = start + 1
		} else {
			output.WriteString(source[start:commandEnd])
			position = commandEnd
		}
	}
	return output.String()
}

func parseHeading(source string) (command, argument string, end int, ok bool) {
	if !strings.HasPrefix(source, `\`) {
		return "", "", 0, false
	}
	command, commandEnd := readTeXCommand(source, 0)
	if !isSectioningCommand(command) {
		return "", "", 0, false
	}
	argument, end, _, ok = parseHeadingArguments(source, commandEnd)
	return command, argument, end, ok
}

func parseHeadingArguments(source string, commandEnd int) (argument string, end int, starred, ok bool) {
	position := commandEnd
	starred = position < len(source) && source[position] == '*'
	if starred {
		position++
	}
	position = skipTeXSpace(source, position)
	if position < len(source) && source[position] == '[' {
		_, optionalEnd, optionalOK := parseDelimited(source, position, '[', ']')
		if !optionalOK {
			return "", 0, starred, false
		}
		position = skipTeXSpace(source, optionalEnd)
	}
	argument, end, ok = parseBraced(source, position)
	return argument, end, starred, ok
}

func optionalHeadingArgument(source string, commandEnd int) (string, bool) {
	position := commandEnd
	if position < len(source) && source[position] == '*' {
		position++
	}
	position = skipTeXSpace(source, position)
	if position >= len(source) || source[position] != '[' {
		return "", false
	}
	argument, _, ok := parseDelimited(source, position, '[', ']')
	return argument, ok
}

func paragraphBoundary(source string) int {
	inMath := false
	for i := 0; i < len(source); i++ {
		if source[i] == '$' && !isEscaped(source, i) {
			inMath = !inMath
			if i+1 < len(source) && source[i+1] == '$' {
				i++
			}
			continue
		}
		if strings.HasPrefix(source[i:], `\(`) && !isEscaped(source, i) {
			inMath = true
			i++
			continue
		}
		if strings.HasPrefix(source[i:], `\)`) && !isEscaped(source, i) {
			inMath = false
			i++
			continue
		}
		if source[i] != '\n' {
			continue
		}
		if inMath {
			continue
		}
		lineStart := i + 1
		position := lineStart
		for position < len(source) && (source[position] == ' ' || source[position] == '\t' || source[position] == '\r') {
			position++
		}
		if position < len(source) && source[position] == '\n' {
			return i
		}
		if position < len(source) && isBlockStart(source[position:]) {
			return i
		}
	}
	return len(source)
}

func isBlockStart(source string) bool {
	if strings.HasPrefix(source, `\begin{`) || strings.HasPrefix(source, `\attentionwindow`) ||
		strings.HasPrefix(source, `\[`) || strings.HasPrefix(source, `$$`) ||
		strings.HasPrefix(source, `\maketitle`) || strings.HasPrefix(source, `\tableofcontents`) ||
		strings.HasPrefix(source, `\appendix`) {
		return true
	}
	_, _, _, ok := parseHeading(source)
	return ok
}

func parseEnvironmentStart(source string) (string, int, bool) {
	if !strings.HasPrefix(source, `\begin{`) {
		return "", 0, false
	}
	name, end, ok := parseBraced(source, len(`\begin`))
	if !ok || name == "" {
		return "", 0, false
	}
	end = skipTeXSpace(source, end)
	if end < len(source) && source[end] == '[' {
		if _, optionalEnd, optionalOK := parseDelimited(source, end, '[', ']'); optionalOK {
			end = optionalEnd
		}
	}
	return name, end, true
}

func environmentOptions(source, environment string, contentStart int) string {
	position := len(`\begin{`) + len(environment) + len(`}`)
	position = skipTeXSpace(source, position)
	if position >= contentStart || position >= len(source) || source[position] != '[' {
		return ""
	}
	options, end, ok := parseDelimited(source, position, '[', ']')
	if !ok || end > contentStart {
		return ""
	}
	return options
}

func extractEnvironment(source, environment string, contentStart int) (string, int, bool) {
	beginToken := `\begin{` + environment + `}`
	endToken := `\end{` + environment + `}`
	depth := 1
	position := contentStart
	for position < len(source) {
		nextBegin := strings.Index(source[position:], beginToken)
		nextEnd := strings.Index(source[position:], endToken)
		if nextEnd < 0 {
			return "", 0, false
		}
		if nextBegin >= 0 && nextBegin < nextEnd {
			depth++
			position += nextBegin + len(beginToken)
			continue
		}
		absoluteEnd := position + nextEnd
		depth--
		if depth == 0 {
			return source[contentStart:absoluteEnd], absoluteEnd + len(endToken), true
		}
		position = absoluteEnd + len(endToken)
	}
	return "", 0, false
}

func standaloneMathEnvironment(source string) (string, string, bool) {
	source = strings.TrimSpace(source)
	for len(source) >= 2 && source[0] == '{' {
		inner, end, ok := parseBraced(source, 0)
		if !ok || skipTeXSpace(source, end) != len(source) {
			break
		}
		source = strings.TrimSpace(inner)
	}
	environment, contentStart, ok := parseEnvironmentStart(source)
	if !ok {
		return "", "", false
	}
	content, end, ok := extractEnvironment(source, environment, contentStart)
	if !ok || skipTeXSpace(source, end) != len(source) {
		return "", "", false
	}
	return environment, content, true
}

type listItem struct {
	Label   string
	Content string
	Offset  int
}

func splitListItems(content string) []listItem {
	type marker struct {
		start, content int
		label          string
	}
	var markers []marker
	depth := 0
	for i := 0; i < len(content); {
		if strings.HasPrefix(content[i:], `\begin{`) {
			_, end, ok := parseEnvironmentStart(content[i:])
			if ok {
				depth++
				i += end
				continue
			}
		}
		if strings.HasPrefix(content[i:], `\end{`) {
			_, end, ok := parseBraced(content[i:], len(`\end`))
			if ok {
				if depth > 0 {
					depth--
				}
				i += end
				continue
			}
		}
		if depth == 0 && strings.HasPrefix(content[i:], `\item`) && commandBoundary(content, i+len(`\item`)) {
			position := skipTeXSpace(content, i+len(`\item`))
			label := ""
			if position < len(content) && content[position] == '[' {
				if value, end, ok := parseDelimited(content, position, '[', ']'); ok {
					label = value
					position = end
				}
			}
			markers = append(markers, marker{start: i, content: position, label: label})
			i = position
			continue
		}
		_, size := utf8.DecodeRuneInString(content[i:])
		if size <= 0 {
			break
		}
		i += size
	}
	items := make([]listItem, 0, len(markers))
	for i, marker := range markers {
		end := len(content)
		if i+1 < len(markers) {
			end = markers[i+1].start
		}
		raw := content[marker.content:end]
		trimmedLeft := strings.TrimLeftFunc(raw, unicode.IsSpace)
		items = append(items, listItem{
			Label: marker.label, Content: strings.TrimSpace(raw),
			Offset: marker.content + len(raw) - len(trimmedLeft),
		})
	}
	return items
}

func commandBoundary(source string, position int) bool {
	return position >= len(source) || !unicode.IsLetter(rune(source[position]))
}

func parseBraced(source string, start int) (string, int, bool) {
	return parseDelimited(source, start, '{', '}')
}

func parseDelimited(source string, start int, open, close byte) (string, int, bool) {
	if start >= len(source) || source[start] != open {
		return "", start, false
	}
	depth := 1
	for i := start + 1; i < len(source); i++ {
		if source[i] == open && !isEscaped(source, i) {
			depth++
		} else if source[i] == close && !isEscaped(source, i) {
			depth--
			if depth == 0 {
				return source[start+1 : i], i + 1, true
			}
		}
	}
	return "", start, false
}

func findCommandArgument(source, name string) (string, bool) {
	needle := `\` + name
	for offset := 0; offset < len(source); {
		position := strings.Index(source[offset:], needle)
		if position < 0 {
			return "", false
		}
		position += offset
		after := position + len(needle)
		if commandBoundary(source, after) {
			after = skipTeXSpace(source, after)
			if argument, _, ok := parseBraced(source, after); ok {
				return argument, true
			}
		}
		offset = after
	}
	return "", false
}

func skipTeXSpace(source string, position int) int {
	for position < len(source) && unicode.IsSpace(rune(source[position])) {
		position++
	}
	return position
}

func isEscaped(source string, position int) bool {
	backslashes := 0
	for position > 0 && source[position-1] == '\\' {
		backslashes++
		position--
	}
	return backslashes%2 == 1
}

func findUnescaped(source, needle string, start int) int {
	for position := start; position < len(source); {
		next := strings.Index(source[position:], needle)
		if next < 0 {
			return -1
		}
		next += position
		if !isEscaped(source, next) {
			return next
		}
		position = next + len(needle)
	}
	return -1
}

func findMathDollar(source string, start int) int {
	depth := 0
	for i := start; i < len(source); i++ {
		if isEscaped(source, i) {
			continue
		}
		switch source[i] {
		case '{':
			depth++
		case '}':
			if depth > 0 {
				depth--
			}
		case '$':
			if depth == 0 {
				return i
			}
		}
	}
	return -1
}

func readTeXCommand(source string, start int) (string, int) {
	if start >= len(source) || source[start] != '\\' || start+1 >= len(source) {
		return "", 0
	}
	position := start + 1
	if !unicode.IsLetter(rune(source[position])) && source[position] != '@' {
		return source[position : position+1], position + 1
	}
	for position < len(source) {
		r, size := utf8.DecodeRuneInString(source[position:])
		if !unicode.IsLetter(r) && r != '@' {
			break
		}
		position += size
	}
	return source[start+1 : position], position
}

func escapedTextCommand(command string) (string, bool) {
	switch command {
	case "%":
		return "%", true
	case "$":
		return "$", true
	case "&":
		return "&amp;", true
	case "_":
		return "_", true
	case "#":
		return "#", true
	case "{":
		return "{", true
	case "}":
		return "}", true
	case "LaTeX":
		return "LaTeX", true
	case "TeX":
		return "TeX", true
	case "textbackslash":
		return `\`, true
	case "textasciitilde":
		return "~", true
	case "textasciicircum":
		return "^", true
	case "oe":
		return "œ", true
	case "OE":
		return "Œ", true
	case "ae":
		return "æ", true
	case "AE":
		return "Æ", true
	case "aa":
		return "å", true
	case "AA":
		return "Å", true
	case "o":
		return "ø", true
	case "O":
		return "Ø", true
	case "l":
		return "ł", true
	case "L":
		return "Ł", true
	case "ss":
		return "ß", true
	case "og":
		return "«", true
	case "fg":
		return "»", true
	case "S":
		return "§", true
	case "ldots", "dots":
		return "…", true
	default:
		return "", false
	}
}

func texLiteralText(source string) string {
	return strings.NewReplacer(
		`\textbackslash{}`, `\`,
		`\textasciitilde{}`, `~`,
		`\textasciicircum{}`, `^`,
		`\%`, `%`, `\$`, `$`, `\&`, `&`, `\_`, `_`, `\#`, `#`, `\{`, `{`, `\}`, `}`,
	).Replace(source)
}

func safeURL(value string) bool {
	parsed, err := url.Parse(strings.TrimSpace(value))
	if err != nil {
		return false
	}
	return parsed.Scheme == "http" || parsed.Scheme == "https" || parsed.Scheme == "mailto" || parsed.Scheme == ""
}

func stripLatexComments(source string) string {
	var output strings.Builder
	for _, line := range strings.SplitAfter(source, "\n") {
		cut := len(line)
		for i := 0; i < len(line); i++ {
			if line[i] == '%' && !isEscaped(line, i) {
				cut = i
				if strings.HasSuffix(line, "\n") {
					output.WriteString(line[:cut])
					output.WriteByte('\n')
				} else {
					output.WriteString(line[:cut])
				}
				break
			}
		}
		if cut == len(line) {
			output.WriteString(line)
		}
	}
	return output.String()
}

func documentBody(source string) string {
	begin := strings.Index(source, `\begin{document}`)
	if begin < 0 {
		return source
	}
	begin += len(`\begin{document}`)
	end := strings.LastIndex(source[begin:], `\end{document}`)
	if end < 0 {
		return source[begin:]
	}
	return source[begin : begin+end]
}

func latexToPlainText(source string) string {
	source = normalizeLatexSourceEncoding(source)
	return texFragmentToPlainText(documentBody(stripLatexComments(source)))
}

func texFragmentToPlainText(source string) string {
	var output strings.Builder
	for i := 0; i < len(source); {
		if source[i] == '\\' {
			command, end := readTeXCommand(source, i)
			if end == 0 {
				i++
				continue
			}
			if command == "texorpdfstring" {
				position := skipTeXSpace(source, end)
				if _, next, ok := parseBraced(source, position); ok {
					next = skipTeXSpace(source, next)
					if fallback, argumentEnd, fallbackOK := parseBraced(source, next); fallbackOK {
						output.WriteString(texFragmentToPlainText(fallback))
						i = argumentEnd
						continue
					}
				}
			}
			if command == "color" || command == "textcolor" {
				_, next, syntaxOK, _ := parseLatexColorArgument(source, end, baseLatexColors)
				if syntaxOK {
					if command == "color" {
						i = next
						continue
					}
					next = skipTeXSpace(source, next)
					if argument, argumentEnd, argumentOK := parseBraced(source, next); argumentOK {
						output.WriteString(texFragmentToPlainText(argument))
						i = argumentEnd
						continue
					}
				}
			}
			if isTextAccentCommand(command) {
				position := skipTeXSpace(source, end)
				if argument, argumentEnd, ok := parseTextAccentArgument(source, position); ok {
					output.WriteString(applyTextAccent(command, argument))
					i = argumentEnd
					continue
				}
			}
			if consumed, ok := consumeNonRenderingTeXCommand(source[i:], command, end-i); ok {
				i += consumed
				continue
			}
			if replacement, ok := escapedTextCommand(command); ok {
				output.WriteString(template.HTMLEscapeString(replacement))
				if group, groupEnd, groupOK := parseBraced(source, skipTeXSpace(source, end)); groupOK && group == "" && groupEnd > end {
					i = groupEnd
					continue
				}
			}
			if command == "item" || command == "section" || command == "subsection" || command == "attentionwindow" {
				output.WriteByte('\n')
			}
			i = end
			continue
		}
		switch source[i] {
		case '{', '}', '$', '&', '[', ']':
			output.WriteByte(' ')
		case '~':
			output.WriteByte(' ')
		default:
			output.WriteByte(source[i])
		}
		i++
	}
	return collapseWhitespace(output.String())
}

func extractMathMacros(source string) map[string]mathMacro {
	defaults := map[string]mathMacro{
		"Z": {body: `\mathbb{Z}`}, "Q": {body: `\mathbb{Q}`}, "R": {body: `\mathbb{R}`},
		"C": {body: `\mathbb{C}`}, "F": {body: `\mathbb{F}`}, "N": {body: `\mathbb{N}`},
		"bF": {body: `\mathbb{F}`}, "bFp": {body: `\bar{\mathbb{F}}_p`},
		"bQ": {body: `\mathbb{Q}`}, "bZ": {body: `\mathbb{Z}`}, "bdF": {body: `\mathbf{F}`},
		"GL": {body: `\mathrm{GL}`}, "SL": {body: `\mathrm{SL}`}, "PGL": {body: `\mathrm{PGL}`},
		"GSp": {body: `\mathrm{GSp}`}, "GSO": {body: `\mathrm{GSO}`}, "GSpin": {body: `\mathrm{GSpin}`},
		"SO": {body: `\mathrm{SO}`}, "Spin": {body: `\mathrm{Spin}`}, "Sp": {body: `\mathrm{Sp}`},
		"U": {body: `\mathrm{U}`}, "St": {body: `\mathrm{St}`}, "Sym": {body: `\operatorname{Sym}`},
		"ur": {body: `\operatorname{ur}`}, "eps": {body: `\bar\epsilon`}, "Gm": {body: `\mathbb{G}_m`},
		"j": {body: `\jmath`}, "rmU": {body: `\mathrm{U}`}, "cX": {body: `\mathcal{X}`},
		"bG": {body: `\breve{G}`}, "Chtop": {body: `\operatorname{Ch}_{\operatorname{top}}`},
		"Hom": {body: `\operatorname{Hom}`}, "im": {body: `\operatorname{im}`},
		"ker": {body: `\operatorname{ker}`}, "coker": {body: `\operatorname{coker}`},
		"Gal": {body: `\operatorname{Gal}`}, "Ga": {body: `\operatorname{G}_a`},
		"Res": {body: `\operatorname{Res}`}, "Remain": {body: `\operatorname{res}`},
		"Der": {body: `\operatorname{der}`}, "Sim": {body: `\operatorname{sim}`},
		"Det": {body: `\operatorname{det}`}, "red": {body: `\operatorname{red}`},
		"spec": {body: `\operatorname{Spec}`}, "cO": {body: `\mathcal{O}`},
		"lsup":       {body: `{^{#1}\!#2}`, arguments: 2},
		"Spec":       {body: `\operatorname{Spec}`},
		"dots":       {body: `\ldots`},
		"pmod":       {body: `\;({\rm mod}\;#1)`, arguments: 1},
		"varprojlim": {body: `\mathop{\mathrm{lim}\limits_{\longleftarrow}}`},
		"varinjlim":  {body: `\mathop{\mathrm{lim}\limits_{\longrightarrow}}`},
		"qed":        {body: `\square`},
	}
	macros := make(map[string]mathMacro, len(defaults)+16)
	for name, definition := range defaults {
		macros[name] = definition
	}
	for name, definition := range extractMathAlphabetMacros(source) {
		macros[name] = definition
	}
	for position := 0; position < len(source); {
		next, command := nextMacroCommand(source, position)
		if next < 0 {
			break
		}
		cursor := next + len(command) + 1
		if cursor < len(source) && source[cursor] == '*' {
			cursor++
		}
		cursor = skipTeXSpace(source, cursor)
		macroName := ""
		if cursor < len(source) && source[cursor] == '{' {
			value, end, ok := parseBraced(source, cursor)
			if !ok {
				position = cursor + 1
				continue
			}
			macroName = strings.TrimPrefix(strings.TrimSpace(value), `\`)
			cursor = end
		} else if cursor < len(source) && source[cursor] == '\\' {
			macroName, cursor = readTeXCommand(source, cursor)
		} else {
			position = cursor + 1
			continue
		}
		cursor = skipTeXSpace(source, cursor)
		argumentCount := 0
		optionalDefault := ""
		hasOptional := false
		if cursor < len(source) && source[cursor] == '[' {
			value, end, ok := parseDelimited(source, cursor, '[', ']')
			if ok {
				argumentCount, _ = strconvAtoi(strings.TrimSpace(value))
				cursor = skipTeXSpace(source, end)
				if cursor < len(source) && source[cursor] == '[' {
					if value, optionalEnd, optionalOK := parseDelimited(source, cursor, '[', ']'); optionalOK {
						optionalDefault = value
						hasOptional = true
						cursor = skipTeXSpace(source, optionalEnd)
					}
				}
			}
		}
		replacement, end, ok := parseBraced(source, cursor)
		if !ok {
			position = cursor + 1
			continue
		}
		if strings.HasPrefix(command, "DeclareMathOperator") {
			replacement = `\operatorname{` + replacement + `}`
			argumentCount = 0
		}
		if strings.Contains(replacement, `\tikz`) {
			if argumentCount > 0 {
				replacement = `\boxed{#1}`
				argumentCount = 1
			} else {
				replacement = ""
			}
		}
		if macroName != "" && argumentCount <= 9 {
			macros[macroName] = mathMacro{
				body: replacement, arguments: argumentCount,
				optionalDefault: optionalDefault, hasOptional: hasOptional,
			}
		}
		position = end
	}
	return macros
}

func expandMathMacros(source string, macros map[string]mathMacro) string {
	for pass := 0; pass < 32; pass++ {
		var output strings.Builder
		changed := false
		for position := 0; position < len(source); {
			if source[position] != '\\' {
				_, size := utf8.DecodeRuneInString(source[position:])
				if size <= 0 {
					break
				}
				output.WriteString(source[position : position+size])
				position += size
				continue
			}
			name, commandEnd := readTeXCommand(source, position)
			if commandEnd == 0 {
				output.WriteByte(source[position])
				position++
				continue
			}
			definition, ok := macros[name]
			trailingAt := ""
			if !ok {
				trimmedName := strings.TrimRight(name, "@")
				if trimmedName != name {
					if candidate, found := macros[trimmedName]; found {
						definition, ok = candidate, true
						trailingAt = name[len(trimmedName):]
					}
				}
			}
			if !ok {
				output.WriteString(source[position:commandEnd])
				position = commandEnd
				continue
			}
			replacement, cursor, valid := expandMathMacroInvocation(source, commandEnd, definition)
			if !valid {
				output.WriteString(source[position:commandEnd])
				position = commandEnd
				continue
			}
			output.WriteByte('{')
			output.WriteString(replacement)
			output.WriteByte('}')
			output.WriteString(trailingAt)
			position = cursor
			changed = true
		}
		source = output.String()
		if !changed {
			break
		}
	}
	return source
}

func expandMathMacroInvocation(source string, commandEnd int, definition mathMacro) (string, int, bool) {
	cursor := commandEnd
	arguments := make([]string, 0, definition.arguments)
	if definition.hasOptional {
		cursor = skipTeXSpace(source, cursor)
		argument := definition.optionalDefault
		if cursor < len(source) && source[cursor] == '[' {
			var ok bool
			argument, cursor, ok = parseDelimited(source, cursor, '[', ']')
			if !ok {
				return "", commandEnd, false
			}
		}
		arguments = append(arguments, argument)
	}
	for index := len(arguments); index < definition.arguments; index++ {
		cursor = skipTeXSpace(source, cursor)
		if cursor >= len(source) {
			return "", commandEnd, false
		}
		if source[cursor] == '{' {
			argument, end, ok := parseBraced(source, cursor)
			if !ok {
				return "", commandEnd, false
			}
			arguments = append(arguments, argument)
			cursor = end
		} else if source[cursor] == '\\' {
			_, end := readTeXCommand(source, cursor)
			if end <= cursor {
				return "", commandEnd, false
			}
			arguments = append(arguments, source[cursor:end])
			cursor = end
		} else {
			_, size := utf8.DecodeRuneInString(source[cursor:])
			if size <= 0 {
				return "", commandEnd, false
			}
			arguments = append(arguments, source[cursor:cursor+size])
			cursor += size
		}
	}
	replacement := definition.body
	for index, argument := range arguments {
		replacement = strings.ReplaceAll(replacement, fmt.Sprintf("#%d", index+1), "{"+argument+"}")
	}
	return replacement, cursor, true
}

var simpleScriptPattern = regexp.MustCompile(`([_^])([A-Za-z0-9])`)

func normalizeMathForEngine(source string) string {
	return normalizeMathForEngineProtected(source, nil)
}

func normalizeMathForEngineProtected(source string, protected map[string]bool) string {
	// mathjax-go deliberately tracks a frozen MathJax pipeline and is stricter
	// than XeTeX about adjacent unbraced scripts such as \otimes_RM. Bracing the
	// single script token preserves TeX semantics and avoids a false double-
	// subscript error without changing the archived source. Its frozen parser
	// also treats a label attached directly to an under/overbrace as a second
	// script; expressing that label with \underset/\overset is equivalent.
	source = normalizeXYMatrices(source)
	source = tableRulePattern.ReplaceAllString(source, "")
	source = normalizeNestedAligned(source)
	source = unwrapMathLayoutCommands(source)
	source = unwrapMathPDFStrings(source)
	source = rewriteMathCompatibilityCommands(source, protected)
	source = normalizeMathControlSpaces(source)
	source = normalizeCDLeadingTabs(source)
	source = groupTableEnvironments(source)
	source = strings.NewReplacer(
		`\lhook\joinrel\longrightarrow`, `\hookrightarrow`,
		`\big/`, `/`,
		`\bbpi`, `\boldsymbol{\pi}`,
		`\qedhere`, ``,
		`\protect`, ``,
		`\scriptsize`, ``,
		`\footnotesize`, ``,
		`\sf`, ``,
		`\relax`, ``,
		`\hline`, ``,
		`\cr`, `\\`,
	).Replace(source)
	source = stripMathCommandArguments(source, `\label`)
	source = stripMathCommandArguments(source, `\xymatrixcolsep`)
	source = stripMathCommandArguments(source, `\xymatrixrowsep`)
	source = rewriteBraceScripts(source)
	source = braceAccentArguments(source)
	source = normalizePrimeExponentScripts(source)
	source = braceCommandScripts(source)
	return simpleScriptPattern.ReplaceAllString(source, `$1{$2}`)
}

func extractMathAlphabetMacros(source string) map[string]mathMacro {
	result := make(map[string]mathMacro)
	declarations := []struct {
		command   string
		arguments int
	}{
		{command: `\DeclareMathAlphabet`, arguments: 5},
		{command: `\DeclareSymbolFontAlphabet`, arguments: 2},
	}
	for _, declaration := range declarations {
		for searchFrom := 0; searchFrom < len(source); {
			relative := strings.Index(source[searchFrom:], declaration.command)
			if relative < 0 {
				break
			}
			position := searchFrom + relative
			commandEnd := position + len(declaration.command)
			if !commandBoundary(source, commandEnd) {
				searchFrom = commandEnd
				continue
			}
			cursor := skipTeXSpace(source, commandEnd)
			values := make([]string, 0, declaration.arguments)
			valid := true
			for range declaration.arguments {
				value, end, ok := parseBraced(source, cursor)
				if !ok {
					valid = false
					break
				}
				values = append(values, strings.TrimSpace(value))
				cursor = skipTeXSpace(source, end)
			}
			if !valid || len(values) == 0 {
				searchFrom = commandEnd
				continue
			}
			name := strings.TrimPrefix(values[0], `\`)
			family := strings.ToLower(strings.Join(values[1:], " "))
			wrapper := `\mathit`
			switch {
			case strings.Contains(family, "bb") || strings.Contains(strings.ToLower(name), "bb"):
				wrapper = `\mathbb`
			case strings.Contains(family, "pzc") || strings.Contains(family, "script"):
				wrapper = `\mathcal`
			case strings.Contains(family, "bold") || strings.Contains(family, " bx"):
				wrapper = `\mathbf`
			}
			if name != "" {
				result[name] = mathMacro{body: wrapper + `{#1}`, arguments: 1}
			}
			searchFrom = cursor
		}
	}
	return result
}

func rewriteMathCompatibilityCommands(source string, protected map[string]bool) string {
	replacements := map[string]string{
		"L":           `\text{Ł}`,
		"dag":         `\dagger`,
		"emph":        `\textit`,
		"llbracket":   `\lbrack\!\lbrack`,
		"mathpzc":     `\mathcal`,
		"mathsection": `\text{§}`,
		"rrbracket":   `\rbrack\!\rbrack`,
	}
	var output strings.Builder
	for position := 0; position < len(source); {
		if source[position] != '\\' {
			_, size := utf8.DecodeRuneInString(source[position:])
			if size <= 0 {
				break
			}
			output.WriteString(source[position : position+size])
			position += size
			continue
		}
		name, end := readTeXCommand(source, position)
		if replacement, ok := replacements[name]; ok && !protected[name] {
			output.WriteString(replacement)
		} else if end > position {
			output.WriteString(source[position:end])
		} else {
			output.WriteByte(source[position])
			end = position + 1
		}
		position = end
	}
	return output.String()
}

func extractPrimitiveMacroNames(source string) map[string]bool {
	result := make(map[string]bool)
	commands := []string{`\def`, `\gdef`, `\edef`, `\xdef`}
	for _, command := range commands {
		for searchFrom := 0; searchFrom < len(source); {
			relative := strings.Index(source[searchFrom:], command)
			if relative < 0 {
				break
			}
			position := searchFrom + relative
			commandEnd := position + len(command)
			if !commandBoundary(source, commandEnd) {
				searchFrom = commandEnd
				continue
			}
			cursor := skipTeXSpace(source, commandEnd)
			if cursor < len(source) && source[cursor] == '\\' {
				name, end := readTeXCommand(source, cursor)
				if name != "" {
					result[name] = true
				}
				searchFrom = max(end, cursor+1)
				continue
			}
			searchFrom = commandEnd
		}
	}
	return result
}

func normalizeMathControlSpaces(source string) string {
	var output strings.Builder
	for position := 0; position < len(source); {
		// A TeX table row terminator followed by source whitespace is `\\` plus
		// whitespace, not a one-character command-space. Examining its second
		// backslash in isolation would turn `\\\n` into `\\,` and visibly add a
		// comma at the beginning of the next matrix/alignment row.
		if strings.HasPrefix(source[position:], `\\`) {
			output.WriteString(`\\`)
			position += 2
			continue
		}
		if source[position] != '\\' || position+1 >= len(source) || !unicode.IsSpace(rune(source[position+1])) {
			_, size := utf8.DecodeRuneInString(source[position:])
			if size <= 0 {
				break
			}
			output.WriteString(source[position : position+size])
			position += size
			continue
		}
		output.WriteString(`\,`)
		position++
		for position < len(source) && unicode.IsSpace(rune(source[position])) {
			_, size := utf8.DecodeRuneInString(source[position:])
			position += size
		}
	}
	return output.String()
}

func normalizePrimeExponentScripts(source string) string {
	for position := 0; position < len(source); {
		if source[position] != '\'' {
			_, size := utf8.DecodeRuneInString(source[position:])
			if size <= 0 {
				break
			}
			position += size
			continue
		}
		primeEnd := position
		for primeEnd < len(source) && source[primeEnd] == '\'' {
			primeEnd++
		}
		exponent := skipTeXSpace(source, primeEnd)
		if exponent >= len(source) || source[exponent] != '^' {
			position = primeEnd
			continue
		}
		baseStart, ok := precedingMathAtomStart(source, position)
		if !ok {
			position = primeEnd
			continue
		}
		source = source[:baseStart] + `{` + source[baseStart:primeEnd] + `}` + source[primeEnd:]
		position = primeEnd + 2
	}
	return source
}

func precedingMathAtomStart(source string, end int) (int, bool) {
	if end <= 0 {
		return 0, false
	}
	last := end - 1
	if source[last] == '}' && !isEscaped(source, last) {
		depth := 1
		for position := last - 1; position >= 0; position-- {
			switch source[position] {
			case '}':
				if !isEscaped(source, position) {
					depth++
				}
			case '{':
				if !isEscaped(source, position) {
					depth--
					if depth == 0 {
						return position, true
					}
				}
			}
		}
		return 0, false
	}
	if isTeXCommandLetter(source[last]) {
		start := last
		for start > 0 && isTeXCommandLetter(source[start-1]) {
			start--
		}
		if start > 0 && source[start-1] == '\\' && !isEscaped(source, start-1) {
			return start - 1, true
		}
	}
	_, size := utf8.DecodeLastRuneInString(source[:end])
	if size <= 0 {
		return 0, false
	}
	return end - size, true
}

func braceCommandScripts(source string) string {
	for position := 0; position < len(source); {
		if source[position] != '_' && source[position] != '^' {
			position++
			continue
		}
		atomStart := skipTeXSpace(source, position+1)
		if atomStart >= len(source) || source[atomStart] != '\\' {
			position++
			continue
		}
		_, commandEnd := readTeXCommand(source, atomStart)
		if commandEnd <= atomStart {
			position++
			continue
		}
		atomEnd := commandEnd
		argumentStart := skipTeXSpace(source, commandEnd)
		name := strings.TrimPrefix(source[atomStart:commandEnd], `\`)
		if mathCommandTakesArgument(name) && argumentStart < len(source) && source[argumentStart] == '{' {
			if _, end, ok := parseBraced(source, argumentStart); ok {
				atomEnd = end
			}
		}
		replacement := string(source[position]) + `{` + source[atomStart:atomEnd] + `}`
		source = source[:position] + replacement + source[atomEnd:]
		position += len(replacement)
	}
	return source
}

func mathCommandTakesArgument(name string) bool {
	switch name {
	case "mathbf", "mathrm", "mathit", "mathsf", "mathtt", "mathbb", "mathcal", "mathfrak", "mathscr",
		"operatorname", "text", "textrm", "textit", "textbf", "textsf", "texttt", "boldsymbol", "pmb":
		return true
	default:
		return false
	}
}

func unwrapMathPDFStrings(source string) string {
	const command = `\texorpdfstring`
	for searchFrom := 0; searchFrom < len(source); {
		relative := strings.Index(source[searchFrom:], command)
		if relative < 0 {
			break
		}
		position := searchFrom + relative
		commandEnd := position + len(command)
		if !commandBoundary(source, commandEnd) {
			searchFrom = commandEnd
			continue
		}
		cursor := skipTeXSpace(source, commandEnd)
		visible, next, ok := parseBraced(source, cursor)
		if !ok {
			searchFrom = commandEnd
			continue
		}
		next = skipTeXSpace(source, next)
		_, end, ok := parseBraced(source, next)
		if !ok {
			searchFrom = commandEnd
			continue
		}
		source = source[:position] + visible + source[end:]
		searchFrom = position
	}
	return source
}

func normalizeXYMatrices(source string) string {
	var output strings.Builder
	for position := 0; position < len(source); {
		relative := strings.Index(source[position:], `\xymatrix`)
		if relative < 0 {
			output.WriteString(source[position:])
			break
		}
		start := position + relative
		commandEnd := start + len(`\xymatrix`)
		if !commandBoundary(source, commandEnd) {
			output.WriteString(source[position:commandEnd])
			position = commandEnd
			continue
		}
		cursor := skipTeXSpace(source, commandEnd)
		body, end, ok := parseBraced(source, cursor)
		if !ok {
			output.WriteString(source[position:commandEnd])
			position = commandEnd
			continue
		}
		output.WriteString(source[position:start])
		output.WriteString(`{\begin{matrix}`)
		output.WriteString(normalizeXYArrows(body))
		output.WriteString(`\end{matrix}}`)
		position = end
	}
	return output.String()
}

func normalizeXYArrows(source string) string {
	var output strings.Builder
	for position := 0; position < len(source); {
		relative := strings.Index(source[position:], `\ar`)
		if relative < 0 {
			output.WriteString(source[position:])
			break
		}
		start := position + relative
		commandEnd := start + len(`\ar`)
		if !commandBoundary(source, commandEnd) {
			output.WriteString(source[position:commandEnd])
			position = commandEnd
			continue
		}
		cursor := commandEnd
		style := ""
		if cursor < len(source) && source[cursor] == '@' {
			cursor++
			if cursor < len(source) && source[cursor] == '{' {
				if value, end, ok := parseBraced(source, cursor); ok {
					style = value
					cursor = end
				}
			}
		}
		cursor = skipTeXSpace(source, cursor)
		if cursor >= len(source) || source[cursor] != '[' {
			output.WriteString(source[position:commandEnd])
			position = commandEnd
			continue
		}
		direction, end, ok := parseDelimited(source, cursor, '[', ']')
		if !ok {
			output.WriteString(source[position:commandEnd])
			position = commandEnd
			continue
		}
		output.WriteString(source[position:start])
		output.WriteString(xyArrowSymbol(direction, style))
		position = end
	}
	return output.String()
}

func xyArrowSymbol(direction, style string) string {
	direction = strings.ToLower(direction)
	arrow := `\longrightarrow`
	switch {
	case strings.Contains(direction, "u") && strings.Contains(direction, "r"):
		arrow = `\nearrow`
	case strings.Contains(direction, "d") && strings.Contains(direction, "r"):
		arrow = `\searrow`
	case strings.Contains(direction, "u") && strings.Contains(direction, "l"):
		arrow = `\nwarrow`
	case strings.Contains(direction, "d") && strings.Contains(direction, "l"):
		arrow = `\swarrow`
	case strings.Contains(direction, "u"):
		arrow = `\uparrow`
	case strings.Contains(direction, "d"):
		arrow = `\downarrow`
	case strings.Contains(direction, "l"):
		arrow = `\longleftarrow`
	}
	if strings.Contains(style, "=") {
		arrow = "="
	} else if strings.Contains(style, "(->") && strings.Contains(direction, "r") {
		arrow = `\hookrightarrow`
	}
	return `\mathrel{` + arrow + `}`
}

func normalizeNestedAligned(source string) string {
	const begin = `\begin{aligned}`
	const end = `\end{aligned}`
	depth := 0
	var nested []bool
	var output strings.Builder
	for position := 0; position < len(source); {
		nextBegin := strings.Index(source[position:], begin)
		nextEnd := strings.Index(source[position:], end)
		if nextBegin < 0 && nextEnd < 0 {
			output.WriteString(source[position:])
			break
		}
		isBegin := nextBegin >= 0 && (nextEnd < 0 || nextBegin < nextEnd)
		relative := nextEnd
		token := end
		if isBegin {
			relative = nextBegin
			token = begin
		}
		next := position + relative
		output.WriteString(source[position:next])
		if isBegin {
			isNested := depth > 0
			nested = append(nested, isNested)
			depth++
			if isNested {
				output.WriteString(`\begin{array}{rl}`)
			} else {
				output.WriteString(begin)
			}
		} else {
			isNested := len(nested) > 0 && nested[len(nested)-1]
			if len(nested) > 0 {
				nested = nested[:len(nested)-1]
			}
			if depth > 0 {
				depth--
			}
			if isNested {
				output.WriteString(`\end{array}`)
			} else {
				output.WriteString(end)
			}
		}
		position = next + len(token)
	}
	return output.String()
}

func normalizeCDLeadingTabs(source string) string {
	const beginToken = `\begin{CD}`
	const endToken = `\end{CD}`
	for searchFrom := 0; searchFrom < len(source); {
		begin := strings.Index(source[searchFrom:], beginToken)
		if begin < 0 {
			break
		}
		begin += searchFrom
		contentStart := begin + len(beginToken)
		end := strings.Index(source[contentStart:], endToken)
		if end < 0 {
			break
		}
		end += contentStart
		rows := splitTopLevelTeXRows(source[contentStart:end])
		for index, row := range rows {
			trimmed := strings.TrimSpace(row)
			leadingTabs := 0
			for leadingTabs < len(trimmed) && trimmed[leadingTabs] == '&' {
				leadingTabs++
			}
			if leadingTabs > 0 {
				trimmed = strings.TrimSpace(trimmed[leadingTabs:])
				trimmed = strings.Repeat(`@.`, max(0, leadingTabs-1)) + trimmed
			}
			rows[index] = trimmed
		}
		replacement := strings.Join(rows, `\\`)
		source = source[:contentStart] + replacement + source[end:]
		searchFrom = contentStart + len(replacement) + len(endToken)
	}
	return source
}

func stripMathCommandArguments(source, command string) string {
	searchFrom := 0
	for {
		relative := strings.Index(source[searchFrom:], command)
		if relative < 0 {
			return source
		}
		position := searchFrom + relative
		commandEnd := position + len(command)
		if !commandBoundary(source, commandEnd) {
			searchFrom = commandEnd
			continue
		}
		cursor := skipTeXSpace(source, commandEnd)
		_, end, ok := parseBraced(source, cursor)
		if !ok {
			return source
		}
		source = source[:position] + source[end:]
	}
}

func stripMathTags(source string) string {
	searchFrom := 0
	for {
		relative := strings.Index(source[searchFrom:], `\tag`)
		if relative < 0 {
			return source
		}
		position := searchFrom + relative
		cursor := position + len(`\tag`)
		if !commandBoundary(source, cursor) && (cursor >= len(source) || source[cursor] != '*') {
			searchFrom = cursor
			continue
		}
		if cursor < len(source) && source[cursor] == '*' {
			cursor++
		}
		cursor = skipTeXSpace(source, cursor)
		_, end, ok := parseBraced(source, cursor)
		if !ok {
			searchFrom = cursor
			continue
		}
		source = source[:position] + source[end:]
	}
}

func stripMathDefinitions(source string) string {
	for _, command := range []string{`\newcommand`, `\renewcommand`, `\providecommand`} {
		for {
			position := strings.Index(source, command)
			if position < 0 {
				break
			}
			cursor := skipTeXSpace(source, position+len(command))
			if cursor < len(source) && source[cursor] == '*' {
				cursor = skipTeXSpace(source, cursor+1)
			}
			if _, end, ok := parseBraced(source, cursor); ok {
				cursor = skipTeXSpace(source, end)
			} else if cursor < len(source) && source[cursor] == '\\' {
				_, cursor = readTeXCommand(source, cursor)
				cursor = skipTeXSpace(source, cursor)
			} else {
				break
			}
			for cursor < len(source) && source[cursor] == '[' {
				_, end, ok := parseDelimited(source, cursor, '[', ']')
				if !ok {
					break
				}
				cursor = skipTeXSpace(source, end)
			}
			if _, end, ok := parseBraced(source, cursor); ok {
				source = source[:position] + source[end:]
			} else {
				break
			}
		}
	}
	return source
}

func unwrapMathLayoutCommands(source string) string {
	for {
		position := strings.Index(source, `\resizebox`)
		if position < 0 {
			break
		}
		cursor := position + len(`\resizebox`)
		arguments := make([]string, 0, 3)
		valid := true
		for index := 0; index < 3; index++ {
			cursor = skipTeXSpace(source, cursor)
			argument, end, ok := parseBraced(source, cursor)
			if !ok {
				valid = false
				break
			}
			arguments = append(arguments, argument)
			cursor = end
		}
		if !valid {
			break
		}
		source = source[:position] + `{` + arguments[2] + `}` + source[cursor:]
	}
	for _, command := range []string{`\ensuremath`, `\mathlarger`, `\reflectbox`} {
		source = unwrapMathFinalArgument(source, command, false, 0, false)
	}
	source = unwrapMathFinalArgument(source, `\scalebox`, false, 1, true)
	source = unwrapMathFinalArgument(source, `\rotatebox`, true, 1, false)
	source = unwrapMathFinalArgument(source, `\raisebox`, false, 1, true)
	for {
		position := strings.Index(source, `\multicolumn`)
		if position < 0 {
			break
		}
		cursor := position + len(`\multicolumn`)
		arguments := make([]string, 0, 3)
		valid := true
		for index := 0; index < 3; index++ {
			cursor = skipTeXSpace(source, cursor)
			argument, end, ok := parseBraced(source, cursor)
			if !ok {
				valid = false
				break
			}
			arguments = append(arguments, argument)
			cursor = end
		}
		if !valid {
			break
		}
		source = source[:position] + `{` + arguments[2] + `}` + source[cursor:]
	}
	return source
}

func unwrapMathFinalArgument(source, command string, optionalBefore bool, precedingArguments int, optionalAfter bool) string {
	for searchFrom := 0; searchFrom < len(source); {
		relative := strings.Index(source[searchFrom:], command)
		if relative < 0 {
			break
		}
		position := searchFrom + relative
		commandEnd := position + len(command)
		if !commandBoundary(source, commandEnd) {
			searchFrom = commandEnd
			continue
		}
		cursor := skipTeXSpace(source, commandEnd)
		if optionalBefore && cursor < len(source) && source[cursor] == '[' {
			if _, end, ok := parseDelimited(source, cursor, '[', ']'); ok {
				cursor = skipTeXSpace(source, end)
			} else {
				searchFrom = commandEnd
				continue
			}
		}
		valid := true
		for range precedingArguments {
			_, end, ok := parseBraced(source, cursor)
			if !ok {
				valid = false
				break
			}
			cursor = skipTeXSpace(source, end)
		}
		if !valid {
			searchFrom = commandEnd
			continue
		}
		if optionalAfter {
			for cursor < len(source) && source[cursor] == '[' {
				_, end, ok := parseDelimited(source, cursor, '[', ']')
				if !ok {
					valid = false
					break
				}
				cursor = skipTeXSpace(source, end)
			}
		}
		argument, end, ok := parseBraced(source, cursor)
		if !valid || !ok {
			searchFrom = commandEnd
			continue
		}
		source = source[:position] + `{` + argument + `}` + source[end:]
		searchFrom = position
	}
	return source
}

func groupTableEnvironments(source string) string {
	for _, environment := range []string{
		"matrix", "smallmatrix", "array", "cases", "subarray", "crampedsubarray",
		"pmatrix", "bmatrix", "Bmatrix", "vmatrix", "Vmatrix",
		"psmallmatrix", "bsmallmatrix", "Bsmallmatrix", "vsmallmatrix", "Vsmallmatrix",
	} {
		begin := `\begin{` + environment + `}`
		end := `\end{` + environment + `}`
		source = strings.ReplaceAll(source, begin, `{`+begin)
		source = strings.ReplaceAll(source, end, end+`}`)
	}
	return source
}

func braceAccentArguments(source string) string {
	accents := map[string]bool{
		"bar": true, "hat": true, "widehat": true, "tilde": true,
		"widetilde": true, "vec": true, "overline": true, "underline": true,
		"dot": true, "ddot": true, "breve": true, "check": true,
	}
	var output strings.Builder
	for position := 0; position < len(source); {
		if source[position] != '\\' {
			_, size := utf8.DecodeRuneInString(source[position:])
			if size <= 0 {
				break
			}
			output.WriteString(source[position : position+size])
			position += size
			continue
		}
		name, commandEnd := readTeXCommand(source, position)
		if commandEnd == 0 || !accents[name] {
			if commandEnd > position {
				output.WriteString(source[position:commandEnd])
				position = commandEnd
			} else {
				output.WriteByte(source[position])
				position++
			}
			continue
		}
		cursor := skipTeXSpace(source, commandEnd)
		if cursor >= len(source) {
			output.WriteString(source[position:commandEnd])
			position = commandEnd
			continue
		}
		argument := ""
		atomEnd := cursor
		if source[cursor] == '{' {
			value, end, ok := parseBraced(source, cursor)
			if !ok {
				output.WriteString(source[position:commandEnd])
				position = commandEnd
				continue
			}
			argument, atomEnd = value, end
		} else if source[cursor] == '\\' {
			_, atomEnd = readTeXCommand(source, cursor)
			argument = source[cursor:atomEnd]
		} else {
			_, size := utf8.DecodeRuneInString(source[cursor:])
			atomEnd = cursor + size
			argument = source[cursor:atomEnd]
		}
		if atomEnd <= cursor {
			output.WriteString(source[position:commandEnd])
			position = commandEnd
			continue
		}
		output.WriteString(`{\` + name + `{` + braceAccentArguments(argument) + `}}`)
		position = atomEnd
	}
	return output.String()
}

func rewriteBraceScripts(source string) string {
	var output strings.Builder
	for position := 0; position < len(source); {
		command := ""
		script := byte(0)
		wrapper := ""
		switch {
		case strings.HasPrefix(source[position:], `\underbrace`):
			command, script, wrapper = `\underbrace`, '_', `\underset`
		case strings.HasPrefix(source[position:], `\overbrace`):
			command, script, wrapper = `\overbrace`, '^', `\overset`
		}
		if command != "" {
			argumentStart := skipTeXSpace(source, position+len(command))
			base, baseEnd, baseOK := parseBraced(source, argumentStart)
			scriptStart := skipTeXSpace(source, baseEnd)
			if baseOK && scriptStart < len(source) && source[scriptStart] == script {
				valueStart := skipTeXSpace(source, scriptStart+1)
				value, valueEnd, valueOK := parseBraced(source, valueStart)
				if !valueOK && valueStart < len(source) {
					if source[valueStart] == '\\' {
						_, valueEnd = readTeXCommand(source, valueStart)
						value = source[valueStart:valueEnd]
						valueOK = valueEnd > valueStart
					} else {
						_, size := utf8.DecodeRuneInString(source[valueStart:])
						if size > 0 {
							value, valueEnd, valueOK = source[valueStart:valueStart+size], valueStart+size, true
						}
					}
				}
				if valueOK {
					output.WriteString(wrapper + `{` + value + `}{` + command + `{` + base + `}}`)
					position = valueEnd
					continue
				}
			}
		}
		_, size := utf8.DecodeRuneInString(source[position:])
		if size <= 0 {
			break
		}
		output.WriteString(source[position : position+size])
		position += size
	}
	return output.String()
}

func nextMacroCommand(source string, start int) (int, string) {
	commands := []string{"newcommand", "renewcommand", "providecommand", "DeclareMathOperator"}
	best := -1
	selected := ""
	for _, command := range commands {
		position := strings.Index(source[start:], `\`+command)
		if position < 0 {
			continue
		}
		position += start
		if best < 0 || position < best {
			best = position
			selected = command
		}
	}
	return best, selected
}

func strconvAtoi(value string) (int, error) {
	value = strings.TrimSpace(value)
	result := 0
	if value == "" {
		return 0, fmt.Errorf("empty integer")
	}
	for _, r := range value {
		if r < '0' || r > '9' {
			return 0, fmt.Errorf("not an integer")
		}
		result = result*10 + int(r-'0')
	}
	return result, nil
}

func stripAlignatArgument(content string) string {
	trimmed := strings.TrimSpace(content)
	if strings.HasPrefix(trimmed, "{") {
		if _, end, ok := parseBraced(trimmed, 0); ok {
			return trimmed[end:]
		}
	}
	return content
}

func titleCase(value string) string {
	if value == "" {
		return value
	}
	return strings.ToUpper(value[:1]) + value[1:]
}
