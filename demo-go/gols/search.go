package main

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"st-go/limits"
	"st-go/pdfmeta"
	"st-go/tui/readline"
	"st-go/tui/textsearch"
)

type documentSearch struct {
	query, path                                            string
	backward, initialBackward, visible, pending, truncated bool
	token, active, anchor                                  int
	pages                                                  []int
	data                                                   []byte
	started                                                time.Time
}

func (browser *Browser) startDocumentSearchPrompt(mode byte) {
	if mode != ':' && !browser.displayedPreview.pdf {
		browser.status = "Select a PDF/TeX/Markdown document to search; s searches the corpus"
		browser.drawStatus()
		return
	}
	browser.searchPromptBuffer = []byte{mode}
	if browser.searchLine != nil {
		browser.searchLine.Close()
	}
	browser.searchLine = readline.New(string(browser.searchPromptBuffer), 1, 4097)
	browser.searchLine.SetHistory(browser.searchHistory)
	fmt.Fprint(browser.out, "\x1b[?25h")
	browser.drawPrompt(string(browser.searchPromptBuffer))
}

func (browser *Browser) handleSearchPromptKey(character byte) {
	buffer := browser.searchPromptBuffer
	if browser.searchLine == nil || browser.searchLine.Text() != string(buffer) {
		if browser.searchLine != nil {
			browser.searchLine.Close()
		}
		browser.searchLine = readline.New(string(buffer), 1, 4097)
		browser.searchLine.SetHistory(browser.searchHistory)
	}
	browser.applySearchPromptResult(browser.editInputByte(browser.searchLine, character))
}

func (browser *Browser) applySearchPromptResult(result readline.Result) {
	if browser.searchLine == nil {
		return
	}
	browser.searchPromptBuffer = []byte(browser.searchLine.Text())
	if result.Err != nil {
		browser.status = result.Err.Error()
	}
	finish := func() {
		browser.searchPromptBuffer = nil
		browser.searchLine.Close()
		browser.searchLine = nil
		fmt.Fprint(browser.out, "\x1b[?25l")
	}
	if result.Submit {
		buffer := browser.searchPromptBuffer
		browser.searchHistory = append(browser.searchHistory, string(buffer))
		if len(browser.searchHistory) > 100 {
			browser.searchHistory = browser.searchHistory[1:]
		}
		value, mode := strings.TrimSpace(string(buffer[1:])), buffer[0]
		finish()
		if mode == ':' {
			if value == "noh" || value == "nohlsearch" {
				browser.clearDocumentSearch()
				browser.status = "Search highlights cleared"
				browser.renderAll()
			} else {
				browser.status = "Use :noh or :nohlsearch to clear highlights"
				browser.drawStatus()
			}
			return
		}
		if value == "" {
			value = browser.documentSearch.query
		}
		browser.findDocumentText(value, mode == '?')
	}
	if result.Cancel {
		finish()
		browser.drawStatus()
		return
	}
	if len(browser.searchPromptBuffer) > 0 {
		browser.drawPrompt(string(browser.searchPromptBuffer))
	}
}

func (browser *Browser) editInputByte(line *readline.Line, character byte) readline.Result {
	if character != 0x1b {
		return line.FeedByte(character)
	}
	next, status := browser.in.readByte(50 * time.Millisecond)
	if status != inData {
		return line.Handle("<Esc>")
	}
	if next == '[' || next == 'O' {
		first, st := browser.in.readByte(50 * time.Millisecond)
		if st != inData {
			return readline.Result{Handled: true}
		}
		sequence := string(first)
		if first < 0x40 || first > 0x7e {
			sequence = browser.readCSI(first)
		}
		return line.Handle(readline.CSI(sequence))
	}
	if key := map[byte]string{'b': "<A-b>", 'f': "<A-f>", 'd': "<A-d>", 0x7f: "<A-BS>"}[next]; key != "" {
		return line.Handle(key)
	}
	return line.Handle("<Esc>")
}

func (browser *Browser) findDocumentText(query string, backward bool) {
	if !browser.displayedPreview.pdf {
		browser.status = "Select a rendered document to search"
		browser.drawStatus()
		return
	}
	if query == "" {
		browser.status = "Use /text or ?text first"
		browser.drawStatus()
		return
	}
	if len(query) > 4096 {
		browser.status = "Search input is limited to 4096 bytes"
		browser.drawStatus()
		return
	}
	expanded := textsearch.Expand(query)
	if len(expanded) > limits.PreviewSearchBytes || strings.ContainsRune(expanded, 0) {
		browser.status = fmt.Sprintf("Search is limited to %d UTF-8 bytes", limits.PreviewSearchBytes)
		browser.drawStatus()
		return
	}
	browser.searchToken = browser.searchToken%(1<<30) + 1
	browser.documentSearch = documentSearch{query: query, path: browser.displayedPreview.path, backward: backward,
		initialBackward: backward, visible: true, pending: true, token: browser.searchToken, active: -1, anchor: browser.page, started: time.Now()}
	browser.status = "Searching document..."
	// Redraw once to remove the previous query's highlights, but reuse its PDF.
	browser.renderAll()
	browser.dcs(fmt.Sprintf("pdfsearch query %d %s %s", browser.searchToken, quoteDSL(browser.documentSearch.path), quoteDSL(expanded)))
	browser.out.Flush()
}

