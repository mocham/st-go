package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"unicode"

	"github.com/mocham/GoLS/latexpdf"
)

const maxTeXIncludeDepth = 32

type localTeXExpander struct {
	active   map[string]bool
	maxBytes int
}

func expandLocalTeXDocument(filename string, source []byte, maxBytes int) ([]byte, error) {
	absolute, err := filepath.Abs(filename)
	if err != nil {
		return nil, err
	}
	expander := localTeXExpander{active: make(map[string]bool), maxBytes: maxBytes}
	expanded, err := expander.expand(absolute, string(source), 0)
	if err != nil {
		return nil, err
	}
	expanded, err = supplementLocalTableMacros(absolute, expanded)
	if err != nil {
		return nil, err
	}
	expanded, err = supplementLocalBibliography(absolute, expanded)
	if err != nil {
		return nil, err
	}
	if maxBytes > 0 && len(expanded) > maxBytes {
		return nil, fmt.Errorf("expanded TeX document exceeds the %d MiB preview limit", maxBytes>>20)
	}
	return []byte(expanded), nil
}

func supplementLocalBibliography(filename, source string) (string, error) {
	if !strings.Contains(source, `\printbibliography`) && !strings.Contains(source, `\bibliography`) {
		return source, nil
	}
	candidates := []string{strings.TrimSuffix(filename, filepath.Ext(filename)) + ".bbl"}
	for _, name := range localTeXBibliographyNames(source) {
		if name == `\jobname` {
			name = strings.TrimSuffix(filepath.Base(filename), filepath.Ext(filename))
		}
		path := filepath.Join(filepath.Dir(filename), filepath.FromSlash(name))
		extension := filepath.Ext(path)
		if strings.EqualFold(extension, ".bib") || strings.EqualFold(extension, ".aux") {
			path = strings.TrimSuffix(path, extension) + ".bbl"
		} else if !strings.EqualFold(extension, ".bbl") {
			path += ".bbl"
		}
		candidates = append(candidates, path)
	}
	auxiliaryPath := strings.TrimSuffix(filename, filepath.Ext(filename)) + ".aux"
	if auxiliary, err := os.ReadFile(auxiliaryPath); err == nil {
		for _, name := range localTeXCommandArguments(string(auxiliary), `\bibdata`) {
			for _, item := range strings.Split(name, ",") {
				item = strings.TrimSpace(item)
				if item == "" || strings.HasSuffix(item, "-blx") {
					continue
				}
				candidates = append(candidates, filepath.Join(filepath.Dir(filename), filepath.FromSlash(item)+".bbl"))
			}
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", fmt.Errorf("read TeX auxiliary %s: %w", auxiliaryPath, err)
	}

	seen := make(map[string]bool)
	for _, candidate := range candidates {
		candidate = filepath.Clean(candidate)
		if seen[candidate] {
			continue
		}
		seen[candidate] = true
		data, err := os.ReadFile(candidate)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return "", fmt.Errorf("read TeX bibliography auxiliary %s: %w", candidate, err)
		}
		if supplemented := latexpdf.SupplementBibliography(source, string(data)); supplemented != source {
			return supplemented, nil
		}
	}
	return source, nil
}

func localTeXBibliographyNames(source string) []string {
	var names []string
	for _, command := range []string{`\bibliography`, `\addbibresource`} {
		for _, argument := range localTeXCommandArguments(source, command) {
			for _, name := range strings.Split(argument, ",") {
				if name = strings.TrimSpace(name); name != "" {
					names = append(names, name)
				}
			}
		}
	}
	return names
}

func localTeXCommandArguments(source, command string) []string {
	var arguments []string
	for position := 0; position < len(source); {
		relative := strings.Index(source[position:], command)
		if relative < 0 {
			break
		}
		start := position + relative
		commandEnd := start + len(command)
		if !latexpdf.CommandBoundary(source, commandEnd) || texCharacterEscaped(source, start) {
			position = commandEnd
			continue
		}
		cursor := latexpdf.SkipSpace(source, commandEnd)
		for cursor < len(source) && source[cursor] == '[' {
			end := strings.IndexByte(source[cursor+1:], ']')
			if end < 0 {
				break
			}
			cursor = latexpdf.SkipSpace(source, cursor+end+2)
		}
		argument, end, ok := latexpdf.ParseBraced(source, cursor)
		if !ok {
			position = commandEnd
			continue
		}
		arguments = append(arguments, strings.TrimSpace(argument))
		position = end
	}
	return arguments
}

