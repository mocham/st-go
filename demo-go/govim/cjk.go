package main

import (
	"fmt"
	"strings"
	"time"

	"st-go/tui/pinyin"
)

type cjkInput struct {
	worker                                   *pinyin.Worker
	buffer                                   *Buffer
	at                                       int
	prompt                                   string
	page                                     int
	serial                                   uint64
	ready                                    bool
	response                                 pinyin.Response
	selection, closing                       int
	queued                                   []keyEvent
	queuedBytes                              int
	commandAt, macroAt                       int
	recordCommand, recordMacro               bool
	openAt, insertAt, commandOpen, macroOpen int
	recordSessionCommand, recordSessionMacro bool
}

func defaultPinyinWorker() (*pinyin.Worker, error) {
	helper, err := pinyin.HelperPath()
	if err != nil {
		return nil, err
	}
	return pinyin.NewProcessWorker(helper)
}

func (e *Editor) startCJK() {
	var worker *pinyin.Worker
	var err error
	if e.newPinyinWorker != nil {
		worker = e.newPinyinWorker()
	} else {
		worker, err = defaultPinyinWorker()
	}
	if err != nil {
		e.error(fmt.Errorf("CJK unavailable; ordinary insertion continues: %w", err))
		return
	}
	e.cjk = &cjkInput{
		worker: worker, buffer: e.buffer(), at: e.pos(), openAt: e.pos() - 3,
		insertAt:    len([]rune(e.insertText)) - 3,
		commandOpen: len(e.commandKeys) - 3, macroOpen: len(e.macroKeys) - 3,
		recordSessionCommand: !e.replaying && len(e.commandKeys) >= 3,
		recordSessionMacro:   e.recording != 0 && len(e.macroKeys) >= 3,
	}
	e.message = ""
	e.dirty = true
}
func (e *Editor) stopCJK(err error) {
	e.cjkOpens = 0
	c := e.cjk
	if c == nil {
		return
	}
	e.cjk = nil
	c.worker.Close()
	e.pinyinClosing = append(e.pinyinClosing, c.worker)
	e.dirty = true
	if err != nil {
		e.error(fmt.Errorf("CJK stopped; typed text retained: %w", err))
	}
	// A failed decoder never consumes the keys typed after a pending selection.
	for _, event := range c.queued {
		e.Feed(event)
	}
}

func (e *Editor) cjkValid(c *cjkInput) bool {
	if e.mode != insertMode || e.buffer() != c.buffer {
		return false
	}
	if c.openAt < 0 || c.openAt+3 > len(c.buffer.text) || e.pos() < c.openAt+3 || string(c.buffer.text[c.openAt:c.openAt+3]) != "[[[" {
		return false
	}
	if c.prompt == "" {
		return true
	}
	end := c.at + len(c.prompt)
	return c.at >= 0 && end <= len(c.buffer.text) && e.pos() == end && string(c.buffer.text[c.at:end]) == c.prompt
}

func (e *Editor) queueCJK(event keyEvent) bool {
	c := e.cjk
	if c == nil || c.selection == 0 {
		return false
	}
	if event.Key == "\x03" {
		e.stopCJK(nil)
		return false
	}
	bytes := len(event.Key) + len(event.Paste)
	if len(c.queued) >= 4096 || c.queuedBytes+bytes > 65536 {
		e.stopCJK(fmt.Errorf("pending input limit exceeded"))
		return false
	}
	c.queued = append(c.queued, event)
	c.queuedBytes += bytes
	return true
}

func (e *Editor) resetCJKComposition(c *cjkInput) {
	c.at = e.pos()
	c.prompt = ""
	c.page = 0
	c.serial++
	c.ready = false
	c.response = pinyin.Response{}
}
func (e *Editor) queryCJK(c *cjkInput) {
	c.serial++
	c.ready = false
	c.response = pinyin.Response{}
	if err := c.worker.Submit(pinyin.Request{ID: c.serial, Prompt: c.prompt, Page: c.page}); err != nil {
		e.stopCJK(err)
	}
}