func (browser *Browser) clearDocumentSearch() {
	search := &browser.documentSearch
	if search.visible || search.pending {
		browser.dcs("pdfsearch clear")
	}
	query, backward := search.query, search.backward
	*search = documentSearch{query: query, backward: backward, active: -1}
}

func (browser *Browser) nextDocumentMatch(reverse bool) {
	search := &browser.documentSearch
	if search.pending {
		browser.status = "Searching document..."
		browser.drawStatus()
		return
	}
	if !search.visible {
		backward := search.backward
		browser.findDocumentText(search.query, backward != reverse)
		browser.documentSearch.backward = backward
		return
	}
	if len(search.pages) == 0 {
		browser.status = "Text not found"
		browser.drawStatus()
		return
	}
	direction := 1
	if search.backward != reverse {
		direction = -1
	}
	search.active = (search.active + direction + len(search.pages)) % len(search.pages)
	browser.revealDocumentMatch()
}

func (browser *Browser) revealDocumentMatch() {
	search := &browser.documentSearch
	if len(search.pages) == 0 {
		browser.status = "Text not found"
		browser.renderAll()
		return
	}
	browser.page = search.pages[search.active]
	if browser.view.kind == viewTOC {
		selected := 1
		for i, entry := range browser.entries {
			if entry.kind == entryTOC && entry.toc.Page <= browser.page {
				selected = i
			}
		}
		browser.idx = selected
		browser.ensureVisible()
	}
	browser.status = fmt.Sprintf("Match %d/%d on page %d", search.active+1, len(search.pages), browser.page)
	if search.truncated {
		browser.status += " [match limit reached]"
	}
	browser.renderAll()
}

func (browser *Browser) searchReply(payload string) {
	fields := strings.Fields(payload)
	search := &browser.documentSearch
	if !search.pending || len(fields) != 6 || fields[0] != "pdfsearch" || fields[1] != "data" {
		return
	}
	token, e1 := strconv.Atoi(fields[2])
	offset, e2 := strconv.Atoi(fields[3])
	next, e3 := strconv.Atoi(fields[4])
	if e1 != nil || e2 != nil || e3 != nil || token != search.token || offset != len(search.data) {
		return
	}
	data, err := base64.StdEncoding.DecodeString(fields[5])
	if err != nil || len(data) > pdfmeta.ChunkBytes || len(search.data)+len(data) > pdfmeta.MaxBytes ||
		(next != -1 && (next != offset+len(data) || len(data) == 0)) {
		browser.searchFailure("Invalid or oversized PDF search reply")
		return
	}
	search.data = append(search.data, data...)
	search.started = time.Now()
	if next != -1 {
		browser.dcs(fmt.Sprintf("pdfsearch chunk %d %d", token, next))
		browser.out.Flush()
		return
	}
	var result pdfmeta.Search
	if err := json.Unmarshal(search.data, &result); err != nil || len(result.Pages) > limits.PreviewSearchMatches {
		browser.searchFailure("Invalid PDF search result")
		return
	}
	if result.Error != "" {
		browser.searchFailure(safeText(result.Error))
		return
	}
	previous := 0
	for _, page := range result.Pages {
		if page < 1 || page > browser.pages || page < previous {
			browser.searchFailure("Invalid PDF search page")
			return
		}
		previous = page
	}
	search.pages, search.pending, search.truncated, search.data = result.Pages, false, result.Truncated, nil
	search.active = 0
	if search.initialBackward {
		search.active = sort.Search(len(search.pages), func(i int) bool { return search.pages[i] > search.anchor }) - 1
		if search.active < 0 {
			search.active = len(search.pages) - 1
		}
	} else {
		search.active = sort.SearchInts(search.pages, search.anchor)
		if search.active == len(search.pages) {
			search.active = 0
		}
	}
	browser.revealDocumentMatch()
}

func quoteDSL(text string) string { return "'" + strings.ReplaceAll(text, "'", "'\"'\"'") + "'" }

func (browser *Browser) searchFailure(message string) {
	browser.clearDocumentSearch()
	browser.status = "Search failed: " + message
	browser.renderAll()
}

func (browser *Browser) expireDocumentSearch(now time.Time) {
	search := &browser.documentSearch
	if search.pending && now.Sub(search.started) > 30*time.Second {
		browser.searchFailure("no PDF search reply; use updated st-go with PDF support")
	}
}
