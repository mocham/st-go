package main

import (
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"st-go/limits"
	"st-go/pdfmeta"
	"st-go/tui/navigation"
	"st-go/tui/readline"
)

type imageMode uint8

const (
	modeFitWidth imageMode = iota
	modeFitPage
	modeZoom
)

type location struct {
	page, position int
}

type viewer struct {
	options
	backend *djvuBackend
	out     io.Writer
	err     error

	cols, rows               int
	pages, position          int
	rendering, regionLoading bool
	documentLoading          bool
	failed, quit             bool
	mode                     imageMode
	imagePath                string
	token                    uint64
	renderToken              uint64
	renderPosition           int
	openToken                uint64
	openPage                 int
	openPosition             int

	prompt, count, prefix, message, lastStatus string
	promptEditor                               *readline.Line
	promptHistory                              []string
	marks                                      map[string]location
	previous                                   location

	search            string
	searchResultQuery string
	searchBackward    bool
	searchVisible     bool
	searchPending     bool
	searchPendingBack bool
	searchToken       uint64
	searchOccurrences []searchOccurrence
	searchActive      int
	searchPage        int
	searchPageMatch   int
	searchTruncated   bool

	panel           panelMode
	panelDirty      bool
	panelError      string
	toc             []pdfmeta.Entry
	tocLoaded       bool
	tocToken        uint64
	tocPending      bool
	tocSelection    navigation.Selection
	pageText        string
	textPage        int
	textTop         int
	textLines       []string
	textLineOffsets []int
	textHits        []textHit
	textToken       uint64
	textPending     bool
}

func newViewer(options options, backend *djvuBackend, pages int, out io.Writer) *viewer {
	page := max(1, min(options.page, pages))
	return &viewer{
		options:      options,
		backend:      backend,
		out:          out,
		cols:         80,
		pages:        pages,
		mode:         modeZoom,
		marks:        make(map[string]location),
		searchActive: -1,
		openPage:     page,
	}
}

func quoteDSL(value string) string { return "'" + strings.ReplaceAll(value, "'", "'\"'\"'") + "'" }

func (v *viewer) nextToken() uint64 {
	v.token++
	if v.token == 0 {
		v.token++
	}
	return v.token
}

func (v *viewer) write(value string) {
	if v.err == nil {
		_, v.err = io.WriteString(v.out, value)
	}
}

func (v *viewer) command(command string) { v.write("\x1bPregion secondary " + command + "\x1b\\") }

func (v *viewer) loading() bool { return v.rendering || v.regionLoading || v.documentLoading }

func (v *viewer) currentLocation() location {
	return location{page: max(1, v.page), position: v.position}
}

func (v *viewer) requestRender(page int) {
	if v.pages < 1 {
		return
	}
	page = max(1, min(page, v.pages))
	v.page, v.position = page, 0
	v.renderPosition = 0
	v.renderToken = v.nextToken()
	v.rendering, v.failed = true, false
	v.lastStatus = ""
	if !v.backend.submit(backendJob{kind: jobRender, token: v.renderToken, page: page}) {
		v.rendering, v.failed = false, true
		v.message = "DjVu renderer is unavailable"
	}
	if v.panel == panelText {
		v.syncPanel()
	}
}

func (v *viewer) requestOpen(path string, page, position int) {
	v.openToken = v.nextToken()
	v.openPage, v.openPosition = page, position
	v.documentLoading, v.failed = true, false
	v.message, v.lastStatus = "Opening DjVu...", ""
	if !v.backend.submit(backendJob{kind: jobOpen, token: v.openToken, path: path, background: v.backgroundRGB}) {
		v.documentLoading, v.failed = false, true
		v.message = "DjVu renderer is unavailable"
	}
}

func (v *viewer) openRendered(path string) {
	mode := "fit-width"
	switch v.mode {
	case modeFitPage:
		mode = "fit-height"
	case modeZoom:
		mode = fmt.Sprintf("zoom %d", max(1, v.zoom))
	}
	v.command("open " + quoteDSL(path) + " viewer " + mode)
	v.regionLoading = true
}

