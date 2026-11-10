package latexpdf

import (
	"fmt"
	"html/template"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

type latexTheoremDeclaration struct {
	Environment string
	Title       string
	Counter     string
	Within      string
	Numbered    bool
}

type latexNumberedOccurrence struct {
	Offset         int
	Environment    string
	Kind           string
	Number         string
	DisplayNumber  string
	DisplayNumbers []string
	DisplayTags    []string
	Title          string
	Option         string
	Anchor         string
	Label          string
	Level          int
}

type latexReference struct {
	Kind   string
	Number string
	Anchor string
}

type latexBibliographyEntry struct {
	Key    string
	Label  string
	Anchor string
}

type latexBibliographyItem struct {
	Key     string
	Label   string
	Content string
	Offset  int
}

type latexDocumentIndex struct {
	theorems           map[string]latexTheoremDeclaration
	occurrences        map[int]latexNumberedOccurrence
	occurrencesByLabel map[string]latexNumberedOccurrence
	references         map[string]latexReference
	bibliography       map[string]latexBibliographyEntry
	tableOfContents    []latexNumberedOccurrence
	counterParents     map[string]string
	counterFormats     map[string]string
	body               string
}

func buildLatexDocumentIndex(prepared string) *latexDocumentIndex {
	index := &latexDocumentIndex{
		theorems:           make(map[string]latexTheoremDeclaration),
		occurrences:        make(map[int]latexNumberedOccurrence),
		occurrencesByLabel: make(map[string]latexNumberedOccurrence),
		references:         make(map[string]latexReference),
		bibliography:       make(map[string]latexBibliographyEntry),
		counterParents:     make(map[string]string),
		counterFormats:     make(map[string]string),
		body:               documentBody(prepared),
	}
	// Included setup fragments are often loaded immediately after
	// \begin{document}; declarations still have document-wide semantics.
	index.theorems = parseTheoremDeclarations(prepared)
	index.counterParents = parseCounterParents(prepared)
	index.counterFormats = parseCounterFormats(prepared)
	index.counterParents["subsection"] = "section"
	index.counterParents["subsubsection"] = "subsection"
	index.counterParents["paragraph"] = "subsubsection"
	for _, item := range splitBibliographyItems(index.body) {
		index.bibliography[item.Key] = latexBibliographyEntry{
			Key: item.Key, Label: item.Label, Anchor: latexBibliographyAnchor(item.Key),
		}
	}
	for _, declaration := range index.theorems {
		if declaration.Within != "" {
			index.counterParents[declaration.Counter] = declaration.Within
		}
	}
	index.scanBody(
		initialCounterValue(prepared, "secnumdepth", 3),
		initialCounterValue(prepared, "tocdepth", 3),
	)
	return index
}

func splitBibliographyItems(source string) []latexBibliographyItem {
	type marker struct {
		start, content int
		key, label     string
	}
	var markers []marker
	for position := 0; position < len(source); {
		next := strings.Index(source[position:], `\bibitem`)
		if next < 0 {
			break
		}
		next += position
		cursor := next + len(`\bibitem`)
		if !commandBoundary(source, cursor) {
			position = cursor
			continue
		}
		cursor = skipTeXSpace(source, cursor)
		label := ""
		if cursor < len(source) && source[cursor] == '[' {
			value, end, ok := parseDelimited(source, cursor, '[', ']')
			if !ok {
				position = cursor + 1
				continue
			}
			label = strings.TrimSpace(value)
			cursor = skipTeXSpace(source, end)
		}
		key, end, ok := parseBraced(source, cursor)
		if !ok || strings.TrimSpace(key) == "" {
			position = cursor + 1
			continue
		}
		markers = append(markers, marker{
			start: next, content: end, key: strings.TrimSpace(key), label: label,
		})
		position = end
	}
	items := make([]latexBibliographyItem, 0, len(markers))
	for index, marker := range markers {
		end := len(source)
		if index+1 < len(markers) {
			end = markers[index+1].start
		}
		raw := source[marker.content:end]
		trimmedLeft := strings.TrimLeftFunc(raw, unicode.IsSpace)
		label := marker.label
		if label == "" {
			label = strconv.Itoa(index + 1)
		}
		items = append(items, latexBibliographyItem{
			Key: marker.key, Label: label, Content: strings.TrimSpace(raw),
			Offset: marker.content + len(raw) - len(trimmedLeft),
		})
	}
	return items
}

func latexBibliographyAnchor(key string) string {
	return "bib-" + strings.TrimPrefix(latexReferenceAnchor(key), "ref-")
}

func parseTheoremDeclarations(preamble string) map[string]latexTheoremDeclaration {
	declarations := make(map[string]latexTheoremDeclaration)
	for position := 0; position < len(preamble); {
		next := strings.Index(preamble[position:], `\newtheorem`)
		if next < 0 {
			break
		}
		next += position
		cursor := next + len(`\newtheorem`)
		if !commandBoundary(preamble, cursor) && (cursor >= len(preamble) || preamble[cursor] != '*') {
			position = cursor
			continue
		}
		numbered := true
		if cursor < len(preamble) && preamble[cursor] == '*' {
			numbered = false
			cursor++
		}
		cursor = skipTeXSpace(preamble, cursor)
		environment, end, ok := parseBraced(preamble, cursor)
		if !ok || environment == "" {
			position = cursor + 1
			continue
		}
		cursor = skipTeXSpace(preamble, end)
		shared := ""
		if cursor < len(preamble) && preamble[cursor] == '[' {
			if value, optionalEnd, optionalOK := parseDelimited(preamble, cursor, '[', ']'); optionalOK {
				shared = strings.TrimSpace(value)
				cursor = skipTeXSpace(preamble, optionalEnd)
			}
		}
		title, titleEnd, titleOK := parseBraced(preamble, cursor)
		if !titleOK {
			position = cursor + 1
			continue
		}
		cursor = skipTeXSpace(preamble, titleEnd)
		within := ""
		if cursor < len(preamble) && preamble[cursor] == '[' {
			if value, optionalEnd, optionalOK := parseDelimited(preamble, cursor, '[', ']'); optionalOK {
				within = strings.TrimSpace(value)
				cursor = optionalEnd
			}
		}
		counter := strings.TrimSpace(environment)
		if shared != "" {
			counter = shared
		}
		declarations[environment] = latexTheoremDeclaration{
			Environment: environment,
			Title:       collapseWhitespace(texFragmentToPlainText(title)),
			Counter:     counter,
			Within:      within,
			Numbered:    numbered,
		}
		position = cursor
	}
	for environment, declaration := range declarations {
		if declaration.Within == "" && declaration.Counter != environment {
			if shared, ok := declarations[declaration.Counter]; ok {
				declaration.Within = shared.Within
				declarations[environment] = declaration
			}
		}
	}
	return declarations
}

func parseCounterParents(source string) map[string]string {
	parents := make(map[string]string)
	for position := 0; position < len(source); {
		numberPosition := strings.Index(source[position:], `\numberwithin`)
		counterPosition := strings.Index(source[position:], `\counterwithin`)
		relative := numberPosition
		command := `\numberwithin`
		if relative < 0 || counterPosition >= 0 && counterPosition < relative {
			relative = counterPosition
			command = `\counterwithin`
		}
		if relative < 0 {
			break
		}
		cursor := position + relative + len(command)
		if cursor < len(source) && source[cursor] == '*' {
			cursor++
		}
		cursor = skipTeXSpace(source, cursor)
		counter, end, ok := parseBraced(source, cursor)
		if ok {
			cursor = skipTeXSpace(source, end)
			within, withinEnd, withinOK := parseBraced(source, cursor)
			if withinOK {
				counter = strings.TrimSpace(counter)
				within = strings.TrimSpace(within)
				if counter != "" && within != "" {
					parents[counter] = within
				}
				cursor = withinEnd
			}
		}
		position = cursor + 1
	}
	return parents
}

func parseCounterFormats(source string) map[string]string {
	formats := make(map[string]string)
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
		macroSource, end, ok := parseBraced(source, cursor)
		if !ok {
			position = cursor + 1
			continue
		}
		macroSource = strings.TrimSpace(macroSource)
		if !strings.HasPrefix(macroSource, `\the`) {
			position = end
			continue
		}
		name, nameEnd := readTeXCommand(macroSource, 0)
		if nameEnd != len(macroSource) || !strings.HasPrefix(name, "the") || len(name) == len("the") {
			position = end
			continue
		}
		cursor = skipTeXSpace(source, end)
		for cursor < len(source) && source[cursor] == '[' {
			_, optionalEnd, optionalOK := parseDelimited(source, cursor, '[', ']')
			if !optionalOK {
				break
			}
			cursor = skipTeXSpace(source, optionalEnd)
		}
		replacement, replacementEnd, replacementOK := parseBraced(source, cursor)
		if replacementOK {
			formats[strings.TrimPrefix(name, "the")] = replacement
			position = replacementEnd
		} else {
			position = cursor + 1
		}
	}
	return formats
}

