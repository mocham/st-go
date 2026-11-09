package main

import (
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

const maxMatches = 65536

type searchState struct {
	pattern                    string
	backward, highlight, whole bool
	regex                      *regexp.Regexp
	matches                    []span
	version                    uint64
	buffer                     *Buffer
	active                     span
}
type substitution struct {
	matches             []span
	replacements        []string
	index, delta, count int
	group               uint64
	all                 bool
}

// The normal Vim magic operators are translated, with \v for RE2's extended
// syntax and \V for literal text. Unsupported backtracking features fail closed.
func compilePattern(pattern string) (*regexp.Regexp, error) {
	if len(pattern) > 4096 {
		return nil, fmt.Errorf("search pattern exceeds 4096 bytes")
	}
	ignore := false
	pattern = strings.ReplaceAll(pattern, "\\C", "")
	if strings.Contains(pattern, "\\c") {
		ignore = true
		pattern = strings.ReplaceAll(pattern, "\\c", "")
	}
	prefix := "(?m)"
	if ignore {
		prefix += "(?i)"
	}
	var translated strings.Builder
	if strings.HasPrefix(pattern, "\\v") {
		translated.WriteString(strings.TrimPrefix(pattern, "\\v"))
	} else if strings.HasPrefix(pattern, "\\V") {
		translated.WriteString(regexp.QuoteMeta(strings.TrimPrefix(pattern, "\\V")))
	} else {
		inClass := false
		for i := 0; i < len(pattern); i++ {
			c := pattern[i]
			if c == '\\' && i+1 < len(pattern) {
				i++
				next := pattern[i]
				if strings.ContainsRune("()+?|{}", rune(next)) {
					translated.WriteByte(next)
				} else if next == '<' || next == '>' {
					translated.WriteString("\\b")
				} else {
					translated.WriteByte('\\')
					translated.WriteByte(next)
				}
				continue
			}
			if c == '[' {
				inClass = true
			}
			if !inClass && strings.ContainsRune("()+?|{}", rune(c)) {
				translated.WriteByte('\\')
			}
			translated.WriteByte(c)
			if c == ']' {
				inClass = false
			}
		}
	}
	re, err := regexp.Compile(prefix + translated.String())
	if err != nil {
		return nil, err
	}
	if re.NumSubexp() > 32 {
		return nil, fmt.Errorf("pattern exceeds 32 capture groups")
	}
	return re, nil
}
func (e *Editor) refreshSearch() error {
	s := &e.search
	b := e.buffer()
	if s.regex == nil {
		return nil
	}
	if s.buffer == b && s.version == b.version && s.matches != nil {
		return nil
	}
	text := string(b.text)
	indices := s.regex.FindAllStringIndex(text, maxMatches+1)
	if len(indices) > maxMatches {
		return fmt.Errorf("more than %d matches; refine the pattern", maxMatches)
	}
	mapping := matchOffsets(text, indices)
	spans := make([]span, 0, len(indices))
	for _, pair := range indices {
		start, end := mapping[pair[0]], mapping[pair[1]]
		if s.whole && ((start > 0 && wordClass(b.text[start-1], false) == 1) || (end < len(b.text) && wordClass(b.text[end], false) == 1)) {
			continue
		}
		spans = append(spans, span{start, end})
	}
	s.matches, s.buffer, s.version = spans, b, b.version
	return nil
}
func (e *Editor) startSearch(pattern string, backward, whole bool, count int) {
	if pattern == "" {
		pattern = e.search.pattern
	}
	if pattern == "" {
		e.message = "No previous search"
		return
	}
	re, err := compilePattern(pattern)
	if err != nil {
		e.error(err)
		return
	}
	old := e.search
	e.search = searchState{pattern: pattern, backward: backward, whole: whole, highlight: true, regex: re}
	if err := e.refreshSearch(); err != nil {
		e.search = old
		e.error(err)
		return
	}
	e.searchNext(false, count)
}
func (e *Editor) searchNext(reverse bool, count int) {
	match, index, err := e.nextSearchMatch(reverse, count)
	if err != nil {
		e.error(err)
		return
	}
	e.search.highlight = true
	e.addJump()
	e.search.active = match
	e.setPos(match.start)
	e.message = fmt.Sprintf("%s  match %d/%d", e.search.pattern, index+1, len(e.search.matches))
}

func (e *Editor) nextSearchMatch(reverse bool, count int) (span, int, error) {
	if e.search.regex == nil {
		return span{}, 0, fmt.Errorf("no previous search")
	}
	if err := e.refreshSearch(); err != nil {
		return span{}, 0, err
	}
	matches := e.search.matches
	if len(matches) == 0 {
		return span{}, 0, fmt.Errorf("pattern not found: %s", e.search.pattern)
	}
	backward := e.search.backward != reverse
	index := sort.Search(len(matches), func(i int) bool { return matches[i].start > e.pos() })
	if backward {
		index = sort.Search(len(matches), func(i int) bool { return matches[i].start >= e.pos() }) - count
	} else {
		index += count - 1
	}
	index = ((index % len(matches)) + len(matches)) % len(matches)
	return matches[index], index, nil
}
func (e *Editor) searchWord(backward, whole bool, count int) {
	b := e.buffer()
	if len(b.text) == 0 {
		return
	}
	pos := min(e.pos(), len(b.text)-1)
	for pos < b.end(b.row(pos)) && wordClass(b.text[pos], false) == 0 {
		pos++
	}
	if pos >= len(b.text) {
		return
	}
	start, end := pos, pos+1
	class := wordClass(b.text[pos], false)
	for start > 0 && wordClass(b.text[start-1], false) == class {
		start--
	}
	for end < len(b.text) && wordClass(b.text[end], false) == class {
		end++
	}
	e.startSearch("\\V"+string(b.text[start:end]), backward, whole, count)
}

func (e *Editor) promptKey(key string) {
	switch key {
	case "<Esc>", "\x03":
		e.prompt = ""
		e.substitute = nil
		e.reset()
	case "<Enter>", "\r", "\n":
		prompt := e.prompt
		e.prompt = ""
		e.promptHistory = append(e.promptHistory, prompt)
		if len(e.promptHistory) > 100 {
			e.promptHistory = e.promptHistory[1:]
		}
		if prompt[0] == ':' {
			e.execute(prompt[1:])
		} else {
			count := e.number()
			if e.operator != "" {
				count = min(maxCount, count*e.operatorCount)
			}
			e.startSearch(prompt[1:], prompt[0] == '?', false, count)
			if e.operator != "" {
				target := e.pos()
				e.window().cursor = e.promptStart
				e.applyMotionOperator(target, false, false)
			}
		}
		e.reset()
	case "<BS>", "\x7f", "\b":
		r := []rune(e.prompt)
		if len(r) > 1 {
			e.prompt = string(r[:len(r)-1])
		} else {
			e.prompt = ""
			e.reset()
		}
	case "\x15":
		e.prompt = e.prompt[:1]
	case "<Up>", "<Down>":
		delta := -1
		if key == "<Down>" {
			delta = 1
		}
		for index := clamp(e.historyIndex+delta, 0, len(e.promptHistory)); index >= 0 && index < len(e.promptHistory); index += delta {
			if e.promptHistory[index][0] == e.prompt[0] {
				e.historyIndex = index
				e.prompt = e.promptHistory[index]
				return
			}
		}
		if key == "<Down>" {
			e.historyIndex = len(e.promptHistory)
			e.prompt = e.prompt[:1]
		}
	default:
		if len([]rune(key)) == 1 && []rune(key)[0] >= 0x20 && len(e.prompt) < 16384 {
			e.prompt += key
		}
	}
}

func parseSubstitute(value string) (pattern, replacement, flags string, err error) {
	if value == "" {
		return "", "", "", fmt.Errorf("missing substitution delimiter")
	}
	delimiter := value[0]
	if (delimiter >= 'a' && delimiter <= 'z') || (delimiter >= 'A' && delimiter <= 'Z') || delimiter == '\\' {
		return "", "", "", fmt.Errorf("invalid substitution delimiter")
	}
	parts := []string{}
	var part strings.Builder
	for i := 1; i < len(value); i++ {
		c := value[i]
		if c == '\\' && i+1 < len(value) {
			next := value[i+1]
			if next == delimiter {
				part.WriteByte(next)
			} else {
				part.WriteByte(c)
				part.WriteByte(next)
			}
			i++
			continue
		}
		if c == delimiter {
			parts = append(parts, part.String())
			part.Reset()
			if len(parts) == 2 {
				return parts[0], parts[1], strings.TrimSpace(value[i+1:]), nil
			}
			continue
		}
		part.WriteByte(c)
	}
	if len(parts) == 1 {
		return parts[0], part.String(), "", nil
	}
	return "", "", "", fmt.Errorf("incomplete substitute command")
}

func replacementText(template, text string, indices []int) string {
	var out strings.Builder
	for i := 0; i < len(template); i++ {
		c := template[i]
		if c == '&' {
			out.WriteString(text[indices[0]:indices[1]])
			continue
		}
		if c == '\\' && i+1 < len(template) {
			i++
			next := template[i]
			if next >= '0' && next <= '9' {
				n := int(next-'0') * 2
				if n+1 < len(indices) && indices[n] >= 0 {
					out.WriteString(text[indices[n]:indices[n+1]])
				}
			} else if next == 'r' || next == 'n' {
				out.WriteByte('\n')
			} else {
				out.WriteByte(next)
			}
			continue
		}
		out.WriteByte(c)
	}
	return out.String()
}

func (e *Editor) substituteRange(first, last int, value string) {
	pattern, replacement, flags, err := parseSubstitute(value)
	if err != nil {
		e.error(err)
		return
	}
	if pattern == "" {
		pattern = e.search.pattern
	}
	if pattern == "" {
		e.message = "No previous pattern"
		return
	}
	if strings.Contains(flags, "i") {
		pattern += "\\c"
	}
	re, err := compilePattern(pattern)
	if err != nil {
		e.error(err)
		return
	}
	for _, flag := range flags {
		if !strings.ContainsRune("gicnIe#pl", flag) {
			e.error(fmt.Errorf("unsupported substitute flag %q", flag))
			return
		}
	}
	b := e.buffer()
	start, end := b.start(first), b.end(last)
	text := string(b.text[start:end])
	indices := re.FindAllStringSubmatchIndex(text, maxMatches+1)
	if len(indices) > maxMatches {
		e.error(fmt.Errorf("too many substitution matches"))
		return
	}
	state := &substitution{group: b.group()}
	mapping := matchOffsets(text, indices)
	previousRow := -1
	resultBytes := b.byteSize
	for _, pair := range indices {
		a, z := start+mapping[pair[0]], start+mapping[pair[1]]
		row := b.row(a)
		if !strings.Contains(flags, "g") && row == previousRow {
			continue
		}
		previousRow = row
		state.matches = append(state.matches, span{a, z})
		next := replacementText(replacement, text, pair)
		resultBytes += len(next) - (pair[1] - pair[0])
		if resultBytes > maxBufferBytes {
			e.error(fmt.Errorf("substitution exceeds buffer limit"))
			return
		}
		state.replacements = append(state.replacements, next)
	}
	e.search = searchState{pattern: pattern, regex: re, highlight: true}
	e.lastSubstitute = value
	if strings.Contains(flags, "n") {
		e.message = fmt.Sprintf("%d matches", len(state.matches))
		return
	}
	if len(state.matches) == 0 {
		if !strings.Contains(flags, "e") {
			e.message = "Pattern not found: " + pattern
		}
		return
	}
	if strings.Contains(flags, "c") {
		e.substitute = state
		e.showSubstitute()
		return
	}
	var edits []patch
	for i := len(state.matches) - 1; i >= 0; i-- {
		m := state.matches[i]
		edits = append(edits, patch{m.start, string(b.text[m.start:m.end]), state.replacements[i]})
	}
	if e.edit(edits, state.matches[0].start, state.group) {
		e.setPos(state.matches[0].start)
		e.message = fmt.Sprintf("%d substitutions", len(edits))
	}
}

func matchOffsets(text string, indices [][]int) map[int]int {
	mapping := make(map[int]int, len(indices)*2)
	for _, pair := range indices {
		mapping[pair[0]] = 0
		mapping[pair[1]] = 0
	}
	runes := 0
	for at := range text {
		if _, ok := mapping[at]; ok {
			mapping[at] = runes
		}
		runes++
	}
	mapping[len(text)] = runes
	return mapping
}
func (e *Editor) showSubstitute() {
	s := e.substitute
	if s == nil {
		return
	}
	if s.index >= len(s.matches) {
		e.message = fmt.Sprintf("%d substitutions", s.count)
		e.substitute = nil
		return
	}
	match := s.matches[s.index]
	e.search.active = span{match.start + s.delta, match.end + s.delta}
	e.setPos(match.start + s.delta)
	e.message = "replace with " + strconv.Quote(s.replacements[s.index]) + "? (y/n/a/q/l)"
}
func (e *Editor) confirmSubstitute(key string) {
	s := e.substitute
	if s == nil {
		return
	}
	if key == "q" || key == "<Esc>" || key == "\x03" {
		e.substitute = nil
		e.message = fmt.Sprintf("%d substitutions", s.count)
		return
	}
	if key != "y" && key != "n" && key != "a" && key != "l" {
		return
	}
	if key == "a" {
		s.all = true
	}
	for s.index < len(s.matches) {
		if key != "n" {
			m := s.matches[s.index]
			at, end := m.start+s.delta, m.end+s.delta
			text := s.replacements[s.index]
			if !e.replace(at, end, text, s.group) {
				e.substitute = nil
				return
			}
			s.delta += len([]rune(text)) - (m.end - m.start)
			s.count++
		}
		s.index++
		if key == "l" {
			e.substitute = nil
			e.message = fmt.Sprintf("%d substitutions", s.count)
			return
		}
		if !s.all {
			break
		}
	}
	e.showSubstitute()
}
