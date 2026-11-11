package main

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"st-go/tui/readline"
	"strings"
	"time"
)

// modalPrompt is the shared modal input loop for the '/' and ':' prompts.
func (b *Browser) modalPrompt(mode string, init []byte) {
	b.clearWebPreview()
	b.promptMode = mode
	b.promptActive = true
	b.promptAbort = false
	b.promptBuffer = append([]byte{}, init...)
	b.promptCursor = len(b.promptBuffer)
	b.promptEditor = readline.New(string(init), 0, readline.DefaultLimit)
	b.promptEditor.SetHistory(b.promptHistories[mode])
	b.pathMatchIdx = -1
	b.pathMatchTop = 0
	b.pathMessage = ""
	b.cmdMessage = ""
	b.cmdAffected = nil
	b.cmdNewNames = nil
	b.cmdPrevAffected = nil
	b.refreshPromptMatches()
	b.drawPrompt()
	for b.promptActive && !b.promptAbort {
		b.processPendingSignals()
		if !b.running {
			b.promptActive = false
			break
		}
		if b.resized {
			b.resized = false
			b.renderAll()
			if b.compact {
				b.promptAbort = true
				break
			}
			b.drawPrompt()
		}
		key, st := b.in.readByte(100 * time.Millisecond)
		switch st {
		case inTimeout:
			if b.promptEditor != nil {
				result := b.promptEditor.Poll()
				if result.Handled || result.Err != nil {
					b.applyPromptResult(result)
					if b.promptActive && !b.promptAbort && result.DisplayChanged {
						b.drawPrompt()
					}
				}
			}
			continue
		case inEOF:
			b.promptActive = false
			b.running = false
			break
		case inData:
			b.handlePromptKey(key)
		}
		if b.promptActive && !b.promptAbort {
			b.drawPrompt()
		}
	}
	if b.promptAbort {
		b.promptActive = false
		b.status = "Entry cancelled"
		b.renderAll()
	}
	if b.promptEditor != nil {
		b.promptEditor.Close()
	}
}

func (b *Browser) drawPrompt() {
	if b.promptMode == "path" {
		b.drawPathPrompt()
	} else {
		b.drawCommandPrompt()
	}
}

func (b *Browser) handlePromptKey(k byte) {
	switch k {
	case 0, '\r', '\n':
		b.promptEdit("<Enter>")
	case '\x7f', '\b':
		b.promptBackspace()
	case 0x01: // Ctrl+A
		b.promptEdit("<Home>")
	case 0x05: // Ctrl+E
		b.promptEdit("<End>")
	case 0x03: // Ctrl+C
		b.promptEdit("\x03")
	case '\t':
		if b.promptMode == "path" {
			b.completePathPrompt()
		}
	case 0x1b:
		next, st := b.in.readByte(50 * time.Millisecond)
		if st == inData && next == 'P' {
			b.readAudioReply()
			return
		}
		if st == inData && (next == '[' || next == 'O') {
			first, st2 := b.in.readByte(50 * time.Millisecond)
			if st2 == inData {
				b.handlePromptCSI(first)
			} else {
				b.promptEdit("<Esc>")
			}
		} else if st == inData {
			if key := map[byte]string{'b': "<A-b>", 'f': "<A-f>", 'd': "<A-d>", 0x7f: "<A-BS>"}[next]; key != "" {
				b.promptEdit(key)
			} else {
				b.promptEdit("<Esc>")
			}
		} else {
			b.promptEdit("<Esc>")
		}
	default:
		b.ensurePromptEditor()
		result := b.promptEditor.FeedByte(k)
		b.applyPromptResult(result)
	}
}

func (b *Browser) handlePromptCSI(first byte) {
	switch first {
	case 'A':
		if b.promptMode == "path" {
			if b.promptEditor != nil && b.promptEditor.IMEActive() {
				b.promptEdit("<Esc>")
			}
			b.pathSelectDelta(-1)
		} else {
			b.promptEdit("<Up>")
		}
	case 'B':
		if b.promptMode == "path" {
			if b.promptEditor != nil && b.promptEditor.IMEActive() {
				b.promptEdit("<Esc>")
			}
			b.pathSelectDelta(1)
		} else {
			b.promptEdit("<Down>")
		}
	case 'C':
		b.promptEdit("<Right>")
	case 'D':
		b.promptEdit("<Left>")
	case 'H':
		b.promptEdit("<Home>")
	case 'F':
		b.promptEdit("<End>")
	case '<':
		b.drainPromptMouse()
	default:
		if first >= '0' && first <= '9' {
			rest := b.readNumericCSI(first)
			switch rest {
			case "1~", "7~":
				b.promptEdit("<Home>")
			case "3~":
				b.promptDelete()
			case "4~", "8~":
				b.promptEdit("<End>")
			default:
				if key := readline.CSI(rest); key != "" {
					b.promptEdit(key)
				}
			}
		} else {
			b.promptAbort = true
		}
	}
}

