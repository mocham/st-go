// Package preview prepares saved LaTeX/Markdown documents for retained terminal
// PDF previews. It has no display connection, editor process, or file watcher.
package preview

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"unicode"

	"github.com/mocham/GoLS/latexpdf"
	"st-go/limits"
)

type Reader func(string) (string, error)

type Snapshot struct {
	Source, Root string
	Hash         [sha256.Size]byte
	CursorLine   int // expanded source line; first %cursor wins
	markdown     bool
	lines        map[string][]int // file line -> expanded line, first inclusion wins
	files        map[string]bool  // includes unresolved generated inputs
}

func Markdown(path string) bool {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".md", ".markdown", ".mdown", ".mkd":
		return true
	}
	return false
}

func Supported(path string) bool {
	if Markdown(path) {
		return true
	}
	switch strings.ToLower(filepath.Ext(path)) {
	case ".tex", ".latex", ".ltx":
		return true
	}
	return false
}

func cleanPath(path string) string {
	if absolute, err := filepath.Abs(path); err == nil {
		path = absolute
	}
	if resolved, err := filepath.EvalSymlinks(path); err == nil {
		return resolved
	}
	return filepath.Clean(path)
}

var mainDirective = regexp.MustCompile(`(?m)^\s*%\s*main\s*=\s*([^\r\n]+)`)
var documentBegin = regexp.MustCompile(`(?m)^[^%\n]*\\begin\s*\{document\}`)

// Load is called only on activation/save/reload. The supplied reader can
// overlay an immutable just-saved buffer and decode its editor encoding.
func Load(path string, read Reader) (*Snapshot, error) {
	if read == nil {
		return nil, fmt.Errorf("preview reader required")
	}
	path = cleanPath(path)
	s := &Snapshot{Root: path, markdown: Markdown(path), files: make(map[string]bool), lines: make(map[string][]int)}
	e := expander{snapshot: s, read: read, cache: make(map[string]string), active: make(map[string]bool)}
	source, err := e.readFile(path)
	if err != nil {
		return nil, err
	}
	if !s.markdown {
		visited := map[string]bool{path: true}
		for depth := 0; ; depth++ {
			if documentBegin.MatchString(source) {
				break
			}
			match := mainDirective.FindStringSubmatch(source)
			if match == nil {
				break
			}
			if depth >= 16 {
				return nil, fmt.Errorf("too many %%main redirects")
			}
			next := strings.TrimSpace(match[1])
			if !filepath.IsAbs(next) {
				next = filepath.Join(filepath.Dir(s.Root), next)
			}
			next = cleanPath(next)
			value, err := e.readFile(next)
			if errors.Is(err, os.ErrNotExist) && filepath.Ext(next) == "" {
				next = cleanPath(next + ".tex")
				value, err = e.readFile(next)
			}
			if err != nil {
				return nil, err
			}
			if visited[next] {
				return nil, fmt.Errorf("cycle in %%main redirects")
			}
			visited[next] = true
			s.Root, source = next, value
		}
	}
	var expanded expansion
	if s.markdown {
		expanded = expansion{text: source, lines: map[string][]int{s.Root: lineOffsets(source)}}
	} else {
		expanded, err = e.expand(s.Root, source, 0)
		if err != nil {
			return nil, err
		}
	}
	if len(expanded.text) > limits.FileBytes {
		return nil, fmt.Errorf("expanded preview source exceeds resource limit")
	}
	s.Source = expanded.text
	if 1+strings.Count(s.Source, "\n") > limits.MaxBytes/64 {
		return nil, fmt.Errorf("too many expanded preview source lines")
	}
	newlines := lineOffsets(s.Source)
	for file, offsets := range expanded.lines {
		for i, offset := range offsets {
			offsets[i] = sort.Search(len(newlines), func(j int) bool { return newlines[j] > offset })
		}
		s.lines[file] = offsets
	}
	s.CursorLine, _ = cursorMarker(s.Source, s.markdown)
	checksum := sha256.New()
	_, _ = checksum.Write([]byte(s.Root))
	_, _ = checksum.Write([]byte{0})
	_, _ = checksum.Write([]byte(s.Source))
	copy(s.Hash[:], checksum.Sum(nil))
	return s, nil
}

