package latexpdf

import (
	"strings"
	"unicode/utf8"
)

// The engine represents the brace itself as an under/overscript. Attach the
// user's labels to that whole node instead of replacing its brace or reporting
// a double script. Bases and labels can themselves contain labelled braces.
func rewriteBraceScripts(source string) string {
	var output strings.Builder
	for position := 0; position < len(source); {
		if source[position] != '\\' {
			output.WriteByte(source[position])
			position++
			continue
		}
		command, commandEnd := readTeXCommand(source, position)
		if commandEnd <= position {
			output.WriteString(source[position:])
			break
		}
		if command != "underbrace" && command != "overbrace" {
			output.WriteString(source[position:commandEnd])
			position = commandEnd
			continue
		}
		base, end, ok := parseBraceMathArgument(source, commandEnd)
		if !ok {
			output.WriteString(source[position:commandEnd])
			position = commandEnd
			continue
		}
		var sub, sup string
		var hasSub, hasSup bool
		for {
			cursor := skipTeXSpace(source, end)
			if cursor >= len(source) || (source[cursor] != '_' && source[cursor] != '^') {
				break
			}
			marker := source[cursor]
			if marker == '_' && hasSub || marker == '^' && hasSup {
				break // Leave duplicate scripts for the engine to diagnose.
			}
			label, labelEnd, ok := parseBraceMathArgument(source, cursor+1)
			if !ok {
				break
			}
			if marker == '_' {
				sub, hasSub = rewriteBraceScripts(label), true
			} else {
				sup, hasSup = rewriteBraceScripts(label), true
			}
			end = labelEnd
		}
		base = `\` + command + `{` + rewriteBraceScripts(base) + `}`
		switch {
		case hasSub && hasSup:
			output.WriteString(`\overunderset{` + sup + `}{` + sub + `}{` + base + `}`)
		case hasSub:
			output.WriteString(`\underset{` + sub + `}{` + base + `}`)
		case hasSup:
			output.WriteString(`\overset{` + sup + `}{` + base + `}`)
		default:
			output.WriteString(base)
		}
		position = end
	}
	return output.String()
}

// Accept a group or a single math token, including common argument-taking
// commands used without an extra group: _\text{label}, _\frac{a}{b}, etc.
func parseBraceMathArgument(source string, start int) (string, int, bool) {
	start = skipTeXSpace(source, start)
	if start >= len(source) {
		return "", start, false
	}
	if source[start] == '{' {
		return parseBraced(source, start)
	}
	if strings.ContainsRune("}_^&$", rune(source[start])) {
		return "", start, false
	}
	if source[start] != '\\' {
		_, size := utf8.DecodeRuneInString(source[start:])
		return source[start : start+size], start + size, true
	}
	command, end := readTeXCommand(source, start)
	if end <= start {
		return "", start, false
	}
	arguments := 0
	if mathCommandTakesArgument(command) {
		arguments = 1
	}
	switch command {
	case "underbrace", "overbrace", "sqrt", "hat", "widehat", "bar", "overline", "underline", "tilde", "widetilde", "vec", "dot", "ddot", "mbox", "substack":
		arguments = 1
	case "frac", "dfrac", "tfrac", "binom", "dbinom", "tbinom", "overset", "underset", "stackrel":
		arguments = 2
	case "overunderset":
		arguments = 3
	}
	if command == "sqrt" {
		cursor := skipTeXSpace(source, end)
		if cursor < len(source) && source[cursor] == '[' {
			_, next, ok := parseDelimited(source, cursor, '[', ']')
			if !ok {
				return "", start, false
			}
			end = next
		}
	}
	for range arguments {
		_, next, ok := parseBraceMathArgument(source, end)
		if !ok {
			return "", start, false
		}
		end = next
	}
	return source[start:end], end, true
}
