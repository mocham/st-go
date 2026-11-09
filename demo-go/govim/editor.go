package main

import (
	"fmt"
	"os"
	"path/filepath"
	"st-go/tui/readline"
	"strconv"
	"strings"
	"time"
	"unicode"
)

type mode byte

const (
	normalMode mode = iota
	insertMode
	replaceMode
	visualMode
	visualLineMode
	visualBlockMode
)

type Window struct {
	buffer           *Buffer
	cursor, goal     int
	topLine, topWrap int
	rect             rectangle
	rows             []screenRow
	lineOriginal     string
	lineOriginalRow  int
}
type rectangle struct{ x, y, w, h int }
type pane struct {
	window        *Window
	vertical      bool
	ratio         float64
	first, second *pane
}
type tab struct {
	root   *pane
	active *Window
}
type location struct {
	buffer *Buffer
	pos    int
}
type register struct {
	Text        string
	Line, Block bool
	Width       int
	Keys        []keyEvent
}
type visualSelection struct {
	buffer         *Buffer
	anchor, cursor int
	kind           mode
}

type clipboard interface {
	Set(string, bool) error
	Request(bool) error
	Close() error
}

type Editor struct {
	tabs                             []*tab
	activeTab                        int
	buffers                          []*Buffer
	arguments                        []string
	argument                         int
	alternate                        *Buffer
	options                          journalOptions
	mode                             mode
	count                            string
	operator, pending                string
	operatorCount                    int
	forceMode                        mode
	selectedRegister                 rune
	registers                        map[rune]register
	marks                            map[rune]location
	jumps, changes                   []location
	jumpIndex, changeIndex           int
	lastJump                         location
	anchor                           int
	lastVisual                       visualSelection
	findKey                          string
	findRune                         rune
	motionGoal                       int
	insertGroup                      uint64
	insertCount                      int
	insertStart                      int
	insertText                       string
	replaceBack                      []patch
	blockInsert                      *blockInsertion
	oneNormal                        bool
	commandKeys, lastChange          []keyEvent
	changed, ignoreRepeat, replaying bool
	recording                        rune
	macroKeys                        []keyEvent
	lastMacro                        rune
	macroDepth, steps                int
	search                           searchState
	prompt                           string
	promptStart                      int
	promptCursor, promptView         int // rune indexes, including the ':'/'/'/'?' prefix
	promptEditor                     *readline.Line
	promptHistory                    []string
	lastSubstitute                   string
	lastEx, lastInsert               string
	completion                       *completionState
	substitute                       *substitution
	message                          string
	pager                            []string
	quit                             bool
	discard                          bool
	rows, cols                       int
	dirty                            bool
	fullRedraw                       bool
	syntax                           *syntaxHighlighter
	preview                          *editorPreview
	clip                             clipboard
	clipboardError                   string
	clipboardRead                    *clipboardRead
	registerReady                    bool
	emptyResult                      bool
	insertSeparator                  string
	runCommand                       func(string) error
	compile                          func(string, bool) error
	lastClick                        time.Time
	clickAnchor, clickCount          int
}

type blockInsertion struct {
	rows    []int
	column  int
	start   int
	initial string
}

func newEditor(options journalOptions) *Editor {
	return &Editor{options: options, registers: make(map[rune]register), marks: make(map[rune]location), rows: 24, cols: 80, dirty: true, selectedRegister: '"'}
}
func (e *Editor) window() *Window { return e.tabs[e.activeTab].active }
func (e *Editor) buffer() *Buffer { return e.window().buffer }
func (e *Editor) pos() int        { return e.window().cursor }
func (e *Editor) setPos(pos int) {
	w := e.window()
	if e.mode == insertMode || e.mode == replaceMode {
		w.cursor = clamp(pos, 0, len(w.buffer.text))
	} else {
		w.cursor = w.buffer.normal(pos)
	}
	w.goal = -1
	e.dirty = true
}
func (e *Editor) error(err error) {
	if err != nil {
		e.message = "Error: " + err.Error()
		e.dirty = true
	}
}
func (e *Editor) number() int { n, _ := strconv.Atoi(e.count); return max(1, min(n, maxCount)) }
func (e *Editor) reset() {
	e.count, e.operator, e.pending = "", "", ""
	e.operatorCount = 0
	e.forceMode = normalMode
	e.selectedRegister = '"'
	e.registerReady = false
}
func (e *Editor) visual() bool { return e.mode >= visualMode && e.mode <= visualBlockMode }