func (v *viewer) jumpPage(page int) {
	v.previous = v.currentLocation()
	v.requestRender(page)
}

func (v *viewer) jumpRelative(delta int) { v.jumpPage(v.page + delta) }

func (v *viewer) crossPage(direction int) {
	if direction > 0 && v.page < v.pages {
		v.jumpLocation(location{page: v.page + 1})
	} else if direction < 0 && v.page > 1 {
		// Crossing the top reveals the bottom of the preceding page, matching a
		// continuous document rather than unexpectedly returning to its top.
		v.jumpLocation(location{page: v.page - 1, position: 1000000})
	}
}

func (v *viewer) jumpLocation(next location) {
	v.previous = v.currentLocation()
	v.requestRender(next.page)
	v.renderPosition = max(0, min(next.position, 1000000))
}

func (v *viewer) fit(mode imageMode) {
	v.mode = mode
	switch mode {
	case modeFitPage:
		v.command("fit height")
	case modeFitWidth:
		v.command("fit width")
	default:
		v.command(fmt.Sprintf("zoom %d", v.zoom))
	}
	v.lastStatus = ""
}

func (v *viewer) zoomTo(zoom int) {
	if zoom == 0 {
		v.fit(modeFitPage)
		return
	}
	zoom = max(10, min(zoom, 500))
	v.zoom, v.mode = zoom, modeZoom
	v.command(fmt.Sprintf("zoom %d", zoom))
	v.lastStatus = ""
}

func (v *viewer) key(key string) {
	if v.prompt != "" {
		if v.promptEditor == nil || v.promptEditor.Text() != v.prompt {
			v.promptEditor = readline.New(v.prompt, 1, 4096)
			v.promptEditor.CancelEmptyBackspace = true
			v.promptEditor.SetHistory(v.promptHistory)
		}
		result := v.promptEditor.Handle(key)
		v.prompt = v.promptEditor.Text()
		if result.Err != nil {
			v.message = result.Err.Error()
		}
		if result.Cancel {
			v.prompt = ""
		}
		if result.Submit {
			command, mode := strings.TrimSpace(v.prompt[1:]), v.prompt[0]
			v.promptHistory = append(v.promptHistory, v.prompt)
			if len(v.promptHistory) > 100 {
				v.promptHistory = v.promptHistory[1:]
			}
			v.prompt = ""
			if mode == ':' {
				v.execute(command)
			} else {
				if command != "" {
					v.search = command
				}
				v.searchBackward = mode == '?'
				v.find(false, false)
			}
		}
		return
	}
	if key == "escape" || key == "\x03" {
		v.prefix, v.count, v.message = "", "", ""
		if v.panel != panelNone {
			v.showPanel(panelNone)
		}
		return
	}
	if key == "\t" {
		v.togglePanel(panelTOC)
		return
	}
	if key == "backtab" {
		v.togglePanel(panelText)
		return
	}
	if v.panelKey(key) {
		v.count, v.prefix = "", ""
		return
	}
	v.message = ""
	if v.prefix == "m" || v.prefix == "'" {
		if len(key) == 1 {
			if v.prefix == "m" {
				v.marks[key] = v.currentLocation()
				v.message = "Mark " + key + " set"
			} else if key == "'" {
				v.jumpLocation(v.previous)
			} else if position, ok := v.marks[key]; ok {
				v.jumpLocation(position)
			} else {
				v.message = "No mark " + key
			}
		}
		v.prefix, v.count = "", ""
		return
	}
	if v.prefix == "z" && key == "0" {
		v.fit(modeFitWidth)
		v.prefix, v.count = "", ""
		return
	}
	if len(key) == 1 && key[0] >= '0' && key[0] <= '9' {
		if len(v.count) < 6 {
			v.count += key
		}
		return
	}
	number, _ := strconv.Atoi(v.count)
	repeat := max(1, number)
	prefix := v.prefix
	v.prefix = ""
	switch key {
	case "q":
		v.quit = true
	case "\x04":
		v.command(fmt.Sprintf("view %d", 500000*min(repeat, limits.MaxBytes/1000000)))
	case "j", "down":
		v.command(fmt.Sprintf("scroll %d", repeat))
	case "k", "up":
		v.command(fmt.Sprintf("scroll %d", -repeat))
	case "h", "left":
		v.command(fmt.Sprintf("pan %d", -repeat))
	case "l", "right":
		v.command(fmt.Sprintf("pan %d", repeat))
	case "J":
		v.jumpRelative(repeat)
	case "K":
		v.jumpRelative(-repeat)
	case " ", "\x06", "pagedown":
		v.command(fmt.Sprintf("view %d", 1000000*min(repeat, limits.MaxBytes/1000000)))
	case "\x02", "pageup":
		v.command(fmt.Sprintf("view %d", -1000000*min(repeat, limits.MaxBytes/1000000)))
	case "\x15":
		v.command(fmt.Sprintf("view %d", -500000*min(repeat, limits.MaxBytes/1000000)))
	case "g":
		if prefix == "g" {
			v.jumpPage(repeat)
		} else {
			v.prefix = "g"
			return
		}
	case "home":
		v.jumpPage(1)
	case "G", "end":
		if number > 0 {
			v.jumpPage(number)
		} else {
			v.jumpPage(v.pages)
		}
	case "%":
		percent := max(0, min(100, number))
		v.jumpPage(max(1, (v.pages*percent+99)/100))
	case "+", "=":
		v.zoomTo(v.zoom + 10*min(repeat, 50))
	case "-":
		v.zoomTo(v.zoom - 10*min(repeat, 50))
	case "z":
		v.prefix = "z"
		return
	case "i":
		if prefix == "z" {
			v.zoomTo(v.zoom + 10*min(repeat, 50))
		}
	case "o":
		if prefix == "z" {
			v.zoomTo(v.zoom - 10*min(repeat, 50))
		} else {
			v.prompt = ":open "
		}
	case "a":
		v.fit(modeFitPage)
	case "s":
		v.fit(modeFitWidth)
	case "r", "R":
		v.requestOpen(v.file, v.page, v.position)
	case "m", "'":
		v.prefix = key
		return
	case ":":
		v.prompt = ":"
	case "/", "?":
		v.prompt = key
	case "n":
		v.find(true, false)
	case "N":
		v.find(true, true)
	case "\x0c":
		v.lastStatus = ""
	default:
		if prefix == "z" && key == "\r" && number > 0 {
			v.zoomTo(number)
		}
	}
	v.count = ""
}

