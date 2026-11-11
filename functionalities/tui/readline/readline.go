// Package readline is a backend-independent, bounded single-line editor with
// optional in-process Pinyin composition. Callers own keyboard decoding,
// rendering, completion, clipboard protocols and command execution.
package readline

import (
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"

	"golang.org/x/text/width"
	"st-go/tui/pinyin"
)

const DefaultLimit = 16384

type Result struct {
	Handled, Changed, DisplayChanged, Submit, Cancel bool
	Err                                              error
}
type imeState struct {
	open, at, page, closing int
	prompt                  string
	serial                  uint64
	ready                   bool
	response                pinyin.Response
	selection               int
	queued                  []string
	queuedBytes             int
	worker                  *pinyin.Worker
}
type Line struct {
	text                 []rune
	Cursor, Min, Limit   int
	CancelEmptyBackspace bool
	DisableIME           bool
	view                 int
	kill                 string
	history              []string
	historyIndex         int
	draft                string
	draftCursor          int
	pending              []byte
	ime                  *imeState
	newPinyinWorker      func() *pinyin.Worker
}

func New(text string, minimum, limit int) *Line {
	line := &Line{Limit: limit}
	line.Reset(text, minimum)
	return line
}
func (l *Line) Reset(text string, minimum int) {
	l.closeIME()
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

// Close releases a Pinyin worker if a line is discarded during composition.
// It is safe to call more than once.
func (l *Line) Close()          { l.closeIME() }
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

func (l *Line) imeValid() bool {
	state := l.ime
	if state == nil || state.open < l.Min || state.open+3 > len(l.text) || string(l.text[state.open:state.open+3]) != "[[[" {
		return false
	}
	if state.prompt == "" {
		return l.Cursor >= state.open+3 && l.Cursor <= len(l.text)
	}
	end := state.at + utf8.RuneCountInString(state.prompt)
	return state.at >= state.open+3 && end <= len(l.text) && l.Cursor == end && string(l.text[state.at:end]) == state.prompt
}

func (l *Line) resetIMEComposition() {
	state := l.ime
	if state == nil {
		return
	}
	state.at = l.Cursor
	state.prompt = ""
	state.page = 0
	state.serial++ // invalidate an in-flight response
	state.ready = false
	state.response = pinyin.Response{}
	state.selection = 0
}

func (l *Line) queryIME() error {
	state := l.ime
	if state == nil || state.prompt == "" {
		return nil
	}
	if state.worker == nil {
		if l.newPinyinWorker != nil {
			state.worker = l.newPinyinWorker()
		} else {
			state.worker = pinyin.NewLocalWorker()
		}
	}
	state.serial++
	state.ready = false
	state.response = pinyin.Response{}
	return state.worker.Submit(pinyin.Request{ID: state.serial, Prompt: state.prompt, Page: state.page})
}

func (l *Line) chooseIME(index int) error {
	state := l.ime
	if state == nil || !state.ready || index < 0 || index >= len(state.response.Candidates) || !l.imeValid() {
		return nil
	}
	chosen := state.response.Candidates[index]
	end := state.at + utf8.RuneCountInString(state.prompt)
	bytesAfter := len(l.Text()) - len(state.prompt) + len(chosen)
	if bytesAfter > l.Limit {
		return fmt.Errorf("single-line input exceeds %d bytes", l.Limit)
	}
	l.text = append(append(append([]rune(nil), l.text[:state.at]...), []rune(chosen)...), l.text[end:]...)
	l.Cursor = state.at + utf8.RuneCountInString(chosen)
	l.resetIMEComposition()
	return nil
}

func (l *Line) finishIME() {
	state := l.ime
	end := l.Cursor
	if state == nil || state.open < l.Min || end < state.open+6 || end > len(l.text) || string(l.text[state.open:state.open+3]) != "[[[" || string(l.text[end-3:end]) != "]]]" {
		l.closeIME()
		return
	}
	body := append([]rune(nil), l.text[state.open+3:end-3]...)
	l.text = append(append(append([]rune(nil), l.text[:state.open]...), body...), l.text[end:]...)
	l.Cursor = end - 6
	l.closeIME()
}

func mergeResult(destination *Result, source Result) {
	destination.Handled = destination.Handled || source.Handled
	destination.Changed = destination.Changed || source.Changed
	destination.DisplayChanged = destination.DisplayChanged || source.DisplayChanged
	destination.Submit = destination.Submit || source.Submit
	destination.Cancel = destination.Cancel || source.Cancel
	if destination.Err == nil {
		destination.Err = source.Err
	}
}

func (l *Line) closeIME() []string {
	state := l.ime
	if state == nil {
		return nil
	}
	l.ime = nil
	if state.worker != nil {
		state.worker.Close()
	}
	return append([]string(nil), state.queued...)
}

func (l *Line) replayIME(keys []string, result *Result) {
	for _, key := range keys {
		mergeResult(result, l.Handle(key))
	}
}

func (l *Line) queueIME(key string, result *Result) {
	state := l.ime
	if state == nil {
		return
	}
	if len(state.queued) >= 4096 || state.queuedBytes+len(key) > 65536 {
		l.closeIME()
		result.Err = fmt.Errorf("pending Pinyin input limit exceeded")
		result.DisplayChanged = true
		return
	}
	state.queued = append(state.queued, key)
	state.queuedBytes += len(key)
}

// Poll applies completed asynchronous Pinyin lookups without blocking. Call it
// from an application's existing timer/input tick and redraw when
// DisplayChanged is true. Text changes are possible when input queued behind a
// pending candidate selection is replayed.
func (l *Line) Poll() (result Result) {
	before := l.Text()
	for {
		state := l.ime
		if state == nil || state.worker == nil {
			result.Changed = result.Changed || before != l.Text()
			return result
		}
		select {
		case update := <-state.worker.Results():
			result.Handled = true
			result.DisplayChanged = true
			if update.Err != nil {
				queued := l.closeIME()
				result.Err = update.Err
				l.replayIME(queued, &result)
				result.Changed = result.Changed || before != l.Text()
				return result
			}
			if update.Response.ID != state.serial || update.Response.Prompt != state.prompt {
				continue
			}
			state.response = update.Response
			state.page = update.Response.Page
			state.ready = true
			if state.selection != 0 {
				selection := state.selection - 1
				queued := append([]string(nil), state.queued...)
				state.queued = nil
				state.queuedBytes = 0
				state.selection = 0
				if err := l.chooseIME(selection); err != nil {
					result.Err = err
				}
				l.replayIME(queued, &result)
				result.Changed = result.Changed || before != l.Text()
				return result
			}
		default:
			result.Changed = result.Changed || before != l.Text()
			return result
		}
	}
}

func (l *Line) handleIME(key string, result *Result) bool {
	if !l.imeValid() {
		l.closeIME()
		return false
	}
	state := l.ime
	result.Handled = true
	result.DisplayChanged = true
	if state.selection != 0 {
		if key == "<Esc>" || key == "escape" || key == "\x1b" {
			queued := l.closeIME()
			l.replayIME(queued, result)
			return true
		}
		l.queueIME(key, result)
		return true
	}
	if key == "<Esc>" || key == "escape" || key == "\x1b" {
		l.closeIME()
		return true
	}
	if key == "]" {
		l.resetIMEComposition()
		if err := l.Insert("]"); err != nil {
			result.Err = err
			return true
		}
		state.closing++
		if state.closing == 3 {
			l.finishIME()
		}
		return true
	}
	state.closing = 0
	if len(key) == 1 && (key[0] >= 'a' && key[0] <= 'z' || key == "'") {
		if state.prompt == "" {
			state.at = l.Cursor
		}
		if err := l.Insert(key); err != nil {
			result.Err = err
			return true
		}
		state.prompt += key
		state.page = 0
		if len(state.prompt) > pinyin.MaxPrompt {
			l.closeIME()
			result.Err = fmt.Errorf("Pinyin prompt exceeds %d letters", pinyin.MaxPrompt)
			return true
		}
		if err := l.queryIME(); err != nil {
			l.closeIME()
			result.Err = err
		}
		return true
	}
	if state.prompt != "" {
		switch key {
		case "+", "-":
			page := state.page
			if key == "+" {
				page = min(pinyin.MaxPage, page+1)
			} else {
				page = max(0, page-1)
			}
			if state.response.Total > 0 {
				page = min(page, (state.response.Total-1)/pinyin.PageSize)
			}
			if page != state.page {
				state.page = page
				if err := l.queryIME(); err != nil {
					l.closeIME()
					result.Err = err
				}
			}
			return true
		case "1", "2", "3", "4", "5":
			state.selection = int(key[0]-'1') + 1
			if state.ready {
				state.selection = 0
				result.Err = l.chooseIME(int(key[0] - '1'))
			}
			return true
		case "<BS>", "\x7f", "\b":
			l.remove(l.Cursor-1, l.Cursor, false)
			state.prompt = state.prompt[:len(state.prompt)-1]
			state.page = 0
			state.response = pinyin.Response{}
			if state.prompt == "" {
				l.resetIMEComposition()
			} else if err := l.queryIME(); err != nil {
				l.closeIME()
				result.Err = err
			}
			return true
		}
	}
	if runes := []rune(key); len(runes) == 1 && runes[0] >= 32 {
		l.resetIMEComposition()
		return false
	}
	if key == "<BS>" || key == "\x7f" || key == "\b" {
		return false
	}
	// Navigation, submission, and other controls leave the recoverable opening
	// delimiter in the line, then retain their ordinary readline behavior.
	l.closeIME()
	return false
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
	mergeResult(&result, l.Poll())
	if result.Submit || result.Cancel {
		result.Changed = result.Changed || before != l.Text()
		return result
	}
	l.Cursor = max(l.Min, min(l.Cursor, len(l.text)))
	if l.ime != nil && l.handleIME(key, &result) {
		result.Changed = result.Changed || before != l.Text()
		return result
	}
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
		if err := l.Insert(l.kill); result.Err == nil {
			result.Err = err
		}
	case "<Up>", "up", "\x10":
		l.historyMove(-1)
	case "<Down>", "down", "\x0e":
		l.historyMove(1)
	default:
		if r := []rune(key); len(r) == 1 && !unicode.IsControl(r[0]) {
			if err := l.Insert(key); result.Err == nil {
				result.Err = err
			}
		} else {
			result.Handled = false
		}
	}
	if !l.DisableIME && result.Err == nil && before != l.Text() && key == "[" && l.Cursor >= l.Min+3 && string(l.text[l.Cursor-3:l.Cursor]) == "[[[" {
		l.ime = &imeState{open: l.Cursor - 3, at: l.Cursor}
		result.DisplayChanged = true
	} else if l.ime != nil && !l.imeValid() {
		l.closeIME()
		result.DisplayChanged = true
	}
	result.Changed = result.Changed || before != l.Text()
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

// Display is a cell-bounded readline view split at the insertion cursor.
// Candidates are transient display text and are never part of Line.Text.
type Display struct {
	Before, Candidates, After string
	Cursor                    int
}

// IMEActive reports whether this line is currently interpreting a live [[[...
// session. Escape leaves this mode without canceling the enclosing readline.
func (l *Line) IMEActive() bool { return l.ime != nil }

func (l *Line) candidateText() string {
	if l.ime == nil || len(l.ime.response.Candidates) == 0 {
		return ""
	}
	var output strings.Builder
	for index, candidate := range l.ime.response.Candidates {
		fmt.Fprintf(&output, " %d:%s", index+1, candidate)
	}
	return output.String()
}

func truncateDisplay(text string, cells, column int, measure func(rune, int) int) (string, int) {
	if cells <= 0 {
		return "", 0
	}
	var output strings.Builder
	used := 0
	for _, character := range text {
		width := measure(character, column+used)
		if used+width > cells {
			break
		}
		output.WriteRune(character)
		used += width
	}
	return output.String(), used
}

func displayWidth(text string, column int, measure func(rune, int) int) int {
	used := 0
	for _, character := range text {
		used += measure(character, column+used)
	}
	return used
}

// ViewDisplay keeps the cursor visible and inserts the current numbered Pinyin
// choices immediately after it. Callers render Candidates in a distinct style.
func (l *Line) ViewDisplay(cells int, measure func(rune, int) int) Display {
	if cells <= 0 {
		return Display{}
	}
	if l.ime != nil && !l.imeValid() {
		l.closeIME()
	}
	if measure == nil {
		measure = CellWidth
	}
	candidateText := l.candidateText()
	prefixCells := displayWidth(string(l.text[:l.Min]), 0, measure)
	available := max(0, cells-prefixCells)
	reserve := min(displayWidth(candidateText, 0, measure), available*2/3)
	if l.ime != nil && len(l.ime.response.Candidates) > 0 && available > 1 {
		firstRune, _ := utf8.DecodeRuneInString(l.ime.response.Candidates[0])
		firstChoice := " 1:" + string(firstRune)
		reserve = max(reserve, min(displayWidth(firstChoice, 0, measure), available-1))
	}
	shown, cursor := l.View(max(1, cells-reserve), measure)
	first := max(l.Min, min(l.view, l.Cursor))
	before := string(l.text[:l.Min]) + string(l.text[first:l.Cursor])
	if len(before) > len(shown) {
		before = shown
	}
	after := strings.TrimPrefix(shown, before)
	remaining := max(0, cells-cursor)
	candidates, candidateCells := truncateDisplay(candidateText, remaining, cursor, measure)
	remaining -= candidateCells
	after, _ = truncateDisplay(after, remaining, cursor+candidateCells, measure)
	return Display{Before: before, Candidates: candidates, After: after, Cursor: cursor}
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
		shown, _ := truncateDisplay(string(l.text[:l.Min]), cells, 0, measure)
		return shown, cells - 1
	}
	first := max(l.Min, min(l.view, l.Cursor))
	columns := make([]int, len(l.text)+1)
	for i := l.Min; i < len(l.text); i++ {
		columns[i+1] = columns[i] + measure(l.text[i], prefix+columns[i])
	}
	for first < l.Cursor && prefix+columns[l.Cursor]-columns[first] >= cells {
		first++
	}
	for first > l.Min && prefix+columns[l.Cursor]-columns[first-1] < cells {
		first--
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