func (index *latexDocumentIndex) scanBody(secnumdepth, tocdepth int) {
	counters := make(map[string]int)
	statementSerial, equationSerial, headingSerial := 0, 0, 0
	appendix := false
	for position := 0; position < len(index.body); {
		if index.body[position] != '\\' {
			position++
			continue
		}
		command, commandEnd := readTeXCommand(index.body, position)
		if commandEnd <= position {
			position++
			continue
		}
		if command == "appendix" {
			appendix = true
			counters["section"] = 0
			index.resetCounterDescendants("section", counters)
			position = commandEnd
			continue
		}
		if isSectioningCommand(command) {
			headingSource, end, starred, ok := parseHeadingArguments(index.body, commandEnd)
			if ok {
				headingTitle := headingSource
				if optionalTitle, optional := optionalHeadingArgument(index.body, commandEnd); optional && strings.TrimSpace(optionalTitle) != "" {
					headingTitle = optionalTitle
				}
				headingSerial++
				number := ""
				if !starred && sectioningLevel(command) <= secnumdepth {
					counters[command]++
					index.resetCounterDescendants(command, counters)
					number = sectionNumber(command, counters, appendix)
				}
				labels := labelsImmediatelyAfter(index.body, end)
				label := ""
				if len(labels) > 0 {
					label = labels[0]
				}
				anchor := "section-" + strconv.Itoa(headingSerial)
				occurrence := latexNumberedOccurrence{
					Offset: position, Environment: command, Kind: "section", Number: number,
					DisplayNumber: number, Title: headingTitle, Anchor: anchor, Label: label,
					Level: sectioningLevel(command),
				}
				index.addOccurrence(occurrence)
				if number != "" && occurrence.Level <= tocdepth {
					index.tableOfContents = append(index.tableOfContents, occurrence)
				}
				if number != "" {
					for _, label := range labels {
						index.references[label] = latexReference{Kind: "section", Number: number, Anchor: anchor}
						index.occurrencesByLabel[label] = occurrence
					}
				}
				position = end
				continue
			}
		}
		if command == "attentionwindow" {
			headingSerial++
			index.addOccurrence(latexNumberedOccurrence{
				Offset: position, Environment: command, Kind: "section",
				Anchor: "section-" + strconv.Itoa(headingSerial),
			})
			position = commandEnd
			continue
		}
		if command == "setcounter" || command == "addtocounter" {
			if counter, value, end, ok := parseCounterMutation(index.body, commandEnd); ok {
				if counter == "secnumdepth" {
					if command == "setcounter" {
						secnumdepth = value
					} else {
						secnumdepth += value
					}
					position = end
					continue
				}
				if counter == "tocdepth" {
					if command == "setcounter" {
						tocdepth = value
					} else {
						tocdepth += value
					}
					position = end
					continue
				}
				if command == "setcounter" {
					counters[counter] = value
				} else {
					counters[counter] += value
				}
				position = end
				continue
			}
		}
		if command != "begin" {
			position = commandEnd
			continue
		}
		environment, contentStart, ok := parseEnvironmentStart(index.body[position:])
		if !ok {
			position = commandEnd
			continue
		}
		contentStart += position
		content, environmentEnd, found := extractEnvironment(index.body[position:], environment, contentStart-position)
		if !found {
			position = commandEnd
			continue
		}
		environmentEnd += position
		base := strings.TrimSuffix(environment, "*")
		if declaration, declared := index.theorems[base]; declared && declaration.Numbered && !strings.HasSuffix(environment, "*") {
			counter := declaration.Counter
			counters[counter]++
			number := index.counterNumber(counter, counters, appendix)
			statementSerial++
			labels := environmentLabels(content, index.theorems)
			label := ""
			if len(labels) > 0 {
				label = labels[0]
			}
			anchor := "statement-" + strconv.Itoa(statementSerial)
			if label != "" {
				anchor = latexReferenceAnchor(label)
			}
			option := environmentOptions(index.body[position:], environment, contentStart-position)
			occurrence := latexNumberedOccurrence{
				Offset: position, Environment: base, Kind: "statement", Number: number,
				DisplayNumber: number, Title: declaration.Title, Option: option,
				Anchor: anchor, Label: label,
			}
			index.addOccurrence(occurrence)
			for _, label := range labels {
				index.references[label] = latexReference{Kind: "statement", Number: number, Anchor: anchor}
				index.occurrencesByLabel[label] = occurrence
			}
		}
		if base == "enumerate" {
			options := environmentOptions(index.body[position:], environment, contentStart-position)
			index.indexEnumerateItems(content, contentStart, 1, "", options)
		}
		if isNumberedEquationEnvironment(environment) {
			rows := numberedEquationRows(base, content)
			var occurrence latexNumberedOccurrence
			equationSerial++
			occurrence = latexNumberedOccurrence{
				Offset: position, Environment: base, Kind: "equation",
				Anchor: "equation-" + strconv.Itoa(equationSerial),
			}
			for _, row := range rows {
				if row.numbered {
					number := ""
					if row.tag == "" {
						counters["equation"]++
						number = index.counterNumber("equation", counters, appendix)
					}
					referenceNumber, displayTag := equationRowNumber(row, number)
					occurrence.Number = referenceNumber
					occurrence.DisplayNumber = referenceNumber
					occurrence.DisplayNumbers = append(occurrence.DisplayNumbers, referenceNumber)
					occurrence.DisplayTags = append(occurrence.DisplayTags, displayTag)
					for _, label := range findLatexLabels(row.source) {
						if occurrence.Label == "" {
							occurrence.Label = label
							occurrence.Anchor = latexReferenceAnchor(label)
						}
						index.references[label] = latexReference{Kind: "equation", Number: referenceNumber, Anchor: occurrence.Anchor}
					}
				}
			}
			index.addOccurrence(occurrence)
			position = environmentEnd
			continue
		}
		// Theorem bodies can contain numbered equations, so continue scanning
		// from their content rather than jumping to the closing environment.
		position = contentStart
	}
}