func (v *viewer) help() {
	v.message = "/ search | n/N match | Tab outline | S-Tab text | j/k scroll | J/K page | +/- zoom | q quit"
}

func (v *viewer) find(next, reverse bool) {
	if v.search == "" {
		v.message = "Use /text or ?text first"
		return
	}
	if len(v.search) > 4096 {
		v.message = "Search input is limited to 4096 bytes"
		return
	}
	query := expandSearch(v.search)
	if len(query) > limits.PreviewSearchBytes {
		v.message = fmt.Sprintf("Search is limited to %d bytes", limits.PreviewSearchBytes)
		return
	}
	backward := v.searchBackward != reverse
	if next && !v.searchPending && v.searchResultQuery == query && len(v.searchOccurrences) > 0 {
		v.chooseSearch(backward, true)
		return
	}
	v.searchToken = v.nextToken()
	v.searchPending, v.searchPendingBack = true, backward
	v.searchVisible, v.searchResultQuery = true, ""
	v.message = "Searching DjVu text..."
	if !v.backend.submit(backendJob{kind: jobSearch, token: v.searchToken, query: query}) {
		v.searchPending = false
		v.message = "DjVu search is unavailable"
	}
}

func (v *viewer) chooseSearch(backward, next bool) {
	count := len(v.searchOccurrences)
	if count == 0 {
		v.message = "Text not found"
		return
	}
	index := v.searchActive
	if next && index >= 0 && index < count {
		if backward {
			index = (index - 1 + count) % count
		} else {
			index = (index + 1) % count
		}
	} else if backward {
		index = sort.Search(count, func(i int) bool { return v.searchOccurrences[i].page > v.page }) - 1
		if index < 0 {
			index = count - 1
		}
	} else {
		index = sort.Search(count, func(i int) bool { return v.searchOccurrences[i].page >= v.page })
		if index == count {
			index = 0
		}
	}
	v.searchActive = index
	occurrence := v.searchOccurrences[index]
	v.searchPage, v.searchPageMatch = occurrence.page, occurrence.local
	v.message = fmt.Sprintf("Match %d/%d on page %d (n/N: next/previous)", index+1, count, occurrence.page)
	if v.searchTruncated {
		v.message += " [match/text limit reached]"
	}
	if v.page != occurrence.page {
		v.jumpPage(occurrence.page)
	}
	v.updateTextSearch(true)
	v.panelDirty = true
}

