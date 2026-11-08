package latexpdf

import (
	"regexp"
	"strings"
)

type auxiliaryBibliographyEntry struct {
	key, label string
	fields     map[string]string
	authors    []string
}

var entryKeyLabelPattern = regexp.MustCompile(`(?s)\\DeclareFieldFormat\s*\{labelalpha\}\s*\{\s*\\thefield\s*\{entrykey\}\s*\}`)

// SupplementBibliography replaces \printbibliography (or a body-level
// \bibliography command) with bibliography entries from a caller-supplied BBL
// auxiliary. Both traditional \bibitem BBL files and biblatex data BBL files
// are accepted. The source is returned unchanged when the auxiliary is not
// recognizable, preserving the explicit unsupported-command fallback.
func SupplementBibliography(source, auxiliary string) string {
	if strings.Contains(source, `\begin{thebibliography}`) {
		return source
	}
	bibliography, ok := traditionalBBLBibliography(auxiliary)
	if !ok {
		entries := parseBibLaTeXBBL(auxiliary)
		if len(entries) == 0 {
			return source
		}
		bibliography = renderAuxiliaryBibliography(entries, entryKeyLabelPattern.MatchString(source))
	}
	if replaced, ok := replacePrintBibliography(source, bibliography); ok {
		return replaced
	}
	if replaced, ok := replaceBodyBibliographyCommand(source, bibliography); ok {
		return replaced
	}
	return source
}

func traditionalBBLBibliography(source string) (string, bool) {
	start := strings.Index(source, `\begin{thebibliography}`)
	if start < 0 {
		return "", false
	}
	environment, contentStart, ok := parseEnvironmentStart(source[start:])
	if !ok || environment != "thebibliography" {
		return "", false
	}
	_, end, ok := extractEnvironment(source[start:], environment, contentStart)
	if !ok {
		return "", false
	}
	return source[start : start+end], true
}

func replacePrintBibliography(source, bibliography string) (string, bool) {
	const command = `\printbibliography`
	var output strings.Builder
	position, changed := 0, false
	for position < len(source) {
		relative := strings.Index(source[position:], command)
		if relative < 0 {
			output.WriteString(source[position:])
			break
		}
		start := position + relative
		commandEnd := start + len(command)
		if !commandBoundary(source, commandEnd) {
			output.WriteString(source[position:commandEnd])
			position = commandEnd
			continue
		}
		end := skipTeXSpace(source, commandEnd)
		for end < len(source) && source[end] == '[' {
			_, optionalEnd, ok := parseDelimited(source, end, '[', ']')
			if !ok {
				break
			}
			end = skipTeXSpace(source, optionalEnd)
		}
		output.WriteString(source[position:start])
		output.WriteString(bibliography)
		position = end
		changed = true
	}
	return output.String(), changed
}

func replaceBodyBibliographyCommand(source, bibliography string) (string, bool) {
	const command = `\bibliography`
	bodyStart := strings.Index(source, `\begin{document}`)
	if bodyStart < 0 {
		bodyStart = 0
	} else {
		bodyStart += len(`\begin{document}`)
	}
	for searchFrom := bodyStart; searchFrom < len(source); {
		relative := strings.Index(source[searchFrom:], command)
		if relative < 0 {
			break
		}
		start := searchFrom + relative
		commandEnd := start + len(command)
		if !commandBoundary(source, commandEnd) {
			searchFrom = commandEnd
			continue
		}
		cursor := skipTeXSpace(source, commandEnd)
		_, end, ok := parseBraced(source, cursor)
		if !ok {
			searchFrom = commandEnd
			continue
		}
		return source[:start] + bibliography + source[end:], true
	}
	return source, false
}