func (s *Snapshot) HasFile(path string) bool { return s != nil && s.files[cleanPath(path)] }

// Position approximately maps a source line to the scroll range, including lines
// in included files as well as the root. It is not SyncTeX.
func (s *Snapshot) Position(path string, line int) int {
	if offsets := s.lines[cleanPath(path)]; len(offsets) > 0 {
		line = offsets[max(0, min(line-1, len(offsets)-1))]
	}
	return s.ExpandedPosition(line)
}

func (s *Snapshot) ExpandedPosition(line int) int {
	first, last := 1, 1+strings.Count(s.Source, "\n")
	if !s.markdown {
		if at := strings.Index(s.Source, `\begin{document}`); at >= 0 {
			first = 1 + strings.Count(s.Source[:at], "\n")
		}
		if at := strings.LastIndex(s.Source, `\end{document}`); at >= 0 {
			last = 1 + strings.Count(s.Source[:at], "\n")
		}
	}
	line = max(first, min(line, last))
	return int(int64(line-first) * 1000000 / int64(max(1, last-first)))
}

func lineOffsets(source string) []int {
	offsets := []int{0}
	for i := range source {
		if source[i] == '\n' {
			offsets = append(offsets, i+1)
		}
	}
	return offsets
}

type expansion struct {
	text  string
	lines map[string][]int
}
type expander struct {
	snapshot     *Snapshot
	read         Reader
	cache        map[string]string
	active       map[string]bool
	bytes, lines int
}

func (e *expander) readFile(path string) (string, error) {
	path = cleanPath(path)
	if value, ok := e.cache[path]; ok {
		return value, nil
	}
	if len(e.snapshot.files) >= 1024 {
		return "", fmt.Errorf("too many preview include files")
	}
	e.snapshot.files[path] = true
	value, err := e.read(path)
	if err != nil {
		return "", err
	}
	e.bytes += len(value)
	e.lines += 1 + strings.Count(value, "\n")
	if e.bytes > limits.FileBytes || e.lines > limits.MaxBytes/64 {
		return "", fmt.Errorf("preview source files exceed resource limit")
	}
	e.cache[path] = value
	return value, nil
}

