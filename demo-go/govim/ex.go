package main

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

func (e *Editor) address(value string) (row int, rest string, ok bool) {
	b := e.buffer()
	rest = strings.TrimSpace(value)
	row = b.row(e.pos())
	if rest == "" {
		return row, rest, false
	}
	switch rest[0] {
	case '.':
		rest = rest[1:]
		ok = true
	case '$':
		row = len(b.lineStarts()) - 1
		rest = rest[1:]
		ok = true
	case '\'':
		if len(rest) < 2 {
			return row, rest, false
		}
		mark := e.marks[rune(rest[1])]
		if mark.buffer != b {
			return row, rest, false
		}
		row = b.row(mark.pos)
		rest = rest[2:]
		ok = true
	default:
		at := 0
		for at < len(rest) && rest[at] >= '0' && rest[at] <= '9' {
			at++
		}
		if at > 0 {
			n, _ := strconv.Atoi(rest[:at])
			row = n - 1
			rest = rest[at:]
			ok = true
		}
	}
	for len(rest) > 0 && (rest[0] == '+' || rest[0] == '-') {
		sign := 1
		if rest[0] == '-' {
			sign = -1
		}
		rest = rest[1:]
		at := 0
		for at < len(rest) && rest[at] >= '0' && rest[at] <= '9' {
			at++
		}
		n := 1
		if at > 0 {
			n, _ = strconv.Atoi(rest[:at])
			rest = rest[at:]
		}
		row += sign * min(n, maxCount)
		ok = true
	}
	return row, strings.TrimSpace(rest), ok
}

func (e *Editor) exRange(command string) (first, last int, rest string, explicit bool) {
	current := e.buffer().row(e.pos())
	first, last = current, current
	rest = strings.TrimSpace(command)
	if strings.HasPrefix(rest, "%") {
		return 0, len(e.buffer().lineStarts()) - 1, strings.TrimSpace(rest[1:]), true
	}
	if row, next, ok := e.address(rest); ok {
		first, last, rest, explicit = row, row, next, true
	}
	if len(rest) > 0 && (rest[0] == ',' || rest[0] == ';') {
		rest = strings.TrimSpace(rest[1:])
		if row, next, ok := e.address(rest); ok {
			last, rest, explicit = row, next, true
		}
	}
	return
}

