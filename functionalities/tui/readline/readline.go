// Package readline is a backend-independent, bounded single-line editor. It
// owns text/cursor/history only; callers own keyboard decoding, rendering,
// completion, clipboard protocols and command execution.
package readline

import (
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"

	"golang.org/x/text/width"
)

const DefaultLimit = 16384

type Result struct {
	Handled, Changed, Submit, Cancel bool
	Err                              error
}
type Line struct {
	text                 []rune
	Cursor, Min, Limit   int
	CancelEmptyBackspace bool
	view                 int
	kill                 string
	history              []string
	historyIndex         int
	draft                string
	draftCursor          int
	pending              []byte
}

func New(text string, minimum, limit int) *Line {
	line := &Line{Limit: limit}
	line.Reset(text, minimum)
	return line
}
func (l *Line) Reset(text string, minimum int) {
	l.text = []rune(text)
	l.Min = max(0, min(minimum, len(l.text)))
	l.Cursor = len(l.text)
	l.view = l.Min
	l.pending = nil
	l.historyIndex = len(l.history)
	l.draft = text
	l.draftCursor = l.Cursor
	if l.Limit <= 0 {
		l.Limit = DefaultLimit
	}
}
func (l *Line) Text() string    { return string(l.text) }
func (l *Line) ByteCursor() int { return len(string(l.text[:max(l.Min, min(l.Cursor, len(l.text)))])) }
func (l *Line) ViewStart() int  { return l.view }
func (l *Line) SetHistory(history []string) {
	l.history = append([]string(nil), history[max(0, len(history)-100):]...)
	l.historyIndex = len(l.history)
	l.draft = l.Text()
	l.draftCursor = l.Cursor
}
func (l *Line) Insert(text string) error {
	text = strings.NewReplacer("\r\n", " ", "\r", " ", "\n", " ", "\t", " ").Replace(text)
	if !utf8.ValidString(text) || len(l.Text())+len(text) > l.Limit {
		return fmt.Errorf("single-line input exceeds %d bytes or is not UTF-8", l.Limit)
	}
	for _, r := range text {
		if unicode.IsControl(r) {
			return fmt.Errorf("control character in single-line input")
		}
	}
	l.Cursor = max(l.Min, min(l.Cursor, len(l.text)))
	at := l.Cursor
	l.text = append(append(append([]rune(nil), l.text[:at]...), []rune(text)...), l.text[at:]...)
	l.Cursor += utf8.RuneCountInString(text)
	return nil
}
func (l *Line) remove(first, last int, kill bool) {
	first, last = max(l.Min, min(first, len(l.text))), max(l.Min, min(last, len(l.text)))
	if last <= first {
		return
	}
	if kill {
		l.kill = string(l.text[first:last])
	}
	l.text = append(l.text[:first], l.text[last:]...)
	l.Cursor = first
}
func (l *Line) wordLeft(at int) int {
	for at > l.Min && unicode.IsSpace(l.text[at-1]) {
		at--
	}
	for at > l.Min && !unicode.IsSpace(l.text[at-1]) {
		at--
	}
	return at
}
func (l *Line) wordRight(at int) int {
	for at < len(l.text) && unicode.IsSpace(l.text[at]) {
		at++
	}
	for at < len(l.text) && !unicode.IsSpace(l.text[at]) {
		at++
	}
	return at
}
func (l *Line) historyMove(delta int) {
	if l.historyIndex == len(l.history) {
		l.draft, l.draftCursor = l.Text(), l.Cursor
	}
	prefix := string(l.text[:l.Min])
	for index := l.historyIndex + delta; index >= 0 && index < len(l.history); index += delta {
		if strings.HasPrefix(l.history[index], prefix) {
			l.historyIndex = index
			l.text = []rune(l.history[index])
			l.Cursor = len(l.text)
			l.view = l.Min
			return
		}
	}
	if delta > 0 {
		l.historyIndex = len(l.history)
		l.text = []rune(l.draft)
		l.Cursor = l.draftCursor
		l.view = l.Min
	}
}
func (l *Line) Handle(key string) (result Result) {
	before := l.Text()
	l.Cursor = max(l.Min, min(l.Cursor, len(l.text)))
	at := l.Cursor
	result.Handled = true
	switch key {
	case "<Enter>", "\r", "\n":
		result.Submit = true
	case "<Esc>", "escape", "\x1b", "\x03":
		result.Cancel = true
	case "<Left>", "left", "\x02":
		l.Cursor = max(l.Min, at-1)
	case "<Right>", "right", "\x06":
		l.Cursor = min(len(l.text), at+1)
	case "<Home>", "home", "\x01":
		l.Cursor = l.Min
	case "<End>", "end", "\x05":
		l.Cursor = len(l.text)
	case "<C-Left>", "<A-b>":
		l.Cursor = l.wordLeft(at)
	case "<C-Right>", "<A-f>":
		l.Cursor = l.wordRight(at)
	case "<BS>", "\x7f", "\b":
		if at > l.Min {
			l.remove(at-1, at, false)
		} else if len(l.text) == l.Min && l.CancelEmptyBackspace {
			result.Cancel = true
		}
	case "<Delete>", "delete", "\x04":
		l.remove(at, at+1, false)
	case "\x15":
		l.remove(l.Min, at, true)
	case "\x0b":
		l.remove(at, len(l.text), true)
	case "\x17", "<A-BS>":
		l.remove(l.wordLeft(at), at, true)
	case "<A-d>":
		l.remove(at, l.wordRight(at), true)
	case "\x19":
		result.Err = l.Insert(l.kill)
	case "<Up>", "up", "\x10":
		l.historyMove(-1)
	case "<Down>", "down", "\x0e":
		l.historyMove(1)
	default:
		if r := []rune(key); len(r) == 1 && !unicode.IsControl(r[0]) {
			result.Err = l.Insert(key)
		} else {
			result.Handled = false
		}
	}
	result.Changed = before != l.Text()
	return
}