func supplementLocalTableMacros(filename, source string) (string, error) {
	extension := filepath.Ext(filename)
	if !strings.EqualFold(extension, ".tex") || strings.HasSuffix(strings.TrimSuffix(filename, extension), "-tables") {
		return source, nil
	}
	companion := strings.TrimSuffix(filename, extension) + "-tables" + extension
	definitions, err := os.ReadFile(companion)
	if errors.Is(err, os.ErrNotExist) {
		return source, nil
	}
	if err != nil {
		return "", fmt.Errorf("read TeX table companion %s: %w", companion, err)
	}
	return latexpdf.SupplementTableMacros(source, string(definitions)), nil
}

func (expander *localTeXExpander) expand(filename, source string, depth int) (string, error) {
	if depth >= maxTeXIncludeDepth {
		return "", fmt.Errorf("TeX include nesting exceeds %d levels at %s", maxTeXIncludeDepth, filename)
	}
	filename = filepath.Clean(filename)
	if expander.active[filename] {
		return "", fmt.Errorf("recursive TeX include: %s", filename)
	}
	expander.active[filename] = true
	defer delete(expander.active, filename)

	var output strings.Builder
	for position := 0; position < len(source); {
		start, name, end, ok := nextLocalTeXInclude(source, position)
		if !ok {
			output.WriteString(source[position:])
			break
		}
		output.WriteString(source[position:start])
		includePath := filepath.Clean(filepath.Join(filepath.Dir(filename), filepath.FromSlash(name)))
		data, err := os.ReadFile(includePath)
		if errors.Is(err, os.ErrNotExist) && filepath.Ext(includePath) == "" {
			includePath += ".tex"
			data, err = os.ReadFile(includePath)
		}
		if errors.Is(err, os.ErrNotExist) {
			// Preserve unresolved package-generated or intentionally absent inputs.
			output.WriteString(source[start:end])
			position = end
			continue
		}
		if err != nil {
			return "", fmt.Errorf("read TeX include %s: %w", includePath, err)
		}
		expanded, err := expander.expand(includePath, string(data), depth+1)
		if err != nil {
			return "", err
		}
		output.WriteString(expanded)
		if expander.maxBytes > 0 && output.Len()+len(source)-end > expander.maxBytes {
			return "", fmt.Errorf("expanded TeX document exceeds the %d MiB preview limit", expander.maxBytes>>20)
		}
		position = end
	}
	return output.String(), nil
}

func nextLocalTeXInclude(source string, start int) (position int, name string, end int, ok bool) {
	for cursor := start; cursor < len(source); cursor++ {
		if source[cursor] == '%' && !texCharacterEscaped(source, cursor) {
			if newline := strings.IndexByte(source[cursor:], '\n'); newline >= 0 {
				cursor += newline
				continue
			}
			return 0, "", 0, false
		}
		if source[cursor] != '\\' || texCharacterEscaped(source, cursor) {
			continue
		}
		command := ""
		for _, candidate := range []string{`\input`, `\include`} {
			if strings.HasPrefix(source[cursor:], candidate) && latexpdf.CommandBoundary(source, cursor+len(candidate)) {
				command = candidate
				break
			}
		}
		if command == "" {
			continue
		}
		argumentStart := latexpdf.SkipSpace(source, cursor+len(command))
		if argumentStart >= len(source) {
			continue
		}
		if source[argumentStart] == '{' {
			value, argumentEnd, parsed := latexpdf.ParseBraced(source, argumentStart)
			if parsed && strings.TrimSpace(value) != "" {
				return cursor, strings.TrimSpace(value), argumentEnd, true
			}
			continue
		}
		argumentEnd := argumentStart
		for argumentEnd < len(source) && !unicode.IsSpace(rune(source[argumentEnd])) && source[argumentEnd] != '%' {
			argumentEnd++
		}
		if argumentEnd > argumentStart {
			return cursor, strings.TrimSpace(source[argumentStart:argumentEnd]), argumentEnd, true
		}
	}
	return 0, "", 0, false
}

func texCharacterEscaped(source string, position int) bool {
	backslashes := 0
	for position > 0 && source[position-1] == '\\' {
		backslashes++
		position--
	}
	return backslashes%2 == 1
}