func (v *viewer) execute(command string) {
	name, argument, _ := strings.Cut(command, " ")
	argument = strings.TrimSpace(argument)
	number, numberErr := strconv.Atoi(argument)
	switch name {
	case "q", "quit", "exit":
		v.quit = true
	case "help", "h":
		v.help()
	case "nohlsearch", "noh":
		v.searchVisible, v.searchPage, v.searchPageMatch = false, 0, 0
		v.searchActive, v.textHits = -1, nil
		v.panelDirty = true
	case "reload", "r":
		v.requestOpen(v.file, v.page, v.position)
	case "open", "o":
		if len(argument) >= 2 && (argument[0] == '\'' || argument[0] == '"') && argument[len(argument)-1] == argument[0] {
			argument = argument[1 : len(argument)-1]
		}
		if argument == "" {
			v.message = "Usage: :open path.djvu"
			return
		}
		path, err := validateDjVu(argument)
		if err != nil {
			v.message = err.Error()
			return
		}
		v.requestOpen(path, 1, 0)
	case "page", "p":
		if numberErr != nil || number < 1 {
			v.message = "Usage: :page NUMBER"
			return
		}
		v.jumpPage(number)
	case "zoom":
		if argument == "width" {
			v.fit(modeFitWidth)
		} else if argument == "fit" {
			v.fit(modeFitPage)
		} else if numberErr == nil && number >= 10 && number <= 500 {
			v.zoomTo(number)
		} else {
			v.message = "Usage: :zoom 10..500|width|fit"
		}
	case "":
	default:
		if page, err := strconv.Atoi(name); err == nil && page > 0 {
			v.jumpPage(page)
		} else {
			v.message = "Unknown command: " + name
		}
	}
}

func (v *viewer) backendReply(result backendResult) {
	switch result.kind {
	case jobRender:
		if result.token != v.renderToken {
			return
		}
		v.rendering = false
		if result.err != nil {
			v.failed = true
			v.message = result.err.Error()
			return
		}
		v.imagePath = result.path
		v.openRendered(result.path)
		if v.renderPosition > 0 {
			v.command(fmt.Sprintf("position %d", v.renderPosition))
			v.renderPosition = 0
		}
	case jobOpen:
		if result.token != v.openToken {
			return
		}
		v.documentLoading = false
		if result.err != nil {
			v.message = result.err.Error()
			return
		}
		changed := v.file != result.path
		v.file, v.pages = result.path, result.pages
		v.page = max(1, min(v.openPage, v.pages))
		if changed {
			v.marks = make(map[string]location)
			v.previous = location{}
		}
		v.toc, v.tocLoaded, v.tocPending = nil, false, false
		v.pageText, v.textPage, v.textPending = "", 0, false
		v.searchOccurrences, v.searchActive, v.searchResultQuery = nil, -1, ""
		v.searchPage, v.searchPageMatch, v.searchTruncated = 0, 0, false
		v.panelError, v.message = "", ""
		v.requestRender(v.page)
		v.renderPosition = max(0, min(v.openPosition, 1000000))
	case jobText:
		if result.token != v.textToken || result.page != v.page {
			return
		}
		v.textPending = false
		if result.err != nil {
			v.panelError, v.message = result.err.Error(), result.err.Error()
			v.panelDirty = true
			return
		}
		v.pageText, v.textPage, v.textTop = cleanPanelText(result.text), result.page, 0
		v.reflowText()
		v.updateTextSearch(true)
		if result.truncated {
			v.message = "DjVu page text truncated at the preview limit"
		}
		v.panelDirty = true
	case jobTOC:
		if result.token != v.tocToken {
			return
		}
		v.tocPending = false
		if result.err != nil {
			v.panelError, v.message = result.err.Error(), result.err.Error()
			v.panelDirty = true
			return
		}
		v.toc, v.tocLoaded = result.toc, true
		v.tocSelection.Normalize(len(v.toc), v.panelRows())
		if result.truncated {
			v.message = "DjVu outline truncated at the preview limit"
		}
		v.panelDirty = true
	case jobSearch:
		if result.token != v.searchToken {
			return
		}
		v.searchPending = false
		if result.err != nil {
			if !errors.Is(result.err, errSearchCanceled) {
				v.message = result.err.Error()
			}
			return
		}
		v.searchOccurrences = result.occurrences
		v.searchResultQuery = expandSearch(v.search)
		v.searchTruncated = result.truncated
		v.searchActive = -1
		v.chooseSearch(v.searchPendingBack, false)
	}
	v.lastStatus = ""
}