func (e *Editor) execute(command string) {
	command = strings.TrimSpace(command)
	if command == "" {
		return
	}
	e.lastEx = command
	e.dirty = true
	first, last, rest, explicit := e.exRange(command)
	b := e.buffer()
	lineCount := len(b.lineStarts())
	if rest == "" {
		e.addJump()
		e.setPos(firstNonblank(b, clamp(last, 0, lineCount-1)))
		return
	}
	// Commands which own a separator (substitute, global and shell) parse it
	// themselves. Other Ex commands may be chained with an unescaped bar.
	ownsDelimiter := len(rest) > 1 && (rest[0] == 's' || rest[0] == 'g' || rest[0] == 'v') && !isASCIIAlpha(rest[1]) && rest[1] != ' '
	if !ownsDelimiter && !strings.HasPrefix(rest, "substitute") && !strings.HasPrefix(rest, "global") && !strings.HasPrefix(rest, "vglobal") && !strings.HasPrefix(rest, "!") {
		if at := unescapedBar(rest); at >= 0 {
			e.execute(command[:len(command)-len(rest)+at])
			if !e.quit {
				e.execute(rest[at+1:])
			}
			return
		}
	}
	if first > last {
		first, last = last, first
	}
	if first < 0 || last >= lineCount {
		if !(first == -1 && last == -1 && strings.HasPrefix(rest, "r")) {
			e.error(fmt.Errorf("invalid line range"))
			return
		}
	}
	if strings.HasPrefix(rest, "s") && len(rest) > 1 && !isASCIIAlpha(rest[1]) {
		e.substituteRange(first, last, rest[1:])
		return
	}
	at := 0
	for at < len(rest) && (isASCIIAlpha(rest[at]) || rest[at] == '>' || rest[at] == '<' || rest[at] == '=') {
		at++
	}
	name := rest[:at]
	args := strings.TrimSpace(rest[at:])
	force := strings.HasPrefix(args, "!")
	if force {
		args = strings.TrimSpace(args[1:])
	}
	if name == "" && strings.HasPrefix(rest, "!") {
		if e.runCommand == nil {
			e.error(fmt.Errorf("shell is unavailable"))
		} else {
			e.error(e.runCommand(strings.TrimSpace(rest[1:])))
		}
		return
	}
	switch name {
	case "AIConnect", "AINext", "AIList", "AIView", "AIEdit", "AIIns", "AIDismiss", "AIRefine", "AIRetry", "AIReasoning", "AICancel", "AIBack":
		e.aiCommand(name, args)
	case "w", "write", "wnomove", "wq", "x", "xit", "exit", "saveas", "sav":
		noMove := name == "wnomove"
		if args != "" {
			path, err := exPath(args)
			if err != nil {
				e.error(err)
				return
			}
			if name == "saveas" || name == "sav" || b.meta.Path == "" {
				if _, err := os.Stat(path); err == nil && !force {
					e.error(fmt.Errorf("file exists (use ! to overwrite)"))
					return
				}
				if err := e.bindPath(path); err != nil {
					e.error(err)
					return
				}
			} else {
				canonical, err := canonicalPath(path)
				if err != nil {
					e.error(err)
					return
				}
				if canonical == b.meta.Path && !explicit {
					if err := e.saveBuffer(b, force, noMove); err != nil {
						e.error(err)
						return
					}
					e.message = ""
					if name == "wq" || name == "x" || name == "xit" || name == "exit" {
						e.quitWindow(force)
					}
					return
				}
				e.error(e.export(path, first, last, explicit, force, noMove))
				return
			}
		}
		if (name == "x" || name == "xit" || name == "exit") && !b.dirty() && b.meta.Existed {
			e.quitWindow(force)
			return
		}
		if explicit {
			e.error(e.export(b.meta.Path, first, last, true, force, noMove))
			return
		}
		if err := e.saveBuffer(b, force, noMove); err != nil {
			e.error(err)
			return
		}
		e.message = ""
		if name == "wq" || name == "x" || name == "xit" || name == "exit" {
			e.quitWindow(force)
		}
	case "q", "quit":
		e.quitWindow(force)
	case "qa", "qall":
		if !force {
			for _, buffer := range e.buffers {
				if buffer.dirty() {
					e.error(fmt.Errorf("unsaved changes in %s", fileLabel(buffer)))
					return
				}
			}
		}
		e.discard = force
		e.quit = true
	case "wa", "wall", "wqa", "wqall":
		for _, buffer := range e.buffers {
			if buffer.dirty() {
				if err := e.saveBuffer(buffer, force, false); err != nil {
					e.error(err)
					return
				}
			}
		}
		e.message = ""
		if name == "wqa" || name == "wqall" {
			e.quit = true
		}
	case "e", "edit", "enew":
		if b.dirty() && !force {
			e.error(fmt.Errorf("unsaved changes (use :w or :e!)"))
			return
		}
		if name == "enew" {
			_, err := e.open("", false)
			e.error(err)
			return
		}
		path := args
		if path == "" {
			path = b.meta.Path
		}
		path, err := exPath(path)
		if err != nil {
			e.error(err)
			return
		}
		canonical, err := canonicalPath(path)
		if err != nil {
			e.error(err)
			return
		}
		if canonical == b.meta.Path {
			e.error(e.reload(force))
			return
		}
		_, err = e.open(path, false)
		e.error(err)
	case "n", "next", "prev", "previous", "N", "Next", "first", "rewind", "last":
		if b.dirty() && !force {
			e.error(fmt.Errorf("unsaved changes"))
			return
		}
		index := e.argument + 1
		switch name {
		case "prev", "previous", "N", "Next":
			index = e.argument - 1
		case "first", "rewind":
			index = 0
		case "last":
			index = len(e.arguments) - 1
		}
		if index < 0 || index >= len(e.arguments) {
			e.error(fmt.Errorf("no more files"))
			return
		}
		if _, err := e.open(e.arguments[index], false); err != nil {
			e.error(err)
		} else {
			e.argument = index
		}
	case "args", "ar":
		e.pager = append([]string{"Arguments:"}, e.arguments...)
	case "argadd":
		paths, err := filepath.Glob(args)
		if err != nil {
			e.error(err)
			return
		}
		if len(paths) == 0 {
			paths = []string{args}
		}
		e.arguments = append(e.arguments, paths...)
	case "ls", "buffers", "files":
		e.listBuffers()
	case "b", "buffer":
		if args == "" {
			e.listBuffers()
			return
		}
		if b.dirty() && !force {
			e.error(fmt.Errorf("unsaved changes"))
			return
		}
		if args == "#" && e.alternate != nil {
			e.switchBuffer(e.alternate)
			return
		}
		if n, err := strconv.Atoi(args); err == nil && n > 0 && n <= len(e.buffers) {
			e.switchBuffer(e.buffers[n-1])
			return
		}
		var found *Buffer
		for _, candidate := range e.buffers {
			if strings.Contains(candidate.meta.Path, args) {
				if found != nil {
					e.error(fmt.Errorf("ambiguous buffer name"))
					return
				}
				found = candidate
			}
		}
		if found == nil {
			e.error(fmt.Errorf("buffer not found"))
		} else {
			e.switchBuffer(found)
		}
	case "bn", "bnext", "bp", "bprevious":
		if b.dirty() && !force {
			e.error(fmt.Errorf("unsaved changes"))
			return
		}
		index := 0
		for i, candidate := range e.buffers {
			if candidate == b {
				index = i
			}
		}
		delta := 1
		if name == "bp" || name == "bprevious" {
			delta = -1
		}
		e.switchBuffer(e.buffers[(index+delta+len(e.buffers))%len(e.buffers)])
	case "bd", "bdelete":
		e.deleteBuffer(force)
	case "sp", "split", "vs", "vsplit", "new", "vnew":
		vertical := name == "vs" || name == "vsplit" || name == "vnew"
		if err := e.splitWindow(vertical); err != nil {
			e.error(err)
			return
		}
		if args != "" {
			path, err := exPath(args)
			if err != nil {
				e.error(err)
				return
			}
			_, err = e.open(path, false)
			e.error(err)
		} else if name == "new" || name == "vnew" {
			_, err := e.open("", false)
			e.error(err)
		}
	case "close", "clo":
		e.quitWindow(force)
	case "only", "on":
		e.onlyWindow(force)
	case "tabnew", "tabe", "tabedit":
		if len(e.tabs) >= 32 {
			e.error(fmt.Errorf("tab limit reached"))
			return
		}
		w := &Window{buffer: b, goal: -1, lineOriginalRow: -1}
		e.tabs = append(e.tabs, &tab{root: &pane{window: w}, active: w})
		e.activeTab = len(e.tabs) - 1
		if args != "" {
			path, err := exPath(args)
			if err != nil {
				e.error(err)
				return
			}
			_, err = e.open(path, false)
			e.error(err)
		} else {
			_, err := e.open("", false)
			e.error(err)
		}
	case "tabnext", "tabn":
		e.activeTab = (e.activeTab + 1) % len(e.tabs)
	case "tabprevious", "tabp":
		e.activeTab = (e.activeTab + len(e.tabs) - 1) % len(e.tabs)
	case "tabclose":
		e.closeTab(force)
	case "d", "delete", "D", "y", "yank", "Y":
		e.selectedRegister = '"'
		if r := []rune(args); len(r) == 1 {
			e.selectedRegister = r[0]
		}
		op := "d"
		if name == "y" || name == "yank" || name == "Y" {
			op = "y"
		}
		e.applyOperator(op, e.lineRange(first, last-first+1), true)
		e.reset()
	case "pu", "put":
		reg := '"'
		if r := []rune(args); len(r) == 1 {
			reg = r[0]
		}
		e.withRegister(reg, func(value register) {
			value.Line = true
			if !strings.HasSuffix(value.Text, "\n") {
				value.Text += "\n"
			}
			e.setPos(b.start(last))
			e.put(value, force, 1)
		})
	case "j", "join":
		count := last - first + 1
		if !explicit {
			count = 2
		}
		e.joinLines(first, count, force)
	case ">", "<", "=":
		e.indentLines(first, last, name, 1)
	case "t", "copy", "co", "m", "move":
		e.copyMoveLines(first, last, args, name == "m" || name == "move")
	case "sort":
		lines := strings.Split(string(b.text[b.start(first):b.end(last)]), "\n")
		sort.SliceStable(lines, func(i, j int) bool {
			left, right := lines[i], lines[j]
			if strings.Contains(args, "i") {
				left, right = strings.ToLower(left), strings.ToLower(right)
			}
			if strings.Contains(args, "n") {
				a, _ := strconv.ParseFloat(strings.TrimSpace(left), 64)
				z, _ := strconv.ParseFloat(strings.TrimSpace(right), 64)
				if a != z {
					if force {
						return a > z
					}
					return a < z
				}
			}
			if force {
				return left > right
			}
			return left < right
		})
		if strings.Contains(args, "u") {
			unique := lines[:0]
			for _, line := range lines {
				if len(unique) == 0 || unique[len(unique)-1] != line {
					unique = append(unique, line)
				}
			}
			lines = unique
		}
		e.replace(b.start(first), b.end(last), strings.Join(lines, "\n"), 0)
		e.setPos(b.start(first))
	case "retab":
		var edits []patch
		for row := last; row >= first; row-- {
			line := string(b.line(row))
			edits = append(edits, patch{b.start(row), line, string(expandTabs([]rune(line)))})
		}
		e.edit(edits, e.pos(), 0)
	case "r", "read":
		path, err := exPath(args)
		if err != nil {
			e.error(err)
			return
		}
		imported, err := loadBuffer(path)
		if err != nil {
			e.error(err)
			return
		}
		if !imported.meta.Existed {
			e.error(fmt.Errorf("file does not exist"))
			return
		}
		if imported.placeholder {
			return
		}
		text := string(imported.text)
		at := 0
		if last >= 0 {
			at = b.end(last)
			text = "\n" + text
		} else {
			text += "\n"
		}
		e.replace(at, at, text, 0)
		e.setPos(at)
	case "s", "substitute":
		if args == "" && e.lastSubstitute != "" {
			e.substituteRange(first, last, e.lastSubstitute)
		} else {
			e.substituteRange(first, last, args)
		}
	case "g", "global", "v", "vglobal":
		e.globalCommand(first, last, args, name == "v" || name == "vglobal", explicit)
	case "norm", "normal":
		keys := parseKeyNotation(args)
		if len(keys) > 100000 {
			e.error(fmt.Errorf("normal command too long"))
			return
		}
		old := e.replaying
		e.replaying = true
		defer func() { e.replaying = old }()
		for row := first; row <= last && row < len(b.lineStarts()); row++ {
			e.setPos(b.start(row))
			for _, key := range keys {
				e.Feed(key)
				if e.quit {
					return
				}
			}
			if e.mode == insertMode || e.mode == replaceMode {
				e.finishInsert()
			}
		}
	case "u", "undo", "red", "redo":
		pos, err := b.undoChange(name == "redo" || name == "red")
		if pos >= 0 {
			e.setPos(pos)
		}
		e.error(err)
	case "noh", "nohlsearch":
		e.search.highlight = false
	case "preserve", "pre":
		if b.journal != nil {
			if err := b.journal.Sync(); err != nil {
				e.error(err)
			} else {
				e.message = "All changes flushed to " + b.journal.path
			}
		}
	case "swapname", "sw":
		if b.journal != nil {
			e.message = b.journal.path
		}
	case "recover", "rec":
		if args == "" {
			e.message = "This session's journal is active; use govim -r FILE after a crash"
		} else if b.dirty() && !force {
			e.error(fmt.Errorf("unsaved changes"))
		} else {
			path, err := exPath(args)
			if err != nil {
				e.error(err)
				return
			}
			_, err = e.open(path, true)
			e.error(err)
		}
	case "set", "se":
		e.setPreferred(args)
	case "marks":
		e.pager = []string{"Marks:"}
		for key, loc := range e.marks {
			e.pager = append(e.pager, fmt.Sprintf("%c  %d  %s", key, loc.buffer.row(loc.pos)+1, fileLabel(loc.buffer)))
		}
	case "registers", "reg", "display":
		e.pager = []string{"Registers:"}
		for key, value := range e.registers {
			e.pager = append(e.pager, fmt.Sprintf("\"%c  %s", key, value.Text))
		}
	case "jumps", "changes":
		values := e.jumps
		if name == "changes" {
			values = e.changes
		}
		e.pager = []string{name + ":"}
		for i, loc := range values {
			e.pager = append(e.pager, fmt.Sprintf("%d  %d  %s", i, loc.buffer.row(loc.pos)+1, fileLabel(loc.buffer)))
		}
	case "f", "file":
		if args != "" {
			path, err := exPath(args)
			if err != nil {
				e.error(err)
			} else {
				e.error(e.bindPath(path))
			}
		} else {
			e.message = fileLabel(b)
		}
	case "cd", "chdir":
		path, err := exPath(args)
		if err != nil {
			e.error(err)
		} else {
			e.error(os.Chdir(path))
		}
	case "pwd":
		directory, _ := os.Getwd()
		e.message = directory
	case "help", "h":
		e.pager = strings.Split(helpText, "\n")
	case "version":
		e.message = "govim 0.1 — native Go modal editor; no Vimscript"
	case "Gn":
		if b.dirty() {
			if err := e.saveBuffer(b, false, false); err != nil {
				e.error(err)
				return
			}
		}
		path := args
		if path == "" {
			path = b.meta.Path
		}
		if e.compile == nil {
			e.error(fmt.Errorf("TeX compilation unavailable"))
		} else {
			e.error(e.compile(path, force))
		}
	case "toggle":
		if explicit || args != "" {
			e.error(fmt.Errorf("usage: :toggle[!] (no range or arguments)"))
			return
		}
		e.error(e.preview.toggle(e, force))
	case "Preview", "VimtexPreview", "PreviewScroll", "VimtexScroll":
		if e.preview != nil {
			e.preview.observe(e)
		}
		if e.preview == nil || !e.preview.ready || !e.preview.published {
			e.error(fmt.Errorf("preview is not ready (requires st-go)"))
			return
		}
		kind, value := "seek", b.row(e.pos())+1
		if name == "PreviewScroll" || name == "VimtexScroll" {
			kind, value = "scroll", 1
		}
		if args != "" {
			n, err := strconv.Atoi(args)
			if err != nil || n < -maxCount || n > maxCount || kind == "seek" && n < 1 {
				e.error(fmt.Errorf("invalid preview navigation value"))
				return
			}
			value = n
		}
		e.preview.event(e, previewEvent{kind: kind, value: value})
	default:
		e.error(fmt.Errorf("unsupported Ex command: %s", rest))
	}
}

