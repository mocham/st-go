// Package imap implements immutable, data-driven insert suffix mappings and
// cursor-stop templates. It owns no editor state, files, terminal I/O or scripts.
package imap

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"path/filepath"
	"sort"
	"strings"
	"unicode/utf8"
)

const maxText = 64 << 10

type defaults struct {
	NotAfter []string `json:"not_after"`
}
type rule struct {
	LHS      string   `json:"lhs"`
	RHS      string   `json:"rhs"`
	NotAfter []string `json:"not_after"`
}
type config struct {
	Filetypes   []string          `json:"filetypes"`
	Placeholder string            `json:"placeholder"`
	Defaults    defaults          `json:"defaults"`
	IMAP        []rule            `json:"imap"`
	Templates   map[string]string `json:"templates"`
}

type Expansion struct {
	Text   string
	Cursor int // rune offset after consuming the first placeholder, or end of text
}
type Match struct {
	Expansion
	Remove int // runes preceding the incoming key to replace; key isn't inserted yet
}
type compiledRule struct {
	lhs       []rune
	expansion Expansion
	notAfter  [][]rune
}
type Set struct {
	filetypes   map[string]bool
	rules       []compiledRule // longest lhs first
	templates   map[string]string
	placeholder []rune
}

func Parse(data []byte) (*Set, error) {
	if len(data) > 1<<20 || !utf8.Valid(data) {
		return nil, fmt.Errorf("invalid/oversized IMAP data")
	}
	var config config
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&config); err != nil {
		return nil, err
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return nil, fmt.Errorf("trailing IMAP data")
	}
	if len(config.IMAP) > 4096 || len(config.Templates) > 256 || len(config.Filetypes) > 256 {
		return nil, fmt.Errorf("too many IMAP entries")
	}
	if n := utf8.RuneCountInString(config.Placeholder); n < 1 || n > 32 {
		return nil, fmt.Errorf("invalid IMAP placeholder")
	}
	s := &Set{filetypes: make(map[string]bool), templates: make(map[string]string), placeholder: []rune(config.Placeholder)}
	defaultGuards, err := compileGuards(config.Defaults.NotAfter)
	if err != nil {
		return nil, err
	}
	for _, ext := range config.Filetypes {
		if len(ext) < 2 || ext[0] != '.' || strings.ContainsAny(ext, "/\\ \r\n\t") {
			return nil, fmt.Errorf("invalid IMAP filetype %q", ext)
		}
		s.filetypes[strings.ToLower(ext)] = true
	}
	total := 0
	seen := make(map[string]bool)
	for _, entry := range config.IMAP {
		lhs := []rune(entry.LHS)
		total += len(entry.LHS) + len(entry.RHS)
		if len(lhs) == 0 || len(lhs) > 256 || len(entry.RHS) > maxText || seen[entry.LHS] {
			return nil, fmt.Errorf("invalid/duplicate IMAP lhs %q", entry.LHS)
		}
		seen[entry.LHS] = true
		compiled := compiledRule{lhs: lhs, expansion: s.expand(entry.RHS)}
		compiled.notAfter = defaultGuards
		if entry.NotAfter != nil {
			guards, err := compileGuards(entry.NotAfter)
			if err != nil {
				return nil, err
			}
			compiled.notAfter = guards
		}
		s.rules = append(s.rules, compiled)
	}
	for name, text := range config.Templates {
		total += len(text)
		if name == "" || len(name) > 256 || len(text) > maxText {
			return nil, fmt.Errorf("invalid IMAP template")
		}
		if _, err := substitute(text, nil, true); err != nil {
			return nil, err
		}
		s.templates[name] = text
	}
	if total > 2<<20 {
		return nil, fmt.Errorf("IMAP expansions exceed resource limit")
	}
	sort.SliceStable(s.rules, func(i, j int) bool { return len(s.rules[i].lhs) > len(s.rules[j].lhs) })
	return s, nil
}

func compileGuards(prefixes []string) ([][]rune, error) {
	if len(prefixes) > 256 {
		return nil, fmt.Errorf("too many IMAP prefix guards")
	}
	var guards [][]rune
	for _, prefix := range prefixes {
		if n := utf8.RuneCountInString(prefix); n < 1 || n > 256 {
			return nil, fmt.Errorf("invalid IMAP prefix guard")
		}
		guards = append(guards, []rune(prefix))
	}
	return guards, nil
}

func (s *Set) Supports(path string) bool {
	return s != nil && s.filetypes[strings.ToLower(filepath.Ext(path))]
}