func (e *Editor) open(path string, recoverFile bool) (*Buffer, error) {
	if path != "" {
		canonical, err := canonicalPath(path)
		if err != nil {
			return nil, err
		}
		for _, b := range e.buffers {
			if b.meta.Path == canonical {
				if recoverFile {
					return nil, fmt.Errorf("buffer already open; its swap is active")
				}
				e.switchBuffer(b)
				return b, nil
			}
		}
		path = canonical
	}
	if len(e.buffers) >= 128 {
		return nil, fmt.Errorf("buffer limit reached")
	}
	var b *Buffer
	var err error
	if recoverFile {
		b = &Buffer{meta: fileMetadata{Path: path}}
	} else {
		b, err = loadBuffer(path)
		if err != nil {
			return nil, err
		}
	}
	total := b.byteSize
	for _, existing := range e.buffers {
		total += existing.byteSize
	}
	if total > 64<<20 {
		return nil, fmt.Errorf("total open-buffer text exceeds 64 MiB")
	}
	if err := attachJournal(b, e.options, recoverFile); err != nil {
		return nil, err
	}
	if recoverFile {
		total = b.byteSize
		for _, existing := range e.buffers {
			total += existing.byteSize
		}
		if total > 64<<20 {
			b.journal.Close(false)
			return nil, fmt.Errorf("total open-buffer text exceeds 64 MiB")
		}
	}
	e.buffers = append(e.buffers, b)
	if len(e.tabs) == 0 {
		w := &Window{buffer: b, cursor: b.normal(b.recoveryCursor), goal: -1, lineOriginalRow: -1}
		e.tabs = []*tab{{root: &pane{window: w}, active: w}}
	} else {
		e.switchBuffer(b)
	}
	if b.warning != "" {
		e.message = b.warning
	} else {
		e.message = fileLabel(b)
	}
	e.dirty = true
	return b, nil
}
func fileLabel(b *Buffer) string {
	if b.meta.Path == "" {
		return "[No Name]"
	}
	return b.meta.Path
}
func (e *Editor) switchBuffer(b *Buffer) {
	w := e.window()
	if w.buffer == b {
		return
	}
	w.buffer.recoveryCursor = w.cursor
	e.alternate = w.buffer
	w.buffer = b
	w.cursor = b.normal(b.recoveryCursor)
	w.topLine, w.topWrap, w.goal = 0, 0, -1
	w.lineOriginalRow = -1
	e.mode = normalMode
	e.reset()
	e.dirty = true
}

func (e *Editor) edit(edits []patch, after int, group uint64) bool {
	b := e.buffer()
	total := 0
	for _, buffer := range e.buffers {
		total += buffer.byteSize
	}
	for _, p := range edits {
		total += len(p.New) - len(p.Old)
	}
	if total > 64<<20 {
		e.error(fmt.Errorf("total open-buffer text exceeds 64 MiB"))
		return false
	}
	before := e.pos()
	committed, err := b.change(edits, before, after, group, e.emptyResult)
	e.emptyResult = false
	if committed {
		for _, p := range edits {
			e.adjustLocations(b, p)
		}
		e.window().cursor = clamp(after, 0, len(b.text))
		e.window().goal = -1
		if e.mode != insertMode && e.mode != replaceMode {
			e.window().cursor = b.normal(e.pos())
		}
		e.changed = true
		e.dirty = true
		loc := location{b, e.pos()}
		if len(e.changes) == 0 || e.changes[len(e.changes)-1] != loc {
			e.changes = append(e.changes, loc)
			if len(e.changes) > 1000 {
				e.changes = e.changes[1:]
			}
		}
		e.changeIndex = len(e.changes)
	}
	e.error(err)
	return committed
}
func (e *Editor) replace(at, end int, text string, group uint64) bool {
	b := e.buffer()
	at, end = clamp(at, 0, len(b.text)), clamp(end, 0, len(b.text))
	if end < at {
		at, end = end, at
	}
	return e.edit([]patch{{at, string(b.text[at:end]), text}}, at+len([]rune(text)), group)
}