func (e *expander) expand(path, source string, depth int) (expansion, error) {
	if depth >= 32 || e.active[path] {
		return expansion{}, fmt.Errorf("recursive or excessively nested TeX include: %s", path)
	}
	e.active[path] = true
	defer delete(e.active, path)
	result := expansion{lines: make(map[string][]int)}
	offsets := lineOffsets(source)
	mapped := make([]int, len(offsets))
	index := 0
	var out strings.Builder
	appendText := func(text string, first, last int, replacement bool) error {
		if out.Len()+len(text) > limits.FileBytes {
			return fmt.Errorf("expanded preview source exceeds resource limit")
		}
		for index < len(offsets) && offsets[index] < last {
			mapped[index] = out.Len()
			if !replacement {
				mapped[index] += max(0, offsets[index]-first)
			}
			index++
		}
		out.WriteString(text)
		return nil
	}
	for pos := 0; pos < len(source); {
		first, last, name, ok := nextInclude(source, pos)
		if !ok {
			if err := appendText(source[pos:], pos, len(source), false); err != nil {
				return expansion{}, err
			}
			break
		}
		if err := appendText(source[pos:first], pos, first, false); err != nil {
			return expansion{}, err
		}
		include := name
		if !filepath.IsAbs(include) {
			include = filepath.Join(filepath.Dir(path), include)
		}
		include = cleanPath(include)
		value, err := e.readFile(include)
		if errors.Is(err, os.ErrNotExist) && filepath.Ext(include) == "" {
			include = cleanPath(include + ".tex")
			value, err = e.readFile(include)
		}
		text := source[first:last]
		if err == nil {
			child, err := e.expand(include, value, depth+1)
			if err != nil {
				return expansion{}, err
			}
			text = child.text
			for file, lines := range child.lines {
				if _, exists := result.lines[file]; exists {
					continue
				}
				for i := range lines {
					lines[i] += out.Len()
				}
				result.lines[file] = lines
			}
		} else if !errors.Is(err, os.ErrNotExist) {
			return expansion{}, err
		}
		if err := appendText(text, first, last, true); err != nil {
			return expansion{}, err
		}
		pos = last
	}
	for index < len(offsets) {
		mapped[index] = out.Len()
		index++
	}
	result.text = out.String()
	result.lines[path] = mapped
	return result, nil
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
		for _, name := range []string{`\input`, `\include`} {
			if strings.HasPrefix(source[i:], name) && latexpdf.CommandBoundary(source, i+len(name)) {
				command = name
				break
			}
		}
		if command == "" {
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

// Markdown supports the same %cursor directive without printing it in the PDF.
// Literal examples in fenced/inline code and escaped percent signs aren't anchors.
func cursorMarker(source string, markdown bool) (int, string) {
	lines := strings.Split(source, "\n")
	first := 0
	fence := byte(0)
	fenceSize := 0
	code := 0
	verbatim := ""
	for row, line := range lines {
		if markdown {
			if code == 0 && (strings.HasPrefix(line, "    ") || strings.HasPrefix(line, "\t")) {
				continue
			}
			trimmed := strings.TrimSpace(line)
			if code == 0 && len(trimmed) >= 3 && (trimmed[0] == '`' || trimmed[0] == '~') {
				n := 0
				for n < len(trimmed) && trimmed[n] == trimmed[0] {
					n++
				}
				if n >= 3 {
					if fence == 0 {
						fence, fenceSize = trimmed[0], n
						continue
					}
					if fence == trimmed[0] && n >= fenceSize && strings.TrimSpace(trimmed[n:]) == "" {
						fence = 0
						continue
					}
				}
			}
			if fence != 0 {
				continue
			}
		}
		for i := 0; i < len(line); i++ {
			if !markdown {
				if verbatim != "" {
					closing := `\end{` + verbatim + `}`
					end := strings.Index(line[i:], closing)
					if end < 0 {
						break
					}
					i += end + len(closing) - 1
					verbatim = ""
					continue
				}
				if strings.HasPrefix(line[i:], `\verb`) && latexpdf.CommandBoundary(line, i+len(`\verb`)) {
					start := i + len(`\verb`)
					if start < len(line) && line[start] == '*' {
						start++
					}
					if start >= len(line) {
						break
					}
					end := strings.IndexByte(line[start+1:], line[start])
					if end < 0 {
						break
					}
					i = start + end + 1
					continue
				}
				if strings.HasPrefix(line[i:], `\begin`) && latexpdf.CommandBoundary(line, i+len(`\begin`)) {
					arg := latexpdf.SkipSpace(line, i+len(`\begin`))
					if arg < len(line) && line[arg] == '{' {
						if name, end, ok := latexpdf.ParseBraced(line, arg); ok {
							switch name {
							case "verbatim", "verbatim*", "Verbatim", "lstlisting", "minted", "comment":
								verbatim = name
								i = end - 1
								continue
							}
						}
					}
				}
			}
			if line[i] == '\\' && i+1 < len(line) && (line[i+1] == '\\' || line[i+1] == '%' || markdown && line[i+1] == '`') {
				i++
				continue
			}
			if markdown && line[i] == '`' {
				n := 1
				for i+n < len(line) && line[i+n] == '`' {
					n++
				}
				if code == 0 {
					code = n
				} else if code == n {
					code = 0
				}
				i += n - 1
				continue
			}
			if line[i] != '%' || code != 0 {
				continue
			}
			comment := strings.TrimSpace(line[i+1:])
			if comment == "cursor" || strings.HasPrefix(comment, "cursor ") || strings.HasPrefix(comment, "cursor\t") {
				if first == 0 {
					first = row + 1
				}
				if markdown {
					lines[row] = line[:i]
				}
			}
			if !markdown || first == row+1 {
				break
			}
		}
	}
	if markdown {
		return first, strings.Join(lines, "\n")
	}
	return first, source
}