func parseBibLaTeXBBL(source string) []auxiliaryBibliographyEntry {
	var entries []auxiliaryBibliographyEntry
	for position := 0; position < len(source); {
		relative := strings.Index(source[position:], `\entry`)
		if relative < 0 {
			break
		}
		start := position + relative
		commandEnd := start + len(`\entry`)
		if !commandBoundary(source, commandEnd) {
			position = commandEnd
			continue
		}
		cursor := skipTeXSpace(source, commandEnd)
		key, next, ok := parseBraced(source, cursor)
		if !ok {
			position = commandEnd
			continue
		}
		cursor = skipTeXSpace(source, next)
		for range 3 {
			_, next, ok = parseBraced(source, cursor)
			if !ok {
				break
			}
			cursor = skipTeXSpace(source, next)
		}
		if !ok {
			position = commandEnd
			continue
		}
		relativeEnd := strings.Index(source[cursor:], `\endentry`)
		if relativeEnd < 0 {
			break
		}
		entryEnd := cursor + relativeEnd
		block := source[cursor:entryEnd]
		fields := parseBibLaTeXFields(block)
		entries = append(entries, auxiliaryBibliographyEntry{
			key: strings.TrimSpace(key), label: fields["labelalpha"], fields: fields,
			authors: parseBibLaTeXAuthors(block),
		})
		position = entryEnd + len(`\endentry`)
	}
	return entries
}

func parseBibLaTeXFields(source string) map[string]string {
	fields := make(map[string]string)
	for position := 0; position < len(source); {
		relative := strings.Index(source[position:], `\field`)
		if relative < 0 {
			break
		}
		start := position + relative
		cursor := skipTeXSpace(source, start+len(`\field`))
		name, next, ok := parseBraced(source, cursor)
		if !ok {
			position = cursor
			continue
		}
		cursor = skipTeXSpace(source, next)
		value, end, ok := parseBraced(source, cursor)
		if !ok {
			position = cursor
			continue
		}
		fields[strings.TrimSpace(name)] = cleanBibLaTeXValue(value)
		position = end
	}
	for position := 0; position < len(source); {
		relative := strings.Index(source[position:], `\list`)
		if relative < 0 {
			break
		}
		start := position + relative
		cursor := skipTeXSpace(source, start+len(`\list`))
		name, next, ok := parseBraced(source, cursor)
		if !ok {
			position = cursor
			continue
		}
		cursor = skipTeXSpace(source, next)
		_, next, ok = parseBraced(source, cursor)
		if !ok {
			position = cursor
			continue
		}
		cursor = skipTeXSpace(source, next)
		value, end, ok := parseBraced(source, cursor)
		if !ok {
			position = cursor
			continue
		}
		fields[strings.TrimSpace(name)] = cleanBibLaTeXValue(value)
		position = end
	}
	for position := 0; position < len(source); {
		relative := strings.Index(source[position:], `\verb{`)
		if relative < 0 {
			break
		}
		start := position + relative
		name, next, ok := parseBraced(source, start+len(`\verb`))
		if !ok {
			position = start + len(`\verb`)
			continue
		}
		valueCommand := skipTeXSpace(source, next)
		if !strings.HasPrefix(source[valueCommand:], `\verb`) {
			position = next
			continue
		}
		valueStart := valueCommand + len(`\verb`)
		valueEnd := strings.Index(source[valueStart:], `\endverb`)
		if valueEnd < 0 {
			break
		}
		valueEnd += valueStart
		fields[strings.TrimSpace(name)] = cleanBibLaTeXValue(source[valueStart:valueEnd])
		position = valueEnd + len(`\endverb`)
	}
	return fields
}

func parseBibLaTeXAuthors(source string) []string {
	position := strings.Index(source, `\name{author}`)
	if position < 0 {
		return nil
	}
	cursor := position + len(`\name`)
	for range 3 {
		_, next, ok := parseBraced(source, skipTeXSpace(source, cursor))
		if !ok {
			return nil
		}
		cursor = next
	}
	body, _, ok := parseBraced(source, skipTeXSpace(source, cursor))
	if !ok {
		return nil
	}
	chunks := strings.Split(body, "{{hash=")
	authors := make([]string, 0, len(chunks)-1)
	for _, chunk := range chunks[1:] {
		given := bibLaTeXNamedValue(chunk, "given")
		prefix := bibLaTeXNamedValue(chunk, "prefix")
		family := bibLaTeXNamedValue(chunk, "family")
		suffix := bibLaTeXNamedValue(chunk, "suffix")
		parts := make([]string, 0, 4)
		for _, value := range []string{given, prefix, family, suffix} {
			if value != "" {
				parts = append(parts, value)
			}
		}
		if len(parts) > 0 {
			authors = append(authors, strings.Join(parts, " "))
		}
	}
	return authors
}

