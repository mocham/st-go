package main

import (
	"fmt"
	"unicode"
	"unicode/utf8"
)

const maxPromptBytes = 16384

func (e *Editor) beginPrompt(value string) {
	e.prompt = value
	e.promptStart = e.pos()
	e.promptCursor = utf8.RuneCountInString(value)
	e.promptView = 1
	e.historyIndex = len(e.promptHistory)
	e.promptDraft = value
	e.promptDraftCursor = e.promptCursor
}

func (e *Editor) cancelPrompt() {
	e.prompt = ""
	e.promptCursor, e.promptView = 0, 0
	e.promptDraft = ""
	e.substitute = nil
	e.reset()
}

func (e *Editor) insertPrompt(text string) {
	if e.prompt == "" || text == "" {
		return
	}
	if !utf8.ValidString(text) || len(e.prompt)+len(text) > maxPromptBytes {
		e.error(fmt.Errorf("prompt exceeds %d bytes or contains invalid UTF-8", maxPromptBytes))
		return
	}
	line := []rune(e.prompt)
	at := clamp(e.promptCursor, 1, len(line))
	e.prompt = string(line[:at]) + text + string(line[at:])
	e.promptCursor = at + utf8.RuneCountInString(text)
}

func (e *Editor) deletePrompt(first, last int, kill bool) {
	line := []rune(e.prompt)
	first, last = clamp(first, 1, len(line)), clamp(last, 1, len(line))
	if last <= first {
		return
	}
	if kill {
		e.promptKill = string(line[first:last])
	}
	e.prompt = string(line[:first]) + string(line[last:])
	e.promptCursor = first
}

func promptWordLeft(line []rune, at int) int {
	for at > 1 && unicode.IsSpace(line[at-1]) {
		at--
	}
	for at > 1 && !unicode.IsSpace(line[at-1]) {
		at--
	}
	return at
}
func promptWordRight(line []rune, at int) int {
	for at < len(line) && unicode.IsSpace(line[at]) {
		at++
	}
	for at < len(line) && !unicode.IsSpace(line[at]) {
		at++
	}
	return at
}

func (e *Editor) promptHistoryMove(delta int) {
	if e.historyIndex == len(e.promptHistory) {
		e.promptDraft, e.promptDraftCursor = e.prompt, e.promptCursor
	}
	for index := e.historyIndex + delta; index >= 0 && index < len(e.promptHistory); index += delta {
		if entry := e.promptHistory[index]; len(entry) > 0 && entry[0] == e.prompt[0] {
			e.historyIndex = index
			e.prompt = entry
			e.promptCursor = utf8.RuneCountInString(entry)
			e.promptView = 1
			return
		}
	}
	if delta > 0 {
		e.historyIndex = len(e.promptHistory)
		e.prompt = e.promptDraft
		e.promptCursor = clamp(e.promptDraftCursor, 1, utf8.RuneCountInString(e.prompt))
		e.promptView = 1
	}
}

func (e *Editor) promptKey(key string) {
	if e.prompt == "" {
		return
	}
	line := []rune(e.prompt)
	e.promptCursor = clamp(e.promptCursor, 1, len(line))
	at := e.promptCursor
	switch key {
	case "<Esc>", "\x03":
		e.cancelPrompt()
	case "<Enter>", "\r", "\n":
		prompt := e.prompt
		e.prompt = ""
		e.promptCursor, e.promptView = 0, 0
		e.promptDraft = ""
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
	case "<Left>", "\x02":
		e.promptCursor = max(1, at-1)
	case "<Right>", "\x06":
		e.promptCursor = min(len(line), at+1)
	case "<Home>", "\x01":
		e.promptCursor = 1
	case "<End>", "\x05":
		e.promptCursor = len(line)
	case "<C-Left>", "<A-b>":
		e.promptCursor = promptWordLeft(line, at)
	case "<C-Right>", "<A-f>":
		e.promptCursor = promptWordRight(line, at)
	case "<BS>", "\x7f", "\b":
		if at > 1 {
			e.deletePrompt(at-1, at, false)
		} else if len(line) == 1 {
			e.cancelPrompt()
		}
	case "<Delete>", "\x04":
		e.deletePrompt(at, at+1, false)
	case "\x15":
		e.deletePrompt(1, at, true)
	case "\x0b":
		e.deletePrompt(at, len(line), true)
	case "\x17", "<A-BS>":
		e.deletePrompt(promptWordLeft(line, at), at, true)
	case "<A-d>":
		e.deletePrompt(at, promptWordRight(line, at), true)
	case "\x19":
		e.insertPrompt(e.promptKill)
	case "<Up>", "\x10":
		e.promptHistoryMove(-1)
	case "<Down>", "\x0e":
		e.promptHistoryMove(1)
	default:
		if r := []rune(key); len(r) == 1 && r[0] >= 0x20 && !unicode.IsControl(r[0]) {
			e.insertPrompt(key)
		}
	}
}

// Keep the prefix pinned, horizontally scroll long input, and return the
// actual insertion cursor column. Positions are rune indexes; widths are cells.
func (e *Editor) promptLine(width int) (string, int) {
	if e.prompt == "" {
		return "", 0
	}
	line := []rune(e.prompt)
	cursor := clamp(e.promptCursor, 1, len(line))
	e.promptCursor = cursor
	if width <= 1 {
		return string(line[0]), 0
	}
	first := clamp(e.promptView, 1, cursor)
	columns := make([]int, len(line)+1)
	for i := 1; i < len(line); i++ {
		columns[i+1] = columns[i] + runeCells(line[i], columns[i]+1)
	}
	for first < cursor && 1+columns[cursor]-columns[first] >= width {
		first++
	}
	e.promptView = first
	return string(line[0]) + string(line[first:]), min(width-1, 1+columns[cursor]-columns[first])
}