func adjusted(pos int, p patch) int {
	old, new := len([]rune(p.Old)), len([]rune(p.New))
	if pos < p.At {
		return pos
	}
	if pos <= p.At+old {
		return p.At + min(pos-p.At, new)
	}
	return pos + new - old
}
func (e *Editor) adjustLocations(b *Buffer, p patch) {
	for key, loc := range e.marks {
		if loc.buffer == b {
			loc.pos = adjusted(loc.pos, p)
			e.marks[key] = loc
		}
	}
	for _, t := range e.tabs {
		walkPanes(t.root, func(w *Window) {
			if w.buffer == b && w != e.window() {
				w.cursor = b.normal(adjusted(w.cursor, p))
			}
		})
	}
}
func walkPanes(p *pane, fn func(*Window)) {
	if p == nil {
		return
	}
	if p.window != nil {
		fn(p.window)
		return
	}
	walkPanes(p.first, fn)
	walkPanes(p.second, fn)
}

func (e *Editor) addJump() {
	loc := location{e.buffer(), e.pos()}
	e.lastJump = loc
	if len(e.jumps) == 0 || e.jumps[len(e.jumps)-1] != loc {
		e.jumps = append(e.jumps, loc)
		if len(e.jumps) > 1000 {
			e.jumps = e.jumps[1:]
		}
	}
	e.jumpIndex = len(e.jumps)
}
func (e *Editor) gotoLocation(loc location, linewise bool) {
	if loc.buffer == nil {
		return
	}
	e.switchBuffer(loc.buffer)
	pos := loc.pos
	if linewise {
		pos = firstNonblank(loc.buffer, loc.buffer.row(pos))
	}
	e.setPos(pos)
}
func firstNonblank(b *Buffer, row int) int {
	at, end := b.start(row), b.end(row)
	for at < end && (b.text[at] == ' ' || b.text[at] == '\t') {
		at++
	}
	return at
}

func (e *Editor) Feed(event keyEvent) {
	if e.quit {
		return
	}
	if event.Preview != nil {
		if e.preview != nil {
			e.preview.event(e, *event.Preview)
		}
		return
	}
	if event.Clipboard != nil {
		e.finishClipboard(event.Clipboard)
		return
	}
	if read := e.clipboardRead; read != nil {
		if event.Key == "<Esc>" || event.Key == "\x03" {
			e.clipboardRead = nil
			e.message = "Clipboard read cancelled"
			e.reset()
			e.finishCommand()
			e.dirty = true
			return
		}
		if len(read.queued) >= 100000 || read.bytes+len(event.Key)+len(event.Paste) > maxBufferBytes {
			e.error(fmt.Errorf("clipboard input queue is full; Esc cancels"))
			return
		}
		read.queued = append(read.queued, event)
		read.bytes += len(event.Key) + len(event.Paste)
		return
	}
	e.dirty = true
	if len(e.tabs) == 0 {
		return
	}
	w := e.window()
	if e.mode == insertMode || e.mode == replaceMode {
		w.cursor = clamp(w.cursor, 0, len(w.buffer.text))
	} else {
		w.cursor = w.buffer.normal(w.cursor)
	}
	if e.mode == normalMode && w.lineOriginalRow != w.buffer.row(w.cursor) {
		w.lineOriginalRow = w.buffer.row(w.cursor)
		w.lineOriginal = string(w.buffer.line(w.lineOriginalRow))
	}
	if event.Err != "" {
		e.error(fmt.Errorf("%s", event.Err))
		return
	}
	if event.Mouse != nil {
		e.mouse(*event.Mouse)
		return
	}
	if event.Paste != "" || event.Key == "<Paste>" {
		if e.recording != 0 {
			e.macroKeys = append(e.macroKeys, event)
		}
		if !e.replaying {
			e.commandKeys = append(e.commandKeys, event)
		}
		e.paste(event.Paste)
		e.finishCommand()
		return
	}
	key := event.Key
	if key == "" {
		return
	}
	// ESC-b/f/d can also be a quickly typed Escape followed by a normal-mode
	// command. Only command-line mode consumes them as readline Meta keys.
	if e.prompt == "" {
		if fallback, ok := map[string]string{"<A-b>": "b", "<A-f>": "f", "<A-d>": "d", "<A-BS>": "<BS>"}[key]; ok {
			e.Feed(keyEvent{Key: "<Esc>"})
			e.Feed(keyEvent{Key: fallback})
			return
		}
	}
	if len(e.pager) > 0 {
		e.pager = nil
		return
	}
	if e.substitute != nil {
		e.confirmSubstitute(key)
		return
	}
	if e.recording != 0 && !(key == "q" && e.mode == normalMode && e.pending == "" && e.operator == "") {
		if len(e.macroKeys) < 100000 {
			e.macroKeys = append(e.macroKeys, event)
		} else {
			e.recording = 0
			e.error(fmt.Errorf("macro limit reached"))
		}
	}
	if !e.replaying {
		e.commandKeys = append(e.commandKeys, event)
	}
	if e.prompt != "" {
		if e.prompt[0] == ':' {
			e.ignoreRepeat = true
		}
		e.promptKey(key)
		e.finishCommand()
		return
	}
	if e.mode == insertMode || e.mode == replaceMode {
		e.insertKey(key)
	} else {
		e.normalKey(key)
	}
	e.finishCommand()
}