func (index *latexDocumentIndex) counterWithin(counter string) string {
	if parent := index.counterParents[counter]; parent != "" {
		return parent
	}
	if declaration, ok := index.theorems[counter]; ok {
		return declaration.Within
	}
	for _, declaration := range index.theorems {
		if declaration.Counter == counter && declaration.Within != "" {
			return declaration.Within
		}
	}
	return ""
}

func (index *latexDocumentIndex) counterNumber(counter string, counters map[string]int, appendix bool) string {
	if format := index.counterFormats[counter]; format != "" {
		if formatted := index.expandCounterFormat(format, counters, appendix, make(map[string]bool)); formatted != "" {
			return formatted
		}
	}
	parts := []string{formattedCounter(counter, counters[counter], appendix)}
	seen := map[string]bool{counter: true}
	for parent := index.counterWithin(counter); parent != "" && !seen[parent]; parent = index.counterWithin(parent) {
		seen[parent] = true
		parts = append(parts, formattedCounter(parent, counters[parent], appendix))
	}
	for left, right := 0, len(parts)-1; left < right; left, right = left+1, right-1 {
		parts[left], parts[right] = parts[right], parts[left]
	}
	return strings.Join(parts, ".")
}

func (index *latexDocumentIndex) expandCounterFormat(format string, counters map[string]int, appendix bool, seen map[string]bool) string {
	var output strings.Builder
	for position := 0; position < len(format); {
		if format[position] != '\\' {
			_, size := utf8.DecodeRuneInString(format[position:])
			if size <= 0 {
				break
			}
			output.WriteString(format[position : position+size])
			position += size
			continue
		}
		command, commandEnd := readTeXCommand(format, position)
		if commandEnd <= position {
			position++
			continue
		}
		if strings.HasPrefix(command, "the") && len(command) > len("the") {
			counter := strings.TrimPrefix(command, "the")
			if !seen[counter] {
				seen[counter] = true
				if nested := index.counterFormats[counter]; nested != "" {
					output.WriteString(index.expandCounterFormat(nested, counters, appendix, seen))
				} else {
					output.WriteString(formattedCounter(counter, counters[counter], appendix))
				}
				delete(seen, counter)
			}
			position = commandEnd
			continue
		}
		cursor := skipTeXSpace(format, commandEnd)
		counter, end, ok := parseBraced(format, cursor)
		if ok {
			counter = strings.TrimSpace(counter)
			value := counters[counter]
			switch command {
			case "arabic":
				output.WriteString(strconv.Itoa(value))
			case "alph":
				output.WriteString(alphabeticCounter(value, false))
			case "Alph":
				output.WriteString(alphabeticCounter(value, true))
			case "roman":
				output.WriteString(strings.ToLower(romanNumeral(value)))
			case "Roman":
				output.WriteString(romanNumeral(value))
			default:
				output.WriteString(collapseWhitespace(texFragmentToPlainText(format[position:end])))
			}
			position = end
			continue
		}
		position = commandEnd
	}
	return collapseWhitespace(output.String())
}