func (b *Browser) drainPromptMouse() {
	b.readSGRPayload()
	b.promptAbort = true
}

func (b *Browser) submitPrompt() {
	if b.promptHistories == nil {
		b.promptHistories = make(map[string][]string)
	}
	history := append(b.promptHistories[b.promptMode], string(b.promptBuffer))
	if len(history) > 100 {
		history = history[1:]
	}
	b.promptHistories[b.promptMode] = history
	if b.promptMode == "path" {
		b.submitPathPrompt()
	} else {
		b.submitCommand()
	}
}

func (b *Browser) promptInsert(text []byte) {
	b.ensurePromptEditor()
	before := b.promptEditor.Text()
	err := b.promptEditor.Insert(string(text))
	b.syncPromptEditor(readline.Result{Changed: before != b.promptEditor.Text(), Err: err})
}

func (b *Browser) promptBackspace() {
	b.promptEdit("<BS>")
}

func (b *Browser) promptDelete() {
	b.promptEdit("<Delete>")
}

func (b *Browser) ensurePromptEditor() {
	if b.promptEditor == nil || b.promptEditor.Text() != string(b.promptBuffer) {
		if b.promptEditor != nil {
			b.promptEditor.Close()
		}
		b.promptEditor = readline.New(string(b.promptBuffer), 0, readline.DefaultLimit)
	}
}
func (b *Browser) syncPromptEditor(result readline.Result) {
	b.promptBuffer = []byte(b.promptEditor.Text())
	b.promptCursor = b.promptEditor.ByteCursor()
	if result.Err != nil {
		b.pathMessage, b.cmdMessage = result.Err.Error(), result.Err.Error()
	}
	if result.Changed {
		b.refreshPromptMatches()
	}
}
func (b *Browser) promptEdit(key string) {
	b.ensurePromptEditor()
	b.applyPromptResult(b.promptEditor.Handle(key))
}
func (b *Browser) applyPromptResult(result readline.Result) {
	b.syncPromptEditor(result)
	if result.Cancel {
		b.promptAbort = true
		return
	}
	if result.Submit {
		b.submitPrompt()
	}
}
func promptMeasure(r rune, column int) int {
	size := 0
	for _, shown := range displayText(string(r)) {
		size += readline.CellWidth(shown, column+size)
	}
	return size
}
func (b *Browser) promptParts() (string, string, string) {
	b.ensurePromptEditor()
	display := b.promptEditor.ViewDisplay(max(1, b.cols-6), promptMeasure)
	return display.Before, strings.TrimPrefix(display.Candidates, " "), display.After
}

func (b *Browser) refreshPromptMatches() {
	b.pathCompleted = ""
	if b.promptMode == "path" {
		b.updatePathMatches(true)
	} else {
		b.updateCommandPreview()
	}
}

// --- path prompt ---

func (b *Browser) pathPrompt() { b.modalPrompt("path", []byte("/")) }

func hasGlob(s []byte) bool {
	return bytes.ContainsAny(s, "*?[")
}

func (b *Browser) pathPattern(completion bool) string {
	input := string(b.promptBuffer)
	pattern := input
	if !strings.HasPrefix(input, "/") {
		pattern = b.dir + "/" + input
	}
	if completion && !hasGlob(b.promptBuffer) {
		pattern += "*"
	}
	return pattern
}

func (b *Browser) updatePathMatches(completion bool) {
	pattern := b.pathPattern(completion)
	var expanded []string
	if b.pathCompleted != "" {
		if completion && isDir(b.pathCompleted) {
			entries, _ := os.ReadDir(b.pathCompleted)
			for _, entry := range entries {
				expanded = append(expanded, filepath.Join(b.pathCompleted, entry.Name()))
			}
		} else {
			expanded = []string{b.pathCompleted}
		}
	} else {
		expanded, _ = filepath.Glob(pattern)
	}
	b.pathMatches = nil
	for _, f := range expanded {
		if pathExists(f) || isSymlink(f) {
			b.pathMatches = append(b.pathMatches, f)
		}
	}
	b.pathMatchIdx = -1
	b.pathMatchTop = 0
	b.pathMessage = ""
}