func (e *Editor) finishCommand() {
	complete := e.mode == normalMode && e.operator == "" && e.pending == "" && e.count == "" && !e.registerReady && e.prompt == "" && e.clipboardRead == nil
	if complete {
		if e.changed && !e.ignoreRepeat && !e.replaying {
			e.lastChange = append([]keyEvent(nil), e.commandKeys...)
		}
		e.commandKeys = nil
		e.changed, e.ignoreRepeat = false, false
		if e.oneNormal {
			e.oneNormal = false
			e.beginInsert("i", 1)
		}
	}
}

func (e *Editor) normalKey(key string) {
	b := e.buffer()
	w := e.window()
	if key == "<Esc>" || key == "\x03" {
		if e.visual() {
			e.rememberVisual()
		}
		e.mode = normalMode
		e.reset()
		return
	}
	if e.pending != "" {
		e.pendingKey(key)
		return
	}
	if key == "\n" && e.operator == "" && !e.visual() && texEditing(b) {
		e.jumpTexPlaceholder()
		return
	}
	if len(key) == 1 && key[0] >= '0' && key[0] <= '9' && (key != "0" || e.count != "") {
		if len(e.count) < 7 {
			e.count += key
		}
		return
	}
	n := e.number()
	e.registerReady = false
	if e.visual() {
		switch key {
		case ":":
			e.rememberVisual()
			e.mode = normalMode
			e.beginPrompt(":'<,'>")
			return
		case "v", "V", "\x16":
			kind := map[string]mode{"v": visualMode, "V": visualLineMode, "\x16": visualBlockMode}[key]
			if e.mode == kind {
				e.rememberVisual()
				e.mode = normalMode
			} else {
				e.mode = kind
			}
			e.reset()
			return
		case "o", "O":
			w.cursor, e.anchor = e.anchor, w.cursor
			e.reset()
			return
		case "d", "x", "y", "c", "s", "D", "C", "Y", ">", "<", "=", "~", "u", "U", "J":
			e.visualOperation(key)
			e.reset()
			return
		case "p", "P":
			e.withRegister(e.selectedRegister, func(reg register) { e.visualOperation("d"); e.put(reg, false, n); e.mode = normalMode })
			e.reset()
			return
		case "r":
			e.pending = "visual-r"
			return
		case "I", "A":
			e.beginBlockInsert(key)
			e.reset()
			return
		case "i", "a":
			e.pending = "object:" + key
			return
		}
	}
	if e.operator != "" {
		if key == "/" || key == "?" {
			e.beginPrompt(key)
			return
		}
		if key == "v" || key == "V" || key == "\x16" {
			e.forceMode = map[string]mode{"v": visualMode, "V": visualLineMode, "\x16": visualBlockMode}[key]
			return
		}
		if key == e.operator || (e.operator == "g~" && key == "~") || (e.operator == "gu" && key == "u") || (e.operator == "gU" && key == "U") || (e.operator == "gq" && key == "q") {
			e.applyOperator(e.operator, e.lineRange(b.row(e.pos()), n*e.operatorCount), true)
			e.reset()
			return
		}
		if key == "i" || key == "a" {
			e.pending = "object:" + key
			return
		}
	}
	switch key {
	case "\"":
		e.pending = "register"
		return
	case "d", "c", "y", ">", "<", "=":
		if e.operator == "" {
			e.operator = key
			e.operatorCount = n
			e.count = ""
			return
		}
	case "g":
		e.pending = "g"
		return
	case "[", "]":
		e.pending = "section:" + key
		return
	case "f", "F", "t", "T":
		e.pending = "find:" + key
		return
	case "m":
		e.pending = "mark"
		return
	case "'", "`":
		e.pending = "jump:" + key
		return
	}
	count := n
	if e.operator != "" {
		count = min(maxCount, count*e.operatorCount)
	}
	if target, line, inclusive, ok := e.motion(key, count); ok {
		if e.operator != "" {
			if e.operator == "c" && (key == "w" || key == "W") && e.pos() < len(b.text) && !unicode.IsSpace(b.text[e.pos()]) {
				target, _, inclusive, _ = e.motion(map[string]string{"w": "e", "W": "E"}[key], count)
			}
			e.applyMotionOperator(target, line, inclusive)
		} else {
			if key == "G" || key == "%" || key == "{" || key == "}" {
				e.addJump()
			}
			e.setPos(target)
			if key == "j" || key == "k" || key == "<Up>" || key == "<Down>" {
				w.goal = e.motionGoal
			}
		}
		e.count = ""
		return
	}
	if e.operator != "" {
		e.error(fmt.Errorf("not a motion: %s", key))
		e.reset()
		return
	}
	switch key {
	case "i", "a", "I", "A", "o", "O", "R":
		e.beginInsert(key, n)
	case "v", "V", "\x16":
		e.anchor = e.pos()
		e.mode = map[string]mode{"v": visualMode, "V": visualLineMode, "\x16": visualBlockMode}[key]
	case "x", "<Delete>":
		end := min(b.end(b.row(e.pos())), e.pos()+n)
		e.deleteRange(e.pos(), end, false, false)
	case "X":
		e.deleteRange(max(b.start(b.row(e.pos())), e.pos()-n), e.pos(), false, false)
	case "D", "C":
		target, _, _, _ := e.motion("$", n)
		e.deleteRange(e.pos(), min(len(b.text), target+1), false, key == "C")
	case "s":
		end := min(b.end(b.row(e.pos())), e.pos()+n)
		e.deleteRange(e.pos(), end, false, true)
	case "S":
		e.applyOperator("c", e.lineRange(b.row(e.pos()), n), true)
	case "Y":
		e.applyOperator("y", e.lineRange(b.row(e.pos()), n), true)
	case "p", "P":
		e.withRegister(e.selectedRegister, func(value register) { e.put(value, key == "P", n) })
	case "r":
		e.pending = "replace"
		return
	case "J":
		e.joinLines(b.row(e.pos()), max(2, n), false)
	case "~":
		end := min(b.end(b.row(e.pos())), e.pos()+n)
		e.transformRange(e.pos(), end, "~")
		e.setPos(end)
	case "u", "\x12":
		e.ignoreRepeat = true
		for i := 0; i < n; i++ {
			pos, err := b.undoChange(key == "\x12")
			if pos >= 0 {
				e.setPos(pos)
			}
			if err != nil {
				e.error(err)
				break
			}
		}
	case "U":
		if w.lineOriginalRow == b.row(e.pos()) {
			start, end := b.start(w.lineOriginalRow), b.end(w.lineOriginalRow)
			e.replace(start, end, w.lineOriginal, 0)
			e.setPos(start)
		}
	case ".":
		e.repeatChange(n, e.count != "")
	case "q":
		if e.recording != 0 {
			e.registers[e.recording] = register{Keys: append([]keyEvent(nil), e.macroKeys...), Text: keysText(e.macroKeys)}
			e.recording = 0
			e.macroKeys = nil
			e.message = "Recording stopped"
		} else {
			e.pending = "record"
			return
		}
	case "@":
		e.pending = "macro"
		return
	case "/", "?", ":":
		e.beginPrompt(key)
	case "n", "N":
		e.searchNext(key == "N", n)
	case "*", "#", "g*", "g#":
		e.searchWord(strings.Contains(key, "#"), !strings.HasPrefix(key, "g"), n)
	case "z":
		e.pending = "z"
		return
	case "Z":
		e.pending = "Z"
		return
	case "\x06", "<PageDown>":
		e.scroll(n*max(1, w.rect.h-3), true)
	case "\x02", "<PageUp>":
		e.scroll(-n*max(1, w.rect.h-3), true)
	case "\x04":
		e.scroll(n*max(1, (w.rect.h-1)/2), true)
	case "\x15":
		e.scroll(-n*max(1, (w.rect.h-1)/2), true)
	case "\x05":
		e.scroll(n, false)
	case "\x19":
		e.scroll(-n, false)
	case "\x17":
		e.pending = "window"
		return
	case "\x0f", "\t", "<Tab>":
		if len(e.jumps) > 0 {
			if key == "\x0f" {
				if e.jumpIndex == len(e.jumps) {
					e.jumps = append(e.jumps, location{b, e.pos()})
				}
				e.jumpIndex = max(0, e.jumpIndex-1)
			} else {
				e.jumpIndex = min(len(e.jumps)-1, e.jumpIndex+1)
			}
			e.gotoLocation(e.jumps[e.jumpIndex], false)
		}
	case "\x1e":
		if e.alternate != nil {
			e.switchBuffer(e.alternate)
		}
	case "\x01", "\x18":
		e.incrementNumber(n, key == "\x18")
	case "\x07":
		e.message = fmt.Sprintf("%s  %d lines  %d,%d  %s", fileLabel(b), len(b.lineStarts()), b.row(e.pos())+1, e.pos()-b.start(b.row(e.pos()))+1, b.meta.Encoding)
	case "<F5>":
		if !e.visual() && texEditing(b) {
			e.expandTexLine()
		} else {
			e.execute("Gn")
		}
	case "<C-F5>":
		e.execute("Gn")
	case "<C-L>", "\x0c":
		e.dirty, e.fullRedraw = true, true
	default:
		e.message = "Unknown/unsupported command: " + key
	}
	if e.mode != insertMode && e.mode != replaceMode {
		e.reset()
	} else {
		e.count = ""
		e.selectedRegister = '"'
	}
}