// Only live Insert keys enter the IME. Register/paste/literal insertion bypasses
// this hook. Delimiters are journaled immediately, then removed only on ]]] exit.
func (e *Editor) cjkKey(key string) bool {
	if e.mode != insertMode {
		return false
	}
	c := e.cjk
	if c == nil {
		if key != "[" {
			e.cjkOpens = 0
			return false
		}
		e.cjkOpens++
		if e.cjkOpens < 3 {
			return false
		}
		e.cjkOpens = 0
		before := e.pos()
		e.insertLiteral("[")
		if e.pos() == before+1 {
			e.startCJK()
		}
		return true
	}
	if !e.cjkValid(c) {
		e.stopCJK(nil)
		return false
	}
	if key == "]" {
		e.resetCJKComposition(c)
		e.insertLiteral("]")
		c.closing++
		if c.closing == 3 {
			e.finishCJK(c)
		}
		return true
	}
	c.closing = 0
	if len(key) == 1 && (key[0] >= 'a' && key[0] <= 'z' || key == "'") {
		if c.prompt == "" {
			c.at = e.pos()
			c.commandAt = len(e.commandKeys) - 1
			c.macroAt = len(e.macroKeys) - 1
			c.recordCommand = !e.replaying && c.commandAt >= 0
			c.recordMacro = e.recording != 0 && c.macroAt >= 0
		}
		before := e.pos()
		e.insertLiteral(key)
		if e.pos() != before+1 {
			e.stopCJK(nil)
			return true
		}
		c.prompt += key
		c.page = 0
		if len(c.prompt) > pinyin.MaxPrompt {
			e.stopCJK(fmt.Errorf("Pinyin prompt exceeds %d letters", pinyin.MaxPrompt))
			return true
		}
		e.queryCJK(c)
		return true
	}
	if c.prompt != "" {
		switch key {
		case "+", "-":
			page := c.page
			if key == "+" {
				page = min(pinyin.MaxPage, page+1)
			} else {
				page = max(0, page-1)
			}
			if c.ready && c.response.Total > 0 {
				page = min(page, (c.response.Total-1)/pinyin.PageSize)
			}
			if page != c.page {
				c.page = page
				e.queryCJK(c)
			}
			return true
		case "1", "2", "3", "4", "5":
			c.selection = int(key[0]-'1') + 1
			if c.ready {
				e.chooseCJK(c)
			}
			return true
		case "<BS>", "\x7f", "\b":
			if e.replace(e.pos()-1, e.pos(), "", e.insertGroup) {
				c.prompt = c.prompt[:len(c.prompt)-1]
				text := []rune(e.insertText)
				if len(text) > 0 {
					e.insertText = string(text[:len(text)-1])
				}
				c.page = 0
				if c.prompt != "" {
					e.queryCJK(c)
				} else {
					e.resetCJKComposition(c)
				}
			}
			return true
		}
	}
	if len([]rune(key)) == 1 && []rune(key)[0] >= 32 {
		e.resetCJKComposition(c)
		// Punctuation/space finalizes unselected spelling literally. Only 1..5
		// chooses a candidate; no implicit text replacement can lose a suffix.
		return false
	}
	if key == "<BS>" || key == "\x7f" || key == "\b" {
		return false
	}
	e.stopCJK(nil)
	return false
}

// Only an explicit, successful ]]] exit removes the delimiters. Escape,
// navigation, missing/crashed helpers and other cancellation keep the trace.
func (e *Editor) finishCJK(c *cjkInput) {
	b, end := e.buffer(), e.pos()
	if c.openAt < 0 || end < c.openAt+6 || end > len(b.text) || string(b.text[c.openAt:c.openAt+3]) != "[[[" || string(b.text[end-3:end]) != "]]]" {
		e.stopCJK(nil)
		return
	}
	body := string(b.text[c.openAt+3 : end-3])
	if e.edit([]patch{{end - 3, "]]]", ""}, {c.openAt, "[[[", ""}}, end-6, e.insertGroup) {
		text := []rune(e.insertText)
		if c.insertAt >= 0 && len(text) >= c.insertAt+6 && string(text[c.insertAt:c.insertAt+3]) == "[[[" && string(text[len(text)-3:]) == "]]]" {
			e.insertText = string(text[:c.insertAt]) + string(text[c.insertAt+3:len(text)-3])
		} else {
			e.insertText = ""
			e.insertCount = 1
		}
		literal := keyEvent{Key: "<Paste>", Paste: body}
		if c.recordSessionCommand && c.commandOpen <= len(e.commandKeys) {
			e.commandKeys = append(e.commandKeys[:c.commandOpen], literal)
		}
		if c.recordSessionMacro && c.macroOpen <= len(e.macroKeys) {
			e.macroKeys = append(e.macroKeys[:c.macroOpen], literal)
		}
	}
	e.stopCJK(nil)
}

