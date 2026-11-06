package main

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"unicode"

	"github.com/mocham/GoLS/latexpdf"
	"st-go/limits"
)

type sourceSnapshot struct {
	text      string
	hash      [sha256.Size]byte
	files     map[string]os.FileInfo // nil also watches a missing include for creation
	err       error
	rootLines []int // root source line -> expanded source line (both one-based)
}

func (s *sourceSnapshot) expandedLine(line int) int {
	if len(s.rootLines) == 0 {
		return line
	}
	return s.rootLines[max(0, min(line-1, len(s.rootLines)-1))]
}

func (s *sourceSnapshot) changed() bool {
	for path, old := range s.files {
		now, err := os.Stat(path)
		if err != nil {
			if old != nil {
				return true
			}
			continue
		}
		if old == nil || !os.SameFile(old, now) || old.Size() != now.Size() || old.ModTime() != now.ModTime() || old.Mode() != now.Mode() {
			return true
		}
	}
	return false
}

func loadSource(path string) *sourceSnapshot {
	s := &sourceSnapshot{files: make(map[string]os.FileInfo)}
	e := sourceExpander{snapshot: s, active: make(map[string]bool)}
	var data []byte
	data, s.err = e.read(path)
	if s.err == nil {
		e.rootOffsets = []int{0}
		for i, b := range data {
			if b == '\n' {
				e.rootOffsets = append(e.rootOffsets, i+1)
			}
			if len(e.rootOffsets) > limits.MaxBytes/64 {
				s.err = fmt.Errorf("too many source lines")
				return s
			}
		}
		s.rootLines = make([]int, len(e.rootOffsets))
		s.text, s.err = e.expand(path, string(data), 0)
		var newlines []int
		for i := range s.text {
			if s.text[i] == '\n' {
				newlines = append(newlines, i)
			}
			if len(newlines) > limits.MaxBytes/64 {
				s.err = fmt.Errorf("too many expanded source lines")
				return s
			}
		}
		for i, offset := range s.rootLines {
			s.rootLines[i] = 1 + sort.SearchInts(newlines, offset)
		}
	}
	s.hash = sha256.Sum256([]byte(s.text))
	return s
}

type sourceExpander struct {
	snapshot    *sourceSnapshot
	active      map[string]bool
	readBytes   int
	rootOffsets []int
	rootIndex   int
}

func (e *sourceExpander) read(path string) ([]byte, error) {
	if len(e.snapshot.files) >= limits.ControlBytes/1024 {
		return nil, fmt.Errorf("too many TeX include files")
	}
	info, err := os.Stat(path)
	e.snapshot.files[path] = info
	if err != nil {
		return nil, err
	}
	if !limits.Check("vimtex source files", int64(e.readBytes)+info.Size(), limits.FileBytes) {
		return nil, fmt.Errorf("TeX source files exceed resource limit")
	}
	data, err := limits.ReadFile(path)
	e.readBytes += len(data)
	if !limits.Check("vimtex source files", int64(e.readBytes), limits.FileBytes) {
		return nil, fmt.Errorf("TeX source files exceed resource limit")
	}
	return data, err
}

func (e *sourceExpander) expand(path, source string, depth int) (string, error) {
	if depth >= 32 || e.active[path] {
		return "", fmt.Errorf("recursive or excessively nested TeX include: %s", path)
	}
	e.active[path] = true
	defer delete(e.active, path)
	var out strings.Builder
	appendText := func(text string, start, end int, replacement bool) error {
		if !limits.Check("vimtex expanded source", int64(out.Len())+int64(len(text)), limits.FileBytes) {
			return fmt.Errorf("expanded TeX exceeds resource limit")
		}
		if depth == 0 {
			for e.rootIndex < len(e.rootOffsets) && e.rootOffsets[e.rootIndex] < end {
				offset := out.Len()
				if !replacement {
					offset += max(0, e.rootOffsets[e.rootIndex]-start)
				}
				e.snapshot.rootLines[e.rootIndex] = offset
				e.rootIndex++
			}
		}
		out.WriteString(text)
		return nil
	}
	for pos := 0; pos < len(source); {
		start, end, name, found := nextInclude(source, pos)
		if !found {
			if err := appendText(source[pos:], pos, len(source), false); err != nil {
				return "", err
			}
			break
		}
		if err := appendText(source[pos:start], pos, start, false); err != nil {
			return "", err
		}
		include := name
		if !filepath.IsAbs(include) {
			include = filepath.Join(filepath.Dir(path), include)
		}
		include = filepath.Clean(include)
		data, err := e.read(include)
		if errors.Is(err, os.ErrNotExist) && filepath.Ext(include) == "" {
			include += ".tex"
			data, err = e.read(include)
		}
		text := source[start:end]
		if err == nil {
			text, err = e.expand(include, string(data), depth+1)
		}
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return "", err
		}
		// Keep unresolved generated/package inputs; watch them for creation.
		if err := appendText(text, start, end, true); err != nil {
			return "", err
		}
		pos = end
	}
	if depth == 0 {
		for e.rootIndex < len(e.rootOffsets) {
			e.snapshot.rootLines[e.rootIndex] = out.Len()
			e.rootIndex++
		}
	}
	return out.String(), nil
}

func nextInclude(source string, start int) (int, int, string, bool) {
	for i := start; i < len(source); i++ {
		if source[i] == '%' {
			if n := strings.IndexByte(source[i:], '\n'); n >= 0 {
				i += n
				continue
			}
			break
		}
		if source[i] != '\\' {
			continue
		}
		command := ""
		for _, candidate := range []string{`\input`, `\include`} {
			if strings.HasPrefix(source[i:], candidate) && latexpdf.CommandBoundary(source, i+len(candidate)) {
				command = candidate
				break
			}
		}
		if command == "" {
			// Skip an escaped backslash or percent sign.
			if i+1 < len(source) && (source[i+1] == '\\' || source[i+1] == '%') {
				i++
			}
			continue
		}
		arg := latexpdf.SkipSpace(source, i+len(command))
		if arg >= len(source) {
			continue
		}
		if source[arg] == '{' {
			if name, end, ok := latexpdf.ParseBraced(source, arg); ok && strings.TrimSpace(name) != "" {
				return i, end, strings.TrimSpace(name), true
			}
		} else {
			end := arg
			for end < len(source) && !unicode.IsSpace(rune(source[end])) && source[end] != '%' {
				end++
			}
			if end > arg {
				return i, end, source[arg:end], true
			}
		}
	}
	return 0, 0, "", false
}