func (e *Editor) pendingKey(key string) {
	pending := e.pending
	e.pending = ""
	n := e.number()
	b := e.buffer()
	switch {
	case pending == "register":
		r := []rune(key)
		if len(r) == 1 && strings.ContainsRune("abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789\"_-+*%#.:/", r[0]) {
			e.selectedRegister = r[0]
			e.registerReady = true
		} else {
			e.reset()
		}
		return
	case pending == "mark":
		r := []rune(key)
		if len(r) == 1 && ((r[0] >= 'a' && r[0] <= 'z') || (r[0] >= 'A' && r[0] <= 'Z')) {
			e.marks[r[0]] = location{b, e.pos()}
		}
	case strings.HasPrefix(pending, "jump:"):
		r := []rune(key)
		if len(r) == 1 {
			old := location{b, e.pos()}
			if e.operator != "" {
				loc := e.marks[r[0]]
				if r[0] == '\'' || r[0] == '`' {
					loc = e.lastJump
				}
				if loc.buffer != b {
					e.error(fmt.Errorf("mark is not in this buffer"))
				} else {
					e.applyMotionOperator(clamp(loc.pos, 0, len(b.text)), strings.HasSuffix(pending, "'"), false)
				}
				e.reset()
				return
			}
			if r[0] == '\'' || r[0] == '`' {
				e.gotoLocation(e.lastJump, strings.HasSuffix(pending, "'"))
			} else {
				e.gotoLocation(e.marks[r[0]], strings.HasSuffix(pending, "'"))
			}
			e.lastJump = old
		}
	case strings.HasPrefix(pending, "find:"):
		r := []rune(key)
		if len(r) == 1 {
			e.findKey, e.findRune = strings.TrimPrefix(pending, "find:"), r[0]
			count := n
			if e.operator != "" {
				count *= e.operatorCount
			}
			target, ok := e.findCharacter(e.findKey, e.findRune, count)
			if ok {
				if e.operator != "" {
					e.applyMotionOperator(target, false, e.findKey == "f" || e.findKey == "t")
				} else {
					e.setPos(target)
				}
			}
		}
	case strings.HasPrefix(pending, "object:"):
		count := n
		if e.operator != "" {
			count *= e.operatorCount
		}
		start, end, line, ok := e.textObject(key, strings.HasSuffix(pending, "a"), count)
		if ok {
			if e.visual() {
				e.anchor = start
				e.setPos(max(start, end-1))
				if line {
					e.mode = visualLineMode
				}
			} else {
				e.applyOperator(e.operator, span{start, end}, line)
			}
		} else {
			e.message = "Text object not found"
		}
	case pending == "g":
		if key == "~" || key == "u" || key == "U" || key == "q" {
			if e.visual() {
				if key == "q" {
					region, _ := e.visualSpan()
					e.formatLines(b.row(region.start), b.row(max(region.start, region.end-1)))
					e.mode = normalMode
				} else {
					e.visualOperation(key)
				}
				e.reset()
				return
			}
			e.operator = "g" + key
			e.operatorCount = n
			e.count = ""
			return
		}
		if key == "v" {
			v := e.lastVisual
			if v.buffer != nil {
				e.switchBuffer(v.buffer)
				e.mode = v.kind
				e.anchor = v.anchor
				e.setPos(v.cursor)
			}
		} else if key == "J" {
			e.joinLines(b.row(e.pos()), max(2, n), true)
		} else if key == ";" || key == "," {
			if len(e.changes) > 0 {
				delta := -n
				if key == "," {
					delta = n
				}
				e.changeIndex = clamp(e.changeIndex+delta, 0, len(e.changes)-1)
				e.gotoLocation(e.changes[e.changeIndex], false)
			}
		} else if key == "f" {
			e.openWordFile()
		} else if key == "t" || key == "T" {
			e.activeTab = (e.activeTab + map[bool]int{true: len(e.tabs) - 1, false: 1}[key == "T"]) % len(e.tabs)
		} else {
			if e.operator != "" && (key == "p" || key == "P") {
				e.error(fmt.Errorf("not a motion"))
				e.reset()
				return
			}
			e.normalKey("g" + key)
			return
		}
	case pending == "replace" || pending == "visual-r":
		r := []rune(key)
		if key == "<Enter>" {
			r = []rune{'\n'}
		}
		if len(r) == 1 {
			if pending == "visual-r" {
				e.fillVisual(r[0])
			} else {
				end := min(b.end(b.row(e.pos())), e.pos()+n)
				if end-e.pos() < n {
					e.message = "Not enough characters"
				} else {
					start := e.pos()
					e.replace(start, end, strings.Repeat(string(r[0]), n), 0)
					e.setPos(start + n - 1)
				}
			}
		}
	case pending == "record":
		r := []rune(key)
		if len(r) == 1 && unicode.IsLetter(r[0]) {
			e.recording = unicode.ToLower(r[0])
			e.macroKeys = nil
			e.message = "recording @" + key
		}
	case pending == "macro":
		r := []rune(key)
		if len(r) == 1 {
			reg := r[0]
			if reg == '@' {
				reg = e.lastMacro
			}
			e.lastMacro = reg
			e.playMacro(reg, n)
		}
	case pending == "window":
		e.windowCommand(key, n)
	case strings.HasPrefix(pending, "section:"):
		e.normalKey(strings.TrimPrefix(pending, "section:") + key)
		return
	case pending == "z":
		row := b.row(e.pos())
		switch key {
		case "t", "<Enter>":
			e.window().topLine = row
		case "z", ".":
			e.window().topLine = max(0, row-max(1, e.window().rect.h-1)/2)
		case "b", "-":
			e.window().topLine = max(0, row-max(1, e.window().rect.h-2))
		}
		e.window().topWrap = 0
	case pending == "Z":
		if key == "Z" {
			e.execute("x")
		} else if key == "Q" {
			e.execute("q!")
		}
	}
	if e.mode != insertMode && e.mode != replaceMode {
		e.reset()
	}
}