func isASCIIAlpha(c byte) bool { return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' }
func unescapedBar(s string) int {
	quoted := byte(0)
	for i := 0; i < len(s); i++ {
		if s[i] == '\\' {
			i++
			continue
		}
		if s[i] == '\'' || s[i] == '"' {
			if quoted == 0 {
				quoted = s[i]
			} else if quoted == s[i] {
				quoted = 0
			}
			continue
		}
		if s[i] == '|' && quoted == 0 {
			return i
		}
	}
	return -1
}
func exPath(value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", fmt.Errorf("file name required")
	}
	if len(value) > 1 && ((value[0] == '\'' && value[len(value)-1] == '\'') || (value[0] == '"' && value[len(value)-1] == '"')) {
		value = value[1 : len(value)-1]
	}
	var out strings.Builder
	for i := 0; i < len(value); i++ {
		if value[i] == '\\' && i+1 < len(value) {
			i++
		}
		out.WriteByte(value[i])
	}
	value = out.String()
	if value == "~" || strings.HasPrefix(value, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		value = filepath.Join(home, strings.TrimPrefix(value, "~/"))
	}
	return value, nil
}
func (e *Editor) export(path string, first, last int, ranged, force bool, noMove ...bool) error {
	name, err := canonicalPath(path)
	if err != nil {
		return err
	}
	if _, err := os.Stat(name); err == nil && !force {
		return fmt.Errorf("file exists (use ! to overwrite)")
	}
	b := e.buffer()
	if b.journal != nil {
		if err := b.journal.Sync(); err != nil {
			return err
		}
	}
	data, err := b.encoded()
	if err != nil {
		return err
	}
	if ranged {
		copy := *b
		copy.text = append([]rune(nil), b.text[b.start(first):b.end(last)]...)
		copy.meta.FinalEOL = true
		data, err = copy.encoded()
		if err != nil {
			return err
		}
	}
	_, err = atomicWrite(name, data, 0600, !force)
	if err == nil {
		e.message = "Exported at " + time.Now().Format("2006-01-02 15:04:05 MST")
		if e.preview != nil {
			e.preview.saved(e, name, nil, len(noMove) > 0 && noMove[0])
		}
	}
	return err
}