func (index *latexDocumentIndex) resetCounterDescendants(parent string, counters map[string]int) {
	for counter := range counters {
		if counter != parent && index.counterDescendsFrom(counter, parent) {
			counters[counter] = 0
		}
	}
}

func (index *latexDocumentIndex) counterDescendsFrom(counter, ancestor string) bool {
	seen := make(map[string]bool)
	for counter != "" && !seen[counter] {
		if counter == ancestor {
			return true
		}
		seen[counter] = true
		counter = index.counterWithin(counter)
	}
	return false
}

func isSectioningCommand(command string) bool {
	switch command {
	case "section", "subsection", "subsubsection", "paragraph":
		return true
	default:
		return false
	}
}

func sectioningLevel(command string) int {
	switch command {
	case "section":
		return 1
	case "subsection":
		return 2
	case "subsubsection":
		return 3
	case "paragraph":
		return 4
	default:
		return 100
	}
}

func initialCounterValue(source, counter string, fallback int) int {
	if documentStart := strings.Index(source, `\begin{document}`); documentStart >= 0 {
		source = source[:documentStart]
	}
	value := fallback
	for position := 0; position < len(source); {
		next := strings.Index(source[position:], `\setcounter`)
		if next < 0 {
			break
		}
		next += position
		if name, candidate, end, ok := parseCounterMutation(source, next+len(`\setcounter`)); ok {
			if name == counter {
				value = candidate
			}
			position = end
		} else {
			position = next + len(`\setcounter`)
		}
	}
	return value
}