func (b *Browser) completePathPrompt() {
	b.updatePathMatches(true)
	if len(b.pathMatches) == 0 {
		b.pathMessage = "No match"
		return
	}
	target := b.pathMatches[0]
	value := target
	if !bytes.HasPrefix(b.promptBuffer, []byte("/")) {
		if strings.HasPrefix(value, b.dir+"/") {
			value = strings.TrimPrefix(value, b.dir+"/")
		} else if relative, err := filepath.Rel(b.dir, value); err == nil {
			value = relative
		}
	}
	if isDir(target) && !strings.HasSuffix(value, "/") {
		value += "/"
	}
	b.ensurePromptEditor()
	if len(value) > b.promptEditor.Limit {
		b.pathMessage = "Completed path exceeds input limit"
		return
	}
	b.promptEditor.Reset(value, 0)
	b.syncPromptEditor(readline.Result{})
	// Keep the raw filesystem target, including any literal glob/control bytes.
	// Rendering sanitizes names; Enter must not reinterpret the completion.
	b.pathCompleted = target
	b.updatePathMatches(true)
}

func (b *Browser) pathSelectDelta(delta int) {
	count := len(b.pathMatches)
	if count == 0 {
		return
	}
	if b.pathMatchIdx < 0 {
		if delta < 0 {
			b.pathMatchIdx = count - 1
		} else {
			b.pathMatchIdx = 0
		}
	} else {
		b.pathMatchIdx += delta
		if b.pathMatchIdx < 0 {
			b.pathMatchIdx = 0
		}
		if b.pathMatchIdx >= count {
			b.pathMatchIdx = count - 1
		}
	}
}

func (b *Browser) pathDisplayName(path string) string {
	if !bytes.HasPrefix(b.promptBuffer, []byte("/")) && strings.HasPrefix(path, b.dir+"/") {
		path = strings.TrimPrefix(path, b.dir+"/")
	}
	if isDir(path) {
		path += "/"
	}
	return displayText(path)
}

func (b *Browser) drawPathPrompt() {
	b.paintStop()
	b.clearPathPopup()
	count := len(b.pathMatches)
	visible := pathPopupMax
	if count < visible {
		visible = count
	}
	if b.pathMatchIdx >= 0 {
		if b.pathMatchIdx < b.pathMatchTop {
			b.pathMatchTop = b.pathMatchIdx
		}
		if b.pathMatchIdx >= b.pathMatchTop+pathPopupMax {
			b.pathMatchTop = b.pathMatchIdx - pathPopupMax + 1
		}
	}
	if b.pathMatchTop+visible > count {
		b.pathMatchTop = count - visible
	}
	if b.pathMatchTop < 0 {
		b.pathMatchTop = 0
	}
	start := b.rows - visible
	for i := 0; i < visible; i++ {
		row := start + i
		path := b.pathMatches[b.pathMatchTop+i]
		shown := " " + b.pathDisplayName(path)
		style := statusStyle
		if b.pathMatchTop+i == b.pathMatchIdx {
			style = selectedStyle
		}
		b.drawText(row, 1, b.cols, style, shown)
	}
	before, candidates, after := b.promptParts()
	shown := " / " + displayText(before) + selectedStyle + " " + resetStyle + statusStyle
	if candidates != "" {
		shown += imeStyle + displayText(candidates) + resetStyle + statusStyle
	}
	shown += displayText(after)
	if b.pathMessage != "" {
		shown += "  [" + b.pathMessage + "]"
	}
	b.drawText(b.rows, 1, b.cols, statusStyle, shown)
	b.paintResume()
	b.flush()
}

func (b *Browser) submitPathPrompt() {
	if b.pathMatchIdx >= 0 && b.pathMatchIdx < len(b.pathMatches) {
		b.activateTypedPath(b.pathMatches[b.pathMatchIdx])
		return
	}
	if b.pathCompleted != "" {
		b.activateTypedPath(b.pathCompleted)
		return
	}
	exact := b.pathPattern(false)
	if !hasGlob(b.promptBuffer) && (pathExists(exact) || isSymlink(exact)) {
		b.activateTypedPath(exact)
		return
	}
	b.updatePathMatches(false)
	switch len(b.pathMatches) {
	case 0:
		b.pathMessage = "No match"
	case 1:
		b.activateTypedPath(b.pathMatches[0])
		return
	default:
		b.pathMessage = fmt.Sprintf("%d matches; use Up/Down", len(b.pathMatches))
	}
	b.drawPathPrompt()
}