// Match considers one live input rune. Callers must bypass this for pastes and
// literal/quoted input. The existing prefix isn't mutated or copied wholesale.
func (s *Set) Match(before []rune, input string) (Match, bool) {
	if s == nil || !utf8.ValidString(input) || utf8.RuneCountInString(input) != 1 {
		return Match{}, false
	}
	key, _ := utf8.DecodeRuneInString(input)
	for _, rule := range s.rules {
		remove := len(rule.lhs) - 1
		start := len(before) - remove
		if start < 0 || rule.lhs[remove] != key || !equal(before[start:], rule.lhs[:remove]) {
			continue
		}
		blocked := false
		for _, prefix := range rule.notAfter {
			if start >= len(prefix) && equal(before[start-len(prefix):start], prefix) {
				blocked = true
				break
			}
		}
		if !blocked {
			return Match{Expansion: rule.expansion, Remove: remove}, true
		}
	}
	return Match{}, false
}

func (s *Set) expand(text string) Expansion {
	marker := string(s.placeholder)
	cursor := strings.Index(text, marker)
	if cursor < 0 {
		return Expansion{Text: text, Cursor: utf8.RuneCountInString(text)}
	}
	return Expansion{Text: text[:cursor] + text[cursor+len(marker):], Cursor: utf8.RuneCountInString(text[:cursor])}
}

// Template substitutes literal ${variable} values, prefixes each line with
// indent, and consumes the first cursor stop. No expression evaluation occurs.
func (s *Set) Template(name string, values map[string]string, indent string) (Expansion, error) {
	if s == nil {
		return Expansion{}, fmt.Errorf("no IMAP set")
	}
	text, ok := s.templates[name]
	if !ok {
		return Expansion{}, fmt.Errorf("unknown IMAP template %q", name)
	}
	if !utf8.ValidString(indent) || len(indent) > maxText {
		return Expansion{}, fmt.Errorf("invalid IMAP indentation")
	}
	text, err := substitute(text, values, false)
	if err != nil {
		return Expansion{}, err
	}
	if len(indent) > (maxText-len(text))/(1+strings.Count(text, "\n")) {
		return Expansion{}, fmt.Errorf("IMAP template exceeds resource limit")
	}
	return s.expand(indent + strings.ReplaceAll(text, "\n", "\n"+indent)), nil
}

func substitute(text string, values map[string]string, validate bool) (string, error) {
	var out strings.Builder
	for {
		start := strings.Index(text, "${")
		if start < 0 {
			if out.Len()+len(text) > maxText {
				return "", fmt.Errorf("IMAP template exceeds resource limit")
			}
			out.WriteString(text)
			return out.String(), nil
		}
		end := strings.IndexByte(text[start+2:], '}')
		if end < 0 {
			return "", fmt.Errorf("unterminated IMAP variable")
		}
		end += start + 2
		name := text[start+2 : end]
		if name == "" || strings.ContainsAny(name, "${}\r\n\t ") {
			return "", fmt.Errorf("invalid IMAP variable")
		}
		value, ok := values[name]
		if validate {
			value, ok = "", true
		}
		if !ok || !utf8.ValidString(value) {
			return "", fmt.Errorf("missing/invalid IMAP variable %q", name)
		}
		if out.Len()+start+len(value) > maxText {
			return "", fmt.Errorf("IMAP template exceeds resource limit")
		}
		out.WriteString(text[:start])
		out.WriteString(value)
		text = text[end+1:]
	}
}

// NextPlaceholder finds the stop under the cursor, then forward, then wrapped.
// Returned rune bounds describe the literal text for the editor to remove.
func (s *Set) NextPlaceholder(text []rune, pos int) (first, last int, ok bool) {
	if s == nil || len(s.placeholder) == 0 {
		return 0, 0, false
	}
	pos = max(0, min(pos, len(text)))
	size := len(s.placeholder)
	at := func(index int) bool {
		return index >= 0 && index+size <= len(text) && equal(text[index:index+size], s.placeholder)
	}
	for index := max(0, pos-size+1); index <= pos && index+size <= len(text); index++ {
		if at(index) {
			return index, index + size, true
		}
	}
	for index := pos; index+size <= len(text); index++ {
		if at(index) {
			return index, index + size, true
		}
	}
	for index := 0; index < pos && index+size <= len(text); index++ {
		if at(index) {
			return index, index + size, true
		}
	}
	return 0, 0, false
}

func equal(a, b []rune) bool {
	if len(a) != len(b) {
		return false
	}
	for i, r := range a {
		if b[i] != r {
			return false
		}
	}
	return true
}