func sectionNumber(command string, counters map[string]int, appendix bool) string {
	names := []string{"section", "subsection", "subsubsection", "paragraph"}
	last := 0
	for index, name := range names {
		if name == command {
			last = index
			break
		}
	}
	parts := make([]string, 0, last+1)
	for _, name := range names[:last+1] {
		parts = append(parts, formattedCounter(name, counters[name], appendix))
	}
	return strings.Join(parts, ".")
}

func formattedCounter(counter string, value int, appendix bool) string {
	if appendix && counter == "section" {
		return alphabeticCounter(value, true)
	}
	return strconv.Itoa(value)
}

func alphabeticCounter(value int, upper bool) string {
	if value <= 0 {
		return strconv.Itoa(value)
	}
	var output []byte
	for value > 0 {
		value--
		letter := byte('a' + value%26)
		if upper {
			letter = byte('A' + value%26)
		}
		output = append(output, letter)
		value /= 26
	}
	for left, right := 0, len(output)-1; left < right; left, right = left+1, right-1 {
		output[left], output[right] = output[right], output[left]
	}
	return string(output)
}

func labelsImmediatelyAfter(source string, position int) []string {
	var labels []string
	for {
		position = skipTeXSpace(source, position)
		if !strings.HasPrefix(source[position:], `\label`) {
			return labels
		}
		cursor := position + len(`\label`)
		if !commandBoundary(source, cursor) {
			return labels
		}
		cursor = skipTeXSpace(source, cursor)
		label, end, ok := parseBraced(source, cursor)
		if !ok {
			return labels
		}
		if label = strings.TrimSpace(label); label != "" {
			labels = append(labels, label)
		}
		position = end
	}
}