func (v *viewer) reply(payload string) {
	fields := strings.Fields(payload)
	if len(fields) == 5 && strings.Join(fields[:4], " ") == "region secondary event scroll" {
		direction, err := strconv.Atoi(fields[4])
		if err == nil && (direction == -1 || direction == 1) {
			v.crossPage(direction)
		}
		return
	}
	if len(fields) != 10 || strings.Join(fields[:3], " ") != "region secondary image-status" {
		return
	}
	var values [6]int
	for index, field := range []string{fields[3], fields[4], fields[5], fields[6], fields[8], fields[9]} {
		number, err := strconv.Atoi(field)
		if err != nil || number < 0 {
			return
		}
		values[index] = number
	}
	if values[2] > 1000000 || values[4] > 1 || values[5] > 1 {
		return
	}
	v.position, v.zoom = values[2], max(1, values[3])
	v.regionLoading, v.failed = values[4] != 0, values[5] != 0
	switch fields[7] {
	case "zoom":
		v.mode = modeZoom
	case "width":
		v.mode = modeFitWidth
	case "height":
		v.mode = modeFitPage
	default:
		return
	}
}

func (v *viewer) modeName() string {
	switch v.mode {
	case modeFitPage:
		return "height"
	case modeZoom:
		return "zoom"
	default:
		return "width"
	}
}

func (v *viewer) draw() {
	v.drawPanel()
	text := fmt.Sprintf(" %d/%d  %s %d%%  [%d%%]  %s", v.page, v.pages, v.modeName(), v.zoom, v.position/10000, filepath.Base(v.file))
	if v.loading() {
		text += "  loading..."
	}
	if v.failed {
		text += "  DjVu load/render failed (:reload to retry)"
	}
	if v.message != "" {
		text = " " + v.message
	}
	if v.count != "" || v.prefix != "" {
		text += "  " + v.count + v.prefix
	}
	if v.prompt != "" {
		if v.promptEditor == nil || v.promptEditor.Text() != v.prompt {
			v.promptEditor = readline.New(v.prompt, 1, 4096)
			v.promptEditor.CancelEmptyBackspace = true
			v.promptEditor.SetHistory(v.promptHistory)
		}
		shown, cursor := v.promptEditor.View(max(1, v.cols-1), func(rune, int) int { return 1 })
		text = shown
		v.lastStatus = ""
		defer func() { v.write(fmt.Sprintf("\x1b[1;%dH\x1b[?25h", cursor+1)) }()
	} else if v.promptEditor != nil {
		v.write("\x1b[?25l")
		v.promptEditor = nil
	}
	text = statusText(text, max(1, v.cols-1))
	if text == v.lastStatus {
		return
	}
	v.lastStatus = text
	v.write("\x1b[H\x1b[0;38;5;252;48;5;236m\x1b[2K" + text + "\x1b[0m")
}

func statusText(text string, width int) string {
	var output strings.Builder
	for _, character := range text {
		if output.Len() >= width {
			break
		}
		if character < 32 || character > 126 {
			character = '?'
		}
		output.WriteByte(byte(character))
	}
	return output.String()
}