func (e *Editor) chooseCJK(c *cjkInput) {
	queued := c.queued
	c.queued = nil
	c.queuedBytes = 0
	index := c.selection - 1
	c.selection = 0
	if e.cjkValid(c) && index >= 0 && index < len(c.response.Candidates) {
		chosen := c.response.Candidates[index]
		if e.edit([]patch{{c.at, c.prompt, chosen}}, c.at+len([]rune(chosen)), e.insertGroup) {
			e.insertText = strings.TrimSuffix(e.insertText, c.prompt) + chosen
			// Repeats/macros replay accepted Unicode literally, not an asynchronous
			// candidate query whose result could change or arrive after Escape.
			literal := keyEvent{Key: "<Paste>", Paste: chosen}
			if c.recordCommand && c.commandAt <= len(e.commandKeys) {
				e.commandKeys = append(e.commandKeys[:c.commandAt], literal)
			}
			if c.recordMacro && c.macroAt <= len(e.macroKeys) {
				e.macroKeys = append(e.macroKeys[:c.macroAt], literal)
			}
			e.resetCJKComposition(c)
		}
	}
	for _, event := range queued {
		e.Feed(event)
	}
}

func (e *Editor) pollCJK() {
	kept := e.pinyinClosing[:0]
	for _, worker := range e.pinyinClosing {
		select {
		case <-worker.Done():
		default:
			kept = append(kept, worker)
		}
	}
	e.pinyinClosing = kept
	c := e.cjk
	if c == nil {
		return
	}
	if !e.cjkValid(c) {
		e.stopCJK(nil)
		return
	}
	for {
		select {
		case result := <-c.worker.Results():
			if result.Err != nil {
				e.stopCJK(result.Err)
				return
			}
			if result.Response.ID != c.serial {
				continue
			}
			c.response = result.Response
			c.page = result.Response.Page
			c.ready = true
			e.dirty = true
			if c.selection != 0 {
				e.chooseCJK(c)
				return
			}
		default:
			return
		}
	}
}

func (e *Editor) closeCJK() {
	e.stopCJK(nil)
	deadline := time.NewTimer(2 * time.Second)
	defer deadline.Stop()
	for _, worker := range e.pinyinClosing {
		select {
		case <-worker.Done():
		case <-deadline.C:
			return
		}
	}
	e.pinyinClosing = nil
}

func (e *Editor) cjkStatus(columns int) string {
	c := e.cjk
	if c.prompt == "" {
		return "CJK  type pinyin; 1-5 select; +/- pages; ]]] ends"
	}
	header := fmt.Sprintf("CJK %s [%d] ", c.prompt, c.page+1)
	if !c.ready {
		return header + "loading candidates..."
	}
	if len(c.response.Candidates) == 0 {
		return header + "no candidates; Backspace edits; ]]] ends"
	}
	footer := " +/-  ]]]"
	label, between := ":", " "
	count := len(c.response.Candidates)
	available := max(0, columns-1)
	// The default preview split can leave only 32 editor columns. Prioritize
	// all five numbered choices over hints, so Chinese text never disappears
	// merely to make room for the prompt/page caption.
	if len(header)+len(footer)+count*4+count-1 > available {
		header, footer, label = "", "", ""
	}
	if available < count*3+count-1 {
		between = ""
	}
	budget := max(0, available-len(header)-len(footer)-(count-1)*len(between))
	width := budget / count
	var out strings.Builder
	out.WriteString(header)
	for i, candidate := range c.response.Candidates {
		if i > 0 {
			out.WriteString(between)
		}
		fmt.Fprintf(&out, "%d%s%s", i+1, label, truncateCells(candidate, max(0, width-1-len(label))))
	}
	out.WriteString(footer)
	return out.String()
}