func parseCounterMutation(source string, commandEnd int) (counter string, value, end int, ok bool) {
	cursor := skipTeXSpace(source, commandEnd)
	counter, cursor, ok = parseBraced(source, cursor)
	if !ok {
		return "", 0, commandEnd, false
	}
	cursor = skipTeXSpace(source, cursor)
	valueSource, end, ok := parseBraced(source, cursor)
	if !ok {
		return "", 0, commandEnd, false
	}
	value, err := strconv.Atoi(strings.TrimSpace(valueSource))
	if err != nil {
		return "", 0, commandEnd, false
	}
	return strings.TrimSpace(counter), value, end, true
}

func (index *latexDocumentIndex) indexEnumerateItems(content string, contentOffset, depth int, prefix, options string) {
	items := splitListItems(content)
	for itemIndex, item := range items {
		_, number := enumerateItemNumber(itemIndex+1, depth, prefix, options)
		label := leadingItemLabel(item.Content)
		if label == "" {
			continue
		}
		anchor := latexReferenceAnchor(label)
		occurrence := latexNumberedOccurrence{
			Offset: contentOffset + item.Offset, Environment: "item", Kind: "item",
			Number: number, DisplayNumber: number, Anchor: anchor, Label: label,
		}
		index.addOccurrence(occurrence)
		index.references[label] = latexReference{Kind: "item", Number: number, Anchor: anchor}
	}
}

func enumerateReferenceNumber(index, depth int, prefix string) string {
	part := strconv.Itoa(index)
	switch depth {
	case 2:
		part = alphabeticCounter(index, false)
	case 3:
		part = strings.ToLower(romanNumeral(index))
	}
	return prefix + part
}

func enumerateItemNumber(index, depth int, prefix, options string) (display, reference string) {
	template := enumerateLabelTemplate(options)
	if template == "" {
		reference = enumerateReferenceNumber(index, depth, prefix)
		return "", reference
	}
	display, ok := expandEnumerateTemplate(template, index)
	if !ok {
		return "", enumerateReferenceNumber(index, depth, prefix)
	}
	referenceTemplate := enumerateOption(options, "ref")
	if referenceTemplate == "" {
		referenceTemplate = template
	}
	reference, ok = expandEnumerateTemplate(referenceTemplate, index)
	if !ok || reference == "" {
		reference = display
	}
	return display, reference
}