func (b *Browser) activateTypedPath(path string) {
	b.promptActive = false
	if isDir(path) {
		b.changeDirectory(path, "")
	} else {
		b.openPath(path, 0)
		b.renderAll()
	}
}

// --- command prompt ---

func (b *Browser) commandPrompt() { b.modalPrompt("cmd", []byte(":")) }

func (b *Browser) drawCommandPrompt() {
	b.paintStop()
	b.paintAffectedRows()
	before, candidates, after := b.promptParts()
	shown := " " + displayText(before) + selectedStyle + " " + resetStyle + statusStyle
	if candidates != "" {
		shown += imeStyle + displayText(candidates) + resetStyle + statusStyle
	}
	shown += displayText(after)
	if b.cmdMessage != "" {
		shown += "  [" + b.cmdMessage + "]"
	}
	b.drawText(b.rows, 1, b.cols, statusStyle, shown)
	b.paintResume()
	b.flush()
}

func (b *Browser) submitCommand() {
	code := string(b.promptBuffer)
	b.promptActive = false
	switch {
	case strings.HasPrefix(code, ":s/"):
		b.runSubstitution()
	case code == ":help":
		b.manualMode()
	case strings.HasPrefix(code, ":"):
		b.runShellCommand(strings.TrimPrefix(code, ":"))
	default:
		b.runShellCommand(code)
	}
}

// parseSubst parses :s/old/new/ or :s/old/new/g.
func (b *Browser) parseSubst() bool {
	buf := string(b.promptBuffer)
	b.cmdGlobal = false
	if !strings.HasPrefix(buf, ":s/") {
		return false
	}
	if !(strings.HasSuffix(buf, "/") || strings.HasSuffix(buf, "/g")) {
		return false
	}
	rest := strings.TrimPrefix(buf, ":s/")
	slash := strings.IndexByte(rest, '/')
	if slash < 0 {
		return false
	}
	b.cmdOld = rest[:slash]
	rest = rest[slash+1:]
	slash = strings.IndexByte(rest, '/')
	if slash < 0 {
		return false
	}
	b.cmdNew = rest[:slash]
	rest = rest[slash+1:]
	if rest != "" {
		if rest != "g" {
			return false
		}
		b.cmdGlobal = true
	}
	if b.cmdOld == "" {
		return false
	}
	return true
}

func (b *Browser) updateCommandPreview() {
	b.cmdAffected = nil
	b.cmdNewNames = nil
	b.cmdMessage = ""
	if !bytes.HasPrefix(b.promptBuffer, []byte(":s/")) {
		return
	}
	if !b.parseSubst() {
		b.cmdMessage = "format :s/old/new/[/g]"
		return
	}
	for i, path := range b.files {
		if path == b.dir+"/.." {
			continue
		}
		name := baseName(path)
		var newname string
		if b.cmdGlobal {
			newname = strings.ReplaceAll(name, b.cmdOld, b.cmdNew)
		} else {
			newname = strings.Replace(name, b.cmdOld, b.cmdNew, 1)
		}
		if newname == name {
			continue
		}
		b.cmdAffected = append(b.cmdAffected, i)
		b.cmdNewNames = append(b.cmdNewNames, newname)
	}
	b.cmdMessage = fmt.Sprintf("%d file(s) to rename", len(b.cmdAffected))
}

func (b *Browser) drawListRowHL(slot, index int) {
	if slot < 0 || slot >= b.visible {
		return
	}
	row := b.listTop + slot
	if row > b.listBottom {
		return
	}
	b.drawText(row, b.listX, b.listW, renameHL, " "+b.entryLabel(b.files[index]))
}

func (b *Browser) paintAffectedRows() {
	for _, i := range b.cmdPrevAffected {
		inSet := false
		for _, idx := range b.cmdAffected {
			if idx == i {
				inSet = true
				break
			}
		}
		if !inSet {
			b.drawListRow(i - b.viewTop)
		}
	}
	b.cmdPrevAffected = append([]int{}, b.cmdAffected...)
	for _, idx := range b.cmdAffected {
		b.drawListRowHL(idx-b.viewTop, idx)
	}
}