func (e *Editor) rememberVisual() {
	e.lastVisual = visualSelection{e.buffer(), e.anchor, e.pos(), e.mode}
	e.marks['<'] = location{e.buffer(), min(e.anchor, e.pos())}
	e.marks['>'] = location{e.buffer(), max(e.anchor, e.pos())}
}

func (e *Editor) repeatChange(n int, override bool) {
	if len(e.lastChange) == 0 {
		e.message = "No change to repeat"
		return
	}
	keys := append([]keyEvent(nil), e.lastChange...)
	if override {
		at := 0
		for at < len(keys) && len(keys[at].Key) == 1 && keys[at].Key >= "0" && keys[at].Key <= "9" {
			at++
		}
		prefix := eventsText(strconv.Itoa(n))
		keys = append(prefix, keys[at:]...)
	}
	old := e.replaying
	e.replaying = true
	defer func() { e.replaying = old }()
	for _, key := range keys {
		e.Feed(key)
	}
}

func (e *Editor) playMacro(reg rune, count int) {
	e.withRegister(reg, func(value register) { e.playRegisterMacro(value, count) })
}
func (e *Editor) playRegisterMacro(value register, count int) {
	if e.macroDepth >= 20 {
		e.error(fmt.Errorf("macro recursion limit"))
		return
	}
	keys := value.Keys
	if len(keys) == 0 {
		keys = eventsText(value.Text)
	}
	if len(keys) == 0 {
		e.error(fmt.Errorf("empty macro register"))
		return
	}
	if e.macroDepth == 0 {
		e.steps = 0
	}
	e.macroDepth++
	defer func() { e.macroDepth-- }()
	e.reset()
	for repeat := 0; repeat < count; repeat++ {
		for _, event := range keys {
			e.steps++
			if e.steps > 100000 {
				e.error(fmt.Errorf("macro execution limit"))
				return
			}
			e.Feed(event)
			if e.quit {
				return
			}
		}
	}
}

