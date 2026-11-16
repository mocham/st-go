package latexpdf

import (
	"regexp"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

var latexDocumentTypePattern = regexp.MustCompile(`(?mi)^[\t ]*%[\t ]*document-type[\t ]*:[\t ]*([a-z][a-z0-9_-]*)[\t ]*$`)

const defaultRenderPageBytes = 8 << 10

func latexDocumentSections(pages []latexSourcePage) []documentSection {
	var sections []documentSection
	bodyOffset := 0
	sectionSerial := 0
	for pageIndex, page := range pages {
		if page.Oversized {
			bodyOffset += len(page.Source)
			continue
		}
		pageSections := extractLatexSections(page.Source)
		for index := range pageSections {
			if strings.HasPrefix(pageSections[index].ID, "frame-") {
				offset, _ := strconv.Atoi(strings.TrimPrefix(pageSections[index].ID, "frame-"))
				pageSections[index].ID = "frame-" + strconv.Itoa(bodyOffset+offset)
			} else {
				sectionSerial++
				pageSections[index].ID = "section-" + strconv.Itoa(sectionSerial)
			}
			pageSections[index].Page = pageIndex + 1
		}
		sections = append(sections, pageSections...)
		bodyOffset += len(page.Source)
	}
	return sections
}

func extractLatexSections(source string) []documentSection {
	var sections []documentSection
	var scan func(string, int)
	add := func(titleSource string, level int) {
		title := collapseWhitespace(texFragmentToPlainText(titleSource))
		if title == "" {
			title = "Untitled section"
		}
		sections = append(sections, documentSection{
			ID: "section-" + strconv.Itoa(len(sections)+1), Title: title, Level: level,
		})
	}
	scan = func(fragment string, offset int) {
		originalLength := len(fragment)
		for len(fragment) > 0 {
			fragment = strings.TrimLeftFunc(fragment, unicode.IsSpace)
			if fragment == "" {
				return
			}
			if strings.HasPrefix(fragment, `\maketitle`) {
				fragment = fragment[len(`\maketitle`):]
				continue
			}
			if strings.HasPrefix(fragment, `\attentionwindow`) {
				position := skipTeXSpace(fragment, len(`\attentionwindow`))
				if _, next, ok := parseBraced(fragment, position); ok {
					next = skipTeXSpace(fragment, next)
					if title, end, titleOK := parseBraced(fragment, next); titleOK {
						add(title, 2)
						fragment = fragment[end:]
						continue
					}
				}
			}
			if command, title, end, ok := parseHeading(fragment); ok {
				level := 2
				if command == "subsection" {
					level = 3
				} else if command == "subsubsection" || command == "paragraph" {
					level = 4
				}
				add(title, level)
				fragment = fragment[end:]
				continue
			}
			if strings.HasPrefix(fragment, `\begin{`) {
				if environment, beginEnd, ok := parseEnvironmentStart(fragment); ok {
					if content, end, found := extractEnvironment(fragment, environment, beginEnd); found {
						position := offset + originalLength - len(fragment)
						if environment == "frame" {
							title, _, body, consumed := beamerFrameHeading(content, true)
							if strings.TrimSpace(title) == "" {
								title = "Slide"
							}
							add(title, 2)
							sections[len(sections)-1].ID = "frame-" + strconv.Itoa(position)
							scan(body, position+beginEnd+consumed)
						} else if environmentRendersTextBlocks(environment) {
							scan(content, position+beginEnd)
						}
						fragment = fragment[end:]
						continue
					}
				}
			}
			if command, commandEnd := readTeXCommand(fragment, 0); command == "frame" {
				cursor := skipOptionalArguments(fragment, skipOverlay(fragment, commandEnd))
				if content, end, ok := parseBraced(fragment, cursor); ok {
					title, _, body, consumed := beamerFrameHeading(content, false)
					if strings.TrimSpace(title) == "" {
						title = "Slide"
					}
					add(title, 2)
					sections[len(sections)-1].ID = "frame-" + strconv.Itoa(offset+originalLength-len(fragment))
					scan(body, offset+originalLength-len(fragment)+cursor+1+consumed)
					fragment = fragment[end:]
					continue
				}
			}
			end := latexRenderBlockEnd(fragment)
			if end <= 0 {
				_, end = utf8.DecodeRuneInString(fragment)
				if end <= 0 {
					return
				}
			}
			fragment = fragment[end:]
		}
	}
	scan(source, 0)
	return sections
}

func environmentRendersTextBlocks(environment string) bool {
	switch strings.TrimSuffix(environment, "*") {
	case "math", "equation", "displaymath", "align", "alignat", "flalign", "split",
		"gather", "multline", "CD", "verbatim", "lstlisting", "minted",
		"tikzpicture", "flowchart", "tabular", "tabularx", "array", "markdowntable":
		return false
	default:
		return true
	}
}

func prepareLatexPages(source string, byteLimit int) (string, []latexSourcePage) {
	documentType := latexDocumentType(source)
	prepared := preprocessKnownConditionals(stripLatexComments(source))
	prepared, _ = rewriteColorBoxDocuments(prepared, "")
	body := documentBody(prepared)
	if documentType == "flowchart" {
		return prepared, []latexSourcePage{{Source: body}}
	}
	return prepared, paginateLatexBody(body, byteLimit)
}

func latexDocumentType(source string) string {
	if documentStart := strings.Index(source, `\begin{document}`); documentStart >= 0 {
		source = source[:documentStart]
	}
	match := latexDocumentTypePattern.FindStringSubmatch(source)
	if len(match) != 2 {
		return ""
	}
	return strings.ToLower(match[1])
}

// paginateLatexBody groups complete top-level render blocks up to byteLimit.
// A structural block that cannot fit by itself is split into UTF-8-safe source
// pages and is deliberately not sent to MathJax.
func paginateLatexBody(body string, byteLimit int) []latexSourcePage {
	if byteLimit <= 0 {
		byteLimit = defaultRenderPageBytes
	}
	if byteLimit < utf8.UTFMax {
		byteLimit = utf8.UTFMax
	}
	blocks := splitLatexRenderBlocks(body)
	pages := make([]latexSourcePage, 0, len(blocks))
	var current strings.Builder
	flush := func() {
		if current.Len() == 0 {
			return
		}
		pages = append(pages, latexSourcePage{Source: current.String()})
		current.Reset()
	}
	for index, block := range blocks {
		if current.Len() > 0 && startsLatexSection(block) && index+1 < len(blocks) &&
			current.Len()+len(block)+len(blocks[index+1]) > byteLimit {
			flush()
		}
		if len(block) > byteLimit {
			flush()
			for len(block) > 0 {
				end := utf8PageEnd(block, byteLimit)
				pages = append(pages, latexSourcePage{
					Source: block[:end], Oversized: true,
				})
				block = block[end:]
			}
			continue
		}
		if current.Len() > 0 && current.Len()+len(block) > byteLimit {
			flush()
		}
		current.WriteString(block)
	}
	flush()
	if len(pages) == 0 {
		pages = append(pages, latexSourcePage{})
	}
	return pages
}

func startsLatexSection(block string) bool {
	block = strings.TrimLeftFunc(block, unicode.IsSpace)
	if strings.HasPrefix(block, `\attentionwindow`) || strings.HasPrefix(block, `\maketitle`) {
		return true
	}
	_, _, _, ok := parseHeading(block)
	return ok
}

func splitLatexRenderBlocks(source string) []string {
	var blocks []string
	for position := 0; position < len(source); {
		contentStart := skipUnicodeSpace(source, position)
		if contentStart == len(source) {
			if len(blocks) == 0 {
				blocks = append(blocks, source[position:])
			} else {
				blocks[len(blocks)-1] += source[position:]
			}
			break
		}
		end := latexRenderBlockEnd(source[contentStart:])
		if end <= 0 {
			_, end = utf8.DecodeRuneInString(source[contentStart:])
			if end <= 0 {
				end = 1
			}
		}
		end += contentStart
		blocks = append(blocks, source[position:end])
		position = end
	}
	return blocks
}

func latexRenderBlockEnd(source string) int {
	if command, commandEnd := readTeXCommand(source, 0); command == "frame" {
		cursor := skipOptionalArguments(source, skipOverlay(source, commandEnd))
		if _, end, ok := parseBraced(source, cursor); ok {
			return end
		}
	}
	if strings.HasPrefix(source, `\maketitle`) {
		return len(`\maketitle`)
	}
	if strings.HasPrefix(source, `\attentionwindow`) {
		position := skipTeXSpace(source, len(`\attentionwindow`))
		if _, next, ok := parseBraced(source, position); ok {
			next = skipTeXSpace(source, next)
			if _, end, secondOK := parseBraced(source, next); secondOK {
				return end
			}
		}
	}
	if _, _, end, ok := parseHeading(source); ok {
		return end
	}
	if span, ok := parseLatexMathSpan(source, 0); ok && span.display {
		return span.end
	}
	if strings.HasPrefix(source, `\begin{`) && !strings.HasPrefix(source, `\begin{math}`) {
		if environment, beginEnd, ok := parseEnvironmentStart(source); ok {
			if _, end, found := extractEnvironment(source, environment, beginEnd); found {
				return end
			}
		}
	}
	return paragraphBoundary(source)
}

func skipUnicodeSpace(source string, position int) int {
	for position < len(source) {
		character, size := utf8.DecodeRuneInString(source[position:])
		if size <= 0 || !unicode.IsSpace(character) {
			break
		}
		position += size
	}
	return position
}

func utf8PageEnd(source string, byteLimit int) int {
	if len(source) <= byteLimit {
		return len(source)
	}
	end := byteLimit
	for end > 0 && !utf8.RuneStart(source[end]) {
		end--
	}
	if end == 0 {
		_, end = utf8.DecodeRuneInString(source)
	}
	return end
}