func (e *Editor) reload(force bool) error {
	b := e.buffer()
	if b.dirty() && !force {
		return fmt.Errorf("unsaved changes")
	}
	fresh, err := loadBuffer(b.meta.Path)
	if err != nil {
		return err
	}
	// Record reload as a normal, undoable text edit before changing metadata.
	if !e.replace(0, len(b.text), string(fresh.text), 0) && string(fresh.text) != string(b.text) {
		return fmt.Errorf("reload failed")
	}
	b.meta = fresh.meta
	b.savedAt = fresh.savedAt
	b.savedVersion = b.version
	committed, err := b.append(journalRecord{Kind: "saved", Version: b.version, Meta: &b.meta})
	if !committed {
		return err
	}
	if err == nil && b.journal != nil {
		err = b.journal.Sync()
	}
	e.setPos(0)
	if err == nil {
		e.message = ""
	}
	if err == nil && e.preview != nil {
		text := previewText(b)
		e.preview.saved(e, b.meta.Path, &text, false)
	}
	return err
}
func (e *Editor) listBuffers() {
	e.pager = []string{"Buffers:"}
	for i, b := range e.buffers {
		flag := " "
		if b == e.buffer() {
			flag = "%"
		}
		modified := " "
		if b.dirty() {
			modified = "+"
		}
		e.pager = append(e.pager, fmt.Sprintf("%d %s%s %s", i+1, flag, modified, fileLabel(b)))
	}
}
func (e *Editor) setPreferred(args string) {
	if args == "" {
		e.pager = []string{"Fixed preferences: number ruler cursorline hlsearch autoindent smartindent", "tabstop=4 shiftwidth=4 expandtab ambiwidth=double mouse=a", "fileencodings=utf-8,gb18030 colorscheme=desert"}
		return
	}
	allowed := map[string]bool{"nu": true, "number": true, "ruler": true, "cursorline": true, "hlsearch": true, "nocompatible": true, "backspace=indent,eol,start": true, "mouse=a": true, "ttymouse=xterm2": true, "fileencodings=utf-8,gb18030": true, "enc=utf-8": true, "encoding=utf-8": true, "ambiwidth=double": true, "autoindent": true, "ai": true, "smartindent": true, "si": true, "shiftwidth=4": true, "sw=4": true, "ts=4": true, "tabstop=4": true, "expandtab": true, "et": true, "shellslash": true}
	for _, option := range strings.Fields(args) {
		if !allowed[option] {
			e.error(fmt.Errorf("option not implemented: %s (govim uses the fixed vimrc0 preferences)", option))
			return
		}
	}
	e.message = "Preferred options active"
}