func bibLaTeXNamedValue(source, name string) string {
	needle := name + "="
	position := strings.Index(source, needle)
	if position < 0 {
		return ""
	}
	position += len(needle)
	value, _, ok := parseBraced(source, position)
	if !ok {
		return ""
	}
	return cleanBibLaTeXValue(value)
}

func cleanBibLaTeXValue(source string) string {
	source = strings.TrimSpace(stripLatexComments(source))
	for len(source) > 1 && source[0] == '{' {
		inner, end, ok := parseBraced(source, 0)
		if !ok || skipTeXSpace(source, end) != len(source) {
			break
		}
		source = strings.TrimSpace(inner)
	}
	source = strings.NewReplacer(
		`\bibnamedelima`, " ", `\bibnamedelimb`, " ",
		`\bibinitperiod`, ".", `\bibinitdelim`, " ",
		`\bibrangedash`, "--",
	).Replace(source)
	source = collapseWhitespace(source)
	return strings.ReplaceAll(source, "-- ", "--")
}

func renderAuxiliaryBibliography(entries []auxiliaryBibliographyEntry, useEntryKey bool) string {
	var output strings.Builder
	output.WriteString(`\begin{thebibliography}{99}` + "\n")
	for _, entry := range entries {
		if entry.key == "" {
			continue
		}
		label := entry.label
		if useEntryKey || label == "" {
			label = entry.key
		}
		output.WriteString(`\bibitem[` + label + `]{` + entry.key + `}` + "\n")
		output.WriteString(formatAuxiliaryBibliographyEntry(entry))
		output.WriteString("\n\n")
	}
	output.WriteString(`\end{thebibliography}`)
	return output.String()
}

func formatAuxiliaryBibliographyEntry(entry auxiliaryBibliographyEntry) string {
	var parts []string
	if len(entry.authors) > 0 {
		parts = append(parts, strings.Join(entry.authors, ", "))
	}
	if title := entry.fields["title"]; title != "" {
		parts = append(parts, `\emph{`+title+`}`)
	}
	container := entry.fields["journaltitle"]
	if container == "" {
		container = entry.fields["booktitle"]
	}
	if container != "" {
		parts = append(parts, container)
	}
	publication := ""
	if volume := entry.fields["volume"]; volume != "" {
		publication += "vol.~" + volume
	}
	if number := entry.fields["number"]; number != "" {
		if publication != "" {
			publication += ", "
		}
		publication += "no.~" + number
	}
	year := entry.fields["year"]
	if year == "" {
		year = entry.fields["date"]
	}
	if year != "" {
		if publication != "" {
			publication += " "
		}
		publication += "(" + year + ")"
	}
	if pages := entry.fields["pages"]; pages != "" {
		if publication != "" {
			publication += ", "
		}
		publication += "pp.~" + pages
	}
	if publication != "" {
		parts = append(parts, publication)
	}
	for _, name := range []string{"publisher", "location", "series"} {
		if value := entry.fields[name]; value != "" {
			parts = append(parts, value)
		}
	}
	if note := entry.fields["note"]; note != "" {
		parts = append(parts, note)
	}
	if eprint := entry.fields["eprint"]; eprint != "" {
		parts = append(parts, "arXiv:"+eprint)
	}
	url := entry.fields["url"]
	if doi := entry.fields["doi"]; doi != "" && !strings.Contains(url, doi) {
		parts = append(parts, `\url{https://doi.org/`+doi+`}`)
	}
	if url != "" {
		parts = append(parts, `\url{`+url+`}`)
	}
	if len(parts) == 0 {
		return entry.key + "."
	}
	return strings.Join(parts, ". ") + "."
}