func (b *Browser) runSubstitution() {
	if b.isArchiveMode() || b.isSQLiteMode() {
		b.status = "Renaming inside a virtual view is not supported"
		b.renderAll()
		return
	}
	if !b.parseSubst() {
		b.status = "Invalid substitution; expected :s/old/new/ or :s/old/new/g"
		b.renderAll()
		return
	}
	b.updateCommandPreview()
	if len(b.cmdAffected) == 0 {
		b.status = "No filenames matched"
		b.renderAll()
		return
	}
	moved, skipped := 0, 0
	for i, idx := range b.cmdAffected {
		path := b.files[idx]
		target := filepath.Join(b.dir, b.cmdNewNames[i])
		if pathExists(target) || isSymlink(target) {
			skipped++
			continue
		}
		if err := os.Rename(path, target); err == nil {
			moved++
		} else {
			skipped++
		}
	}
	b.refreshList()
	b.status = fmt.Sprintf("Renamed %d, skipped %d", moved, skipped)
	b.drawStatus()
}

// --- manual ---

func (b *Browser) manualMode() {
	b.manualPage = 1
	b.manualActive = true
	b.drawManual(b.manualPage)
	for b.manualActive {
		b.processPendingSignals()
		if !b.running {
			b.manualActive = false
			break
		}
		if b.resized {
			b.resized = false
			b.renderAll()
			if b.compact {
				b.manualActive = false
				break
			}
			b.drawManual(b.manualPage)
		}
		key, st := b.in.readByte(100 * time.Millisecond)
		switch st {
		case inTimeout:
			continue
		case inEOF:
			b.manualActive = false
			b.running = false
			break
		case inData:
			switch key {
			case '[':
				if b.manualPage > 1 {
					b.manualPage--
					b.drawManual(b.manualPage)
				}
			case ']':
				if b.manualPage < b.manualTotal {
					b.manualPage++
					b.drawManual(b.manualPage)
				}
			case 'q', 'Q', 0x03:
				b.manualActive = false
			case 0x1b:
				next, st2 := b.in.readByte(50 * time.Millisecond)
				if st2 == inData && next == 'P' {
					b.readAudioReply()
					continue
				}
				if st2 == inData && next == '[' {
					first, st3 := b.in.readByte(50 * time.Millisecond)
					if st3 == inData {
						if first == '<' {
							b.manualSGRPageChange()
						} else {
							b.manualActive = false
						}
					} else {
						b.manualActive = false
					}
				} else {
					b.manualActive = false
				}
			}
		}
	}
	b.status = "Manual closed"
	b.renderAll()
}

func (b *Browser) manualSGRPageChange() {
	payload := b.readSGRPayload()
	m := sgrRe.FindStringSubmatch(payload)
	if m == nil || m[4] != "M" {
		return
	}
	cb := atoi(m[1])
	button := cb & 3
	if cb&64 != 0 { // wheel
		if button == 0 && b.manualPage > 1 {
			b.manualPage--
			b.drawManual(b.manualPage)
		}
		if button == 1 && b.manualPage < b.manualTotal {
			b.manualPage++
			b.drawManual(b.manualPage)
		}
	} else {
		b.manualActive = false
	}
}

func wrapManualLine(text string, width int) []string {
	words := strings.Fields(text)
	var out []string
	buf := ""
	first := true
	for _, w := range words {
		if first {
			buf = w
			first = false
		} else if len(buf)+1+len(w) <= width {
			buf += " " + w
		} else {
			out = append(out, buf)
			buf = w
		}
	}
	if buf != "" {
		out = append(out, buf)
	}
	return out
}

func (b *Browser) buildManualLines() {
	width := b.previewW - 4
	if width < 8 {
		width = 8
	}
	b.manualLines = nil
	for _, line := range strings.Split(manualText, "\n") {
		for _, wl := range wrapManualLine(line, width) {
			b.manualLines = append(b.manualLines, wl)
		}
	}
}

func (b *Browser) drawManual(page int) {
	b.paintStop()
	h := b.rows - 5
	if h < 1 {
		h = 1
	}
	b.buildManualLines()
	total := (len(b.manualLines) + h - 1) / h
	if total < 1 {
		total = 1
	}
	if page > total {
		page = total
	}
	if page < 1 {
		page = 1
	}
	b.manualPage = page
	b.manualTotal = total
	b.clearPreview()
	b.drawPreviewTitle(fmt.Sprintf("USER MANUAL  %d/%d", page, total))
	start := (page - 1) * h
	for i := 0; i < h; i++ {
		row := 4 + i
		line := ""
		if start+i < len(b.manualLines) {
			line = b.manualLines[start+i]
		}
		b.drawText(row, 3, b.previewW-4, resetStyle, line)
	}
	b.drawNavigationSidebar()
	b.status = fmt.Sprintf("Manual  %d/%d   ] next  [ prev  wheel  q close", page, total)
	b.drawStatus()
	b.paintResume()
	b.flush()
}