func expandEnumerateTemplate(template string, index int) (string, bool) {
	value := strconv.Itoa(index)
	switch {
	case strings.Contains(template, `\alph*`):
		value = alphabeticCounter(index, false)
		template = strings.ReplaceAll(template, `\alph*`, value)
	case strings.Contains(template, `\Alph*`):
		value = alphabeticCounter(index, true)
		template = strings.ReplaceAll(template, `\Alph*`, value)
	case strings.Contains(template, `\roman*`):
		value = strings.ToLower(romanNumeral(index))
		template = strings.ReplaceAll(template, `\roman*`, value)
	case strings.Contains(template, `\Roman*`):
		value = romanNumeral(index)
		template = strings.ReplaceAll(template, `\Roman*`, value)
	case strings.Contains(template, `\arabic*`):
		template = strings.ReplaceAll(template, `\arabic*`, value)
	default:
		return "", false
	}
	expanded := collapseWhitespace(texFragmentToPlainText(template))
	if expanded == "" {
		expanded = template
	}
	return expanded, true
}

func enumerateLabelTemplate(options string) string {
	return enumerateOption(options, "label")
}

func enumerateOption(options, wanted string) string {
	for _, option := range strings.Split(options, ",") {
		name, value, ok := strings.Cut(option, "=")
		if ok && strings.TrimSpace(name) == wanted {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func romanNumeral(value int) string {
	if value <= 0 {
		return strconv.Itoa(value)
	}
	type numeral struct {
		value int
		text  string
	}
	numerals := []numeral{{1000, "M"}, {900, "CM"}, {500, "D"}, {400, "CD"}, {100, "C"}, {90, "XC"}, {50, "L"}, {40, "XL"}, {10, "X"}, {9, "IX"}, {5, "V"}, {4, "IV"}, {1, "I"}}
	var output strings.Builder
	for _, numeral := range numerals {
		for value >= numeral.value {
			output.WriteString(numeral.text)
			value -= numeral.value
		}
	}
	return output.String()
}

func leadingItemLabel(source string) string {
	position := skipTeXSpace(source, 0)
	if !strings.HasPrefix(source[position:], `\label`) {
		return ""
	}
	cursor := skipTeXSpace(source, position+len(`\label`))
	label, _, ok := parseBraced(source, cursor)
	if !ok {
		return ""
	}
	return strings.TrimSpace(label)
}

func (index *latexDocumentIndex) addOccurrence(occurrence latexNumberedOccurrence) {
	index.occurrences[occurrence.Offset] = occurrence
	if occurrence.Label != "" {
		index.occurrencesByLabel[occurrence.Label] = occurrence
	}
}

func (index *latexDocumentIndex) occurrence(offset int, content string) (latexNumberedOccurrence, bool) {
	if occurrence, ok := index.occurrences[offset]; ok {
		return occurrence, true
	}
	for _, label := range findLatexLabels(content) {
		if occurrence, ok := index.occurrencesByLabel[label]; ok {
			return occurrence, true
		}
	}
	return latexNumberedOccurrence{}, false
}

type latexEquationRow struct {
	source   string
	numbered bool
	tag      string
	tagStar  bool
}

func numberedEquationRows(environment, content string) []latexEquationRow {
	if environment == "equation" || environment == "multline" {
		tag, tagStar := equationTag(content)
		return []latexEquationRow{{source: content, numbered: true, tag: tag, tagStar: tagStar}}
	}
	parts := splitTopLevelTeXRows(content)
	rows := make([]latexEquationRow, 0, len(parts))
	for _, part := range parts {
		trimmed := strings.TrimSpace(part)
		if trimmed == "" {
			continue
		}
		numbered := !strings.Contains(trimmed, `\notag`) && !strings.Contains(trimmed, `\nonumber`)
		tag, tagStar := equationTag(trimmed)
		rows = append(rows, latexEquationRow{source: trimmed, numbered: numbered, tag: tag, tagStar: tagStar})
	}
	if len(rows) == 0 {
		tag, tagStar := equationTag(content)
		rows = append(rows, latexEquationRow{source: content, numbered: true, tag: tag, tagStar: tagStar})
	}
	return rows
}

func equationTag(source string) (string, bool) {
	for position := 0; position < len(source); {
		next := strings.Index(source[position:], `\tag`)
		if next < 0 {
			return "", false
		}
		next += position
		cursor := next + len(`\tag`)
		if !commandBoundary(source, cursor) && (cursor >= len(source) || source[cursor] != '*') {
			position = cursor
			continue
		}
		starred := cursor < len(source) && source[cursor] == '*'
		if starred {
			cursor++
		}
		cursor = skipTeXSpace(source, cursor)
		value, _, ok := parseBraced(source, cursor)
		if ok {
			return strings.TrimSpace(value), starred
		}
		position = cursor + 1
	}
	return "", false
}

func equationRowNumber(row latexEquationRow, automatic string) (reference, display string) {
	if row.tag == "" {
		return automatic, "(" + automatic + ")"
	}
	reference = collapseWhitespace(texFragmentToPlainText(row.tag))
	if reference == "" {
		reference = strings.TrimSpace(row.tag)
	}
	if row.tagStar {
		return reference, reference
	}
	return reference, "(" + reference + ")"
}

func splitTopLevelTeXRows(source string) []string {
	var rows []string
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
		if source[position] == '{' && !isEscaped(source, position) {
			braceDepth++
		} else if source[position] == '}' && !isEscaped(source, position) && braceDepth > 0 {
			braceDepth--
		} else if braceDepth == 0 && environmentDepth == 0 && strings.HasPrefix(source[position:], `\\`) {
			rows = append(rows, source[start:position])
			position += 2
			start = position
			continue
		}
		position++
	}
	rows = append(rows, source[start:])
	return rows
}

func isNumberedEquationEnvironment(environment string) bool {
	if strings.HasSuffix(environment, "*") {
		return false
	}
	switch environment {
	case "equation", "align", "alignat", "flalign", "gather", "multline", "eqnarray":
		return true
	default:
		return false
	}
}

func environmentLabels(content string, theorems map[string]latexTheoremDeclaration) []string {
	cutoff := len(content)
	for environment := range theorems {
		if nested := strings.Index(content, `\begin{`+environment+`}`); nested >= 0 && nested < cutoff {
			cutoff = nested
		}
	}
	for _, environment := range []string{"equation", "align", "alignat", "flalign", "gather", "multline", "eqnarray", "enumerate"} {
		if nested := strings.Index(content, `\begin{`+environment+`}`); nested >= 0 && nested < cutoff {
			cutoff = nested
		}
	}
	return findLatexLabels(content[:cutoff])
}

func findLatexLabels(source string) []string {
	var labels []string
	for position := 0; position < len(source); {
		next := strings.Index(source[position:], `\label`)
		if next < 0 {
			break
		}
		next += position
		cursor := next + len(`\label`)
		if !commandBoundary(source, cursor) {
			position = cursor
			continue
		}
		cursor = skipTeXSpace(source, cursor)
		label, end, ok := parseBraced(source, cursor)
		if ok && strings.TrimSpace(label) != "" {
			labels = append(labels, strings.TrimSpace(label))
			position = end
		} else {
			position = cursor + 1
		}
	}
	return labels
}

func latexReferenceAnchor(label string) string {
	var output strings.Builder
	output.WriteString("ref-")
	for _, r := range label {
		if unicode.IsLetter(r) || unicode.IsDigit(r) || r == '-' || r == '_' {
			output.WriteRune(r)
		} else {
			output.WriteByte('-')
		}
	}
	if output.Len() == len("ref-") {
		return "ref-label"
	}
	return output.String()
}

func renderResolvedReference(reference latexReference, equation bool) string {
	text := reference.Number
	if equation {
		text = "(" + text + ")"
	}
	return `<a class="reference" href="#` + template.HTMLEscapeString(reference.Anchor) + `">` + template.HTMLEscapeString(text) + `</a>`
}

func formatOccurrenceTitle(occurrence latexNumberedOccurrence) string {
	title := strings.TrimSpace(occurrence.Title)
	if title == "" {
		title = titleCase(occurrence.Environment)
	}
	if occurrence.Number != "" {
		title += " " + occurrence.Number
	}
	return title
}

func (index *latexDocumentIndex) String() string {
	return fmt.Sprintf("%d theorem declarations, %d numbered occurrences, %d references", len(index.theorems), len(index.occurrences), len(index.references))
}