// FeedByte assembles fragmented UTF-8 for applications with byte-based input.
func (l *Line) FeedByte(b byte) Result {
	if b < 0x80 {
		l.pending = nil
		return l.Handle(string(b))
	}
	l.pending = append(l.pending, b)
	if !utf8.FullRune(l.pending) {
		return Result{Handled: true}
	}
	r, size := utf8.DecodeRune(l.pending)
	l.pending = nil
	if r == utf8.RuneError && size == 1 {
		return Result{Handled: true, Err: fmt.Errorf("invalid UTF-8 input")}
	}
	return l.Handle(string(r))
}
func CellWidth(r rune, column int) int {
	if unicode.Is(unicode.Mn, r) || unicode.Is(unicode.Me, r) {
		return 0
	}
	if unicode.IsControl(r) {
		return 2
	}
	switch width.LookupRune(r).Kind() {
	case width.EastAsianWide, width.EastAsianFullwidth:
		return 2
	}
	return 1
}

// View keeps a protected prefix pinned and scrolls the editable text so the
// insertion cursor remains visible. It returns text, not terminal sequences.
func (l *Line) View(cells int, measure func(rune, int) int) (string, int) {
	if cells <= 0 {
		return "", 0
	}
	if measure == nil {
		measure = CellWidth
	}
	l.Cursor = max(l.Min, min(l.Cursor, len(l.text)))
	prefix := 0
	for _, r := range l.text[:l.Min] {
		prefix += measure(r, prefix)
	}
	if prefix >= cells {
		return string(l.text[:l.Min]), cells - 1
	}
	first := max(l.Min, min(l.view, l.Cursor))
	columns := make([]int, len(l.text)+1)
	for i := l.Min; i < len(l.text); i++ {
		columns[i+1] = columns[i] + measure(l.text[i], prefix+columns[i])
	}
	for first < l.Cursor && prefix+columns[l.Cursor]-columns[first] >= cells {
		first++
	}
	l.view = first
	cursor := prefix + columns[l.Cursor] - columns[first]
	var out strings.Builder
	out.WriteString(string(l.text[:l.Min]))
	column := prefix
	for _, r := range l.text[first:] {
		w := measure(r, column)
		if column+w > cells {
			break
		}
		out.WriteRune(r)
		column += w
	}
	return out.String(), min(cells-1, cursor)
}
func CSI(sequence string) string {
	return map[string]string{"A": "<Up>", "B": "<Down>", "C": "<Right>", "D": "<Left>", "H": "<Home>", "F": "<End>", "1~": "<Home>", "7~": "<Home>", "4~": "<End>", "8~": "<End>", "3~": "<Delete>", "1;5D": "<C-Left>", "1;5C": "<C-Right>"}[sequence]
}