func (e *Editor) copyMoveLines(first, last int, args string, move bool) {
	b := e.buffer()
	target, rest, ok := e.address(args)
	if !ok || rest != "" || target < -1 || target >= len(b.lineStarts()) {
		e.error(fmt.Errorf("invalid destination"))
		return
	}
	if move && target >= first && target <= last {
		e.error(fmt.Errorf("destination is inside moved range"))
		return
	}
	lines := strings.Split(string(b.text), "\n")
	selected := append([]string(nil), lines[first:last+1]...)
	if move {
		lines = append(lines[:first], lines[last+1:]...)
		if target > last {
			target -= last - first + 1
		}
	}
	index := target + 1
	result := append([]string(nil), lines[:index]...)
	result = append(result, selected...)
	result = append(result, lines[index:]...)
	e.replace(0, len(b.text), strings.Join(result, "\n"), 0)
	e.setPos(b.start(index))
}
func (e *Editor) globalCommand(first, last int, args string, invert, explicit bool) {
	if args == "" {
		e.error(fmt.Errorf("global requires /pattern/command"))
		return
	}
	delimiter := args[0]
	end := 1
	for end < len(args) {
		if args[end] == '\\' {
			end += 2
			continue
		}
		if args[end] == delimiter {
			break
		}
		end++
	}
	if end >= len(args) {
		e.error(fmt.Errorf("incomplete global pattern"))
		return
	}
	pattern, command := args[1:end], strings.TrimSpace(args[end+1:])
	re, err := compilePattern(pattern)
	if err != nil {
		e.error(err)
		return
	}
	if !explicit {
		first, last = 0, len(e.buffer().lineStarts())-1
	}
	if command != "d" && command != "delete" && command != "p" && command != "print" {
		e.error(fmt.Errorf("global supports delete and print commands"))
		return
	}
	b := e.buffer()
	var rows []int
	for row := first; row <= last; row++ {
		if re.MatchString(string(b.line(row))) != invert {
			rows = append(rows, row)
		}
	}
	if command == "p" || command == "print" {
		for _, row := range rows {
			e.pager = append(e.pager, fmt.Sprintf("%d %s", row+1, string(b.line(row))))
		}
		return
	}
	group := b.group()
	for i := len(rows) - 1; i >= 0; i-- {
		row := rows[i]
		start, end := b.start(row), b.end(row)
		if end < len(b.text) {
			end++
		} else if start > 0 {
			start--
		}
		if !e.edit([]patch{{start, string(b.text[start:end]), ""}}, start, group) {
			break
		}
	}
}