func (e *Editor) tick(now time.Time) {
	if read := e.clipboardRead; read != nil && !now.Before(read.deadline) {
		e.finishClipboard(&clipboardReply{selection: read.selection, err: fmt.Errorf("terminal clipboard query timed out or was denied; internal registers unchanged")})
	}
	e.pollSyntax()
	for _, b := range e.buffers {
		if b.journal != nil {
			e.error(b.journal.Tick(now))
		}
	}
}
func (e *Editor) close(normalExit bool) error {
	e.syntax.close()
	var first error
	for _, b := range e.buffers {
		if b.journal != nil {
			err := b.journal.Close(normalExit && (e.discard || !b.dirty()))
			if first == nil {
				first = err
			}
		}
	}
	if e.clip != nil {
		if err := e.clip.Close(); first == nil {
			first = err
		}
	}
	if err := e.preview.close(); first == nil {
		first = err
	}
	return first
}

func (e *Editor) openWordFile() {
	b := e.buffer()
	at, end := e.pos(), e.pos()
	valid := func(r rune) bool { return !unicode.IsSpace(r) && !strings.ContainsRune("\"'<>[](){}", r) }
	for at > 0 && valid(b.text[at-1]) {
		at--
	}
	for end < len(b.text) && valid(b.text[end]) {
		end++
	}
	if at == end {
		return
	}
	name := string(b.text[at:end])
	if !filepath.IsAbs(name) && b.meta.Path != "" {
		name = filepath.Join(filepath.Dir(b.meta.Path), name)
	}
	if b.dirty() {
		e.error(fmt.Errorf("unsaved changes"))
		return
	}
	_, err := e.open(name, false)
	e.error(err)
}

func (e *Editor) bindPath(path string) error {
	b := e.buffer()
	name, err := canonicalPath(path)
	if err != nil {
		return err
	}
	if name == b.meta.Path {
		return nil
	}
	for _, other := range e.buffers {
		if other != b && other.meta.Path == name {
			return fmt.Errorf("file is already open in another buffer")
		}
	}
	copy := *b
	copy.meta = b.meta
	copy.meta.Path = name
	copy.meta.Existed = false
	copy.meta.DiskHash = ""
	copy.journal = nil
	// A new name has not been written yet, even if this text matched the old
	// file. Keep edit versions/history intact and reserve an unsaved sentinel.
	copy.savedVersion = ^uint64(0)
	if data, err := readBounded(name, maxBufferBytes*4); err == nil {
		copy.meta.DiskHash = digest(data)
	} else if !os.IsNotExist(err) {
		return err
	}
	if err := attachJournal(&copy, e.options, false); err != nil {
		return err
	}
	old := b.journal
	*b = copy
	if old != nil {
		if err := old.Close(true); err != nil {
			e.message = "New swap created; old swap retained: " + err.Error()
		}
	}
	return nil
}
