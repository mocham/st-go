package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"

	"golang.org/x/sys/unix"
)

const (
	resetStyle     = "\x1b[0m"
	dimStyle       = "\x1b[2m"
	headerStyle    = "\x1b[38;5;231m\x1b[48;5;24m\x1b[1m"
	selectedStyle  = "\x1b[38;5;231m\x1b[48;5;31m\x1b[1m"
	directoryStyle = "\x1b[38;5;81m\x1b[1m"
	documentStyle  = "\x1b[38;5;254m"
	currentStyle   = "\x1b[38;5;120m"
	obsoleteStyle  = "\x1b[38;5;244m"
	infoStyle      = "\x1b[38;5;110m"
	statusStyle    = "\x1b[38;5;254m\x1b[48;5;236m"
)

type viewKind uint8

const (
	viewDatabases viewKind = iota
	viewCourses
	viewDocuments
	viewTOC
	viewSearch
)

type entryKind uint8

const (
	entryParent entryKind = iota
	entryDatabase
	entryCourse
	entryDocument
	entryTOC
)

type viewState struct {
	kind        viewKind
	corpusIndex int
	course      string
	documentID  int64
	query       string
}

type viewSnapshot struct {
	view    viewState
	idx     int
	viewTop int
	page    int
}

type browserEntry struct {
	kind        entryKind
	corpusIndex int
	label       string
	count       int
	document    documentMeta
	toc         previewTOCEntry
}

type Browser struct {
	in  *input
	out interface {
		Write([]byte) (int, error)
		Flush() error
	}
	fd int

	rows, cols          int
	listW, listX        int
	previewW            int
	cellPixelW          float64
	cellPixelH          float64
	listTop, listBottom int
	visible             int
	compact             bool

	corpora        []*corpus
	recentMode     bool
	recentRecorder func([]string) error
	view           viewState
	history        []viewSnapshot
	entries        []browserEntry
	idx            int
	viewTop        int
	page           int
	pages          int
	status         string
	navOpen        bool

	openedDocumentID int64
	openedPreview    renderedPreview

	cacheDir    string
	previewLink string
	previewer   *documentPreviewer

	lastClickIdx int
	lastClickAt  time.Time
	resized      bool
	running      bool
	exitCode     int
	cleaned      bool
	sigCh        chan os.Signal
}

func (browser *Browser) setup() error {
	cacheDir, err := os.MkdirTemp("", "st-go-gols.")
	if err != nil {
		return err
	}
	browser.cacheDir = cacheDir
	browser.previewLink = filepath.Join(cacheDir, "content")
	browser.previewer = newDocumentPreviewer(cacheDir)
	browser.view = viewState{kind: viewDatabases, corpusIndex: -1}
	browser.idx = 0
	browser.page = 1
	browser.lastClickIdx = -1
	browser.status = "Ready"
	browser.navOpen = true
	browser.buildEntries()
	return nil
}

func (browser *Browser) start() {
	browser.terminalSize()
	fmt.Fprint(browser.out, "\x1b[?1049h\x1b[?25l\x1b[?1000h\x1b[?1006h")
	browser.renderAll()
}

func (browser *Browser) cleanup() {
	if browser.cleaned {
		return
	}
	browser.cleaned = true
	fmt.Fprint(browser.out, "\x1b[?1006l\x1b[?1000l\x1b[?25h\x1b[0m\x1b[?1049l")
	browser.out.Flush()
	browser.restoreTermios()
	for _, corpus := range browser.corpora {
		_ = corpus.close()
	}
	_ = os.RemoveAll(browser.cacheDir)
}

func (browser *Browser) buildEntries() {
	browser.entries = nil
	switch browser.view.kind {
	case viewDatabases:
		for index, corpus := range browser.corpora {
			browser.entries = append(browser.entries, browserEntry{
				kind: entryDatabase, corpusIndex: index, label: corpus.label, count: len(corpus.documents),
			})
		}
	case viewCourses:
		browser.entries = append(browser.entries, browserEntry{kind: entryParent, label: ".."})
		if corpus := browser.currentCorpus(); corpus != nil {
			for _, course := range corpus.courses() {
				browser.entries = append(browser.entries, browserEntry{
					kind: entryCourse, corpusIndex: browser.view.corpusIndex,
					label: course.Name, count: course.Count,
				})
			}
		}
	case viewDocuments:
		browser.entries = append(browser.entries, browserEntry{kind: entryParent, label: ".."})
		if corpus := browser.currentCorpus(); corpus != nil {
			for _, document := range corpus.documentsForCourse(browser.view.course) {
				browser.entries = append(browser.entries, browserEntry{
					kind: entryDocument, corpusIndex: browser.view.corpusIndex,
					label: document.Title, document: document,
				})
			}
		}
	case viewTOC:
		browser.entries = append(browser.entries, browserEntry{kind: entryParent, label: ".."})
		document, ok := browser.currentDocument()
		if !ok {
			browser.status = "Document is no longer available"
			break
		}
		if browser.openedDocumentID != document.ID || browser.openedPreview.path == "" {
			browser.status = "Document has not been opened"
			break
		}
		preview := browser.openedPreview
		startLabel := "Document start"
		if !preview.pdf {
			startLabel = "Source"
		}
		browser.entries = append(browser.entries, browserEntry{
			kind: entryTOC, corpusIndex: browser.view.corpusIndex,
			label: startLabel, toc: previewTOCEntry{Title: startLabel, Page: 1},
		})
		for _, section := range preview.toc {
			browser.entries = append(browser.entries, browserEntry{
				kind: entryTOC, corpusIndex: browser.view.corpusIndex,
				label: section.Title, toc: section,
			})
		}
	case viewSearch:
		browser.entries = append(browser.entries, browserEntry{kind: entryParent, label: ".."})
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		for index, corpus := range browser.corpora {
			if browser.view.corpusIndex >= 0 && browser.view.corpusIndex != index {
				continue
			}
			documents, err := corpus.search(ctx, browser.view.query, 250)
			if err != nil {
				browser.status = "Search failed: " + err.Error()
				continue
			}
			for _, document := range documents {
				browser.entries = append(browser.entries, browserEntry{
					kind: entryDocument, corpusIndex: index,
					label: document.Title, document: document,
				})
			}
		}
	}
	if len(browser.entries) == 0 {
		browser.idx = -1
		browser.viewTop = 0
		return
	}
	if browser.idx < 0 {
		browser.idx = 0
	}
	if browser.idx >= len(browser.entries) {
		browser.idx = len(browser.entries) - 1
	}
	browser.ensureVisible()
}

func (browser *Browser) currentCorpus() *corpus {
	if browser.view.corpusIndex < 0 || browser.view.corpusIndex >= len(browser.corpora) {
		return nil
	}
	return browser.corpora[browser.view.corpusIndex]
}

func (browser *Browser) currentDocument() (documentMeta, bool) {
	corpus := browser.currentCorpus()
	if corpus == nil || browser.view.documentID == 0 {
		return documentMeta{}, false
	}
	for _, document := range corpus.documents {
		if document.ID == browser.view.documentID {
			return document, true
		}
	}
	return documentMeta{}, false
}

func (browser *Browser) previewDocument(corpusIndex int, document documentMeta) (renderedPreview, error) {
	if corpusIndex < 0 || corpusIndex >= len(browser.corpora) || browser.previewer == nil {
		return renderedPreview{}, fmt.Errorf("preview is unavailable")
	}
	corpus := browser.corpora[corpusIndex]
	width, height := browser.previewPDFPageSize()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	return browser.previewer.render(ctx, corpus, document, width, height)
}

func (browser *Browser) selectedEntry() *browserEntry {
	if browser.idx < 0 || browser.idx >= len(browser.entries) {
		return nil
	}
	return &browser.entries[browser.idx]
}

func (browser *Browser) setView(next viewState) {
	browser.history = append(browser.history, viewSnapshot{
		view: browser.view, idx: browser.idx, viewTop: browser.viewTop, page: browser.page,
	})
	browser.view = next
	browser.idx = 1
	browser.viewTop = 0
	browser.page = 1
	browser.pages = 0
	browser.buildEntries()
	browser.renderAll()
}

func (browser *Browser) goBack() {
	if len(browser.history) == 0 {
		if browser.view.kind != viewDatabases {
			browser.view = viewState{kind: viewDatabases, corpusIndex: -1}
			browser.idx, browser.viewTop, browser.page = 0, 0, 1
			browser.buildEntries()
			browser.renderAll()
		}
		return
	}
	previous := browser.history[len(browser.history)-1]
	browser.view = previous.view
	browser.history = browser.history[:len(browser.history)-1]
	browser.idx, browser.viewTop, browser.page, browser.pages = previous.idx, previous.viewTop, previous.page, 0
	browser.buildEntries()
	browser.status = "Back"
	browser.renderAll()
}

func (browser *Browser) enterSelected() {
	entry := browser.selectedEntry()
	if entry == nil {
		return
	}
	switch entry.kind {
	case entryParent:
		browser.goBack()
	case entryDatabase:
		browser.status = "Corpora in " + entry.label
		if browser.recentRecorder != nil {
			if err := browser.recentRecorder([]string{browser.corpora[entry.corpusIndex].file}); err != nil {
				browser.status = "Could not update recent databases: " + err.Error()
			}
		}
		browser.setView(viewState{kind: viewCourses, corpusIndex: entry.corpusIndex})
	case entryCourse:
		browser.status = "Documents in " + entry.label
		browser.setView(viewState{kind: viewDocuments, corpusIndex: entry.corpusIndex, course: entry.label})
	case entryDocument:
		browser.status = "Opening " + entry.document.Title
		browser.drawStatus()
		preview, err := browser.previewDocument(entry.corpusIndex, entry.document)
		if err != nil {
			browser.status = "Preview failed: " + err.Error()
			browser.renderAll()
			return
		}
		browser.openedDocumentID = entry.document.ID
		browser.openedPreview = preview
		browser.navOpen = false
		browser.status = "Opened " + entry.document.Title + " — Tab shows contents"
		browser.setView(viewState{
			kind: viewTOC, corpusIndex: entry.corpusIndex, course: entry.document.Course,
			documentID: entry.document.ID,
		})
	case entryTOC:
		browser.page = entry.toc.Page
		browser.status = fmt.Sprintf("Page %d: %s", entry.toc.Page, entry.toc.Title)
		browser.renderAll()
	}
}

func (browser *Browser) ensureVisible() {
	if browser.idx < browser.viewTop {
		browser.viewTop = browser.idx
	}
	if browser.idx >= browser.viewTop+browser.visible {
		browser.viewTop = browser.idx - browser.visible + 1
	}
	maximum := len(browser.entries) - browser.visible
	if maximum < 0 {
		maximum = 0
	}
	if browser.viewTop > maximum {
		browser.viewTop = maximum
	}
	if browser.viewTop < 0 {
		browser.viewTop = 0
	}
}

func (browser *Browser) selectIndex(index int) {
	if len(browser.entries) == 0 {
		return
	}
	if index < 0 {
		index = 0
	}
	if index >= len(browser.entries) {
		index = len(browser.entries) - 1
	}
	if index == browser.idx {
		return
	}
	browser.idx = index
	if entry := browser.selectedEntry(); entry != nil && entry.kind == entryTOC {
		browser.page = entry.toc.Page
	} else {
		browser.page = 1
		browser.pages = 0
	}
	browser.ensureVisible()
	browser.renderAll()
}

func (browser *Browser) moveSelection(delta int) { browser.selectIndex(browser.idx + delta) }

func (browser *Browser) changePage(delta int) {
	entry := browser.selectedEntry()
	if browser.pages < 1 || (browser.view.kind != viewTOC && (entry == nil || entry.kind != entryDocument)) {
		return
	}
	next := browser.page + delta
	if next < 1 {
		next = 1
	}
	if next > browser.pages {
		next = browser.pages
	}
	if next == browser.page {
		return
	}
	browser.page = next
	if browser.view.kind == viewTOC {
		selected := 1
		for index, candidate := range browser.entries {
			if candidate.kind == entryTOC && candidate.toc.Page <= next {
				selected = index
			}
		}
		browser.idx = selected
		browser.ensureVisible()
	}
	browser.renderAll()
}

func (browser *Browser) toggleNavigation() {
	browser.navOpen = !browser.navOpen
	if browser.navOpen {
		browser.status = "Navigation focused"
	} else {
		browser.status = "Preview focused"
	}
	browser.renderAll()
}

func (browser *Browser) searchPrompt() {
	query := []byte{}
	fmt.Fprint(browser.out, "\x1b[?25h")
	defer func() {
		fmt.Fprint(browser.out, "\x1b[?25l")
		browser.out.Flush()
	}()
	for {
		browser.drawPrompt("Search: " + string(query))
		character, status := browser.in.readByte(100 * time.Millisecond)
		if status == inTimeout {
			browser.processSignals()
			if !browser.running {
				return
			}
			continue
		}
		if status == inEOF {
			browser.running = false
			return
		}
		switch character {
		case '\r', '\n':
			value := strings.TrimSpace(string(query))
			if value == "" {
				browser.status = "Search cancelled"
				browser.renderAll()
				return
			}
			corpusIndex := -1
			if browser.view.corpusIndex >= 0 {
				corpusIndex = browser.view.corpusIndex
			}
			browser.status = "Search: " + value
			browser.setView(viewState{kind: viewSearch, corpusIndex: corpusIndex, query: value})
			return
		case 0x1b:
			browser.status = "Search cancelled"
			browser.renderAll()
			return
		case 0x03:
			browser.running = false
			return
		case 0x7f, '\b':
			if len(query) > 0 {
				_, size := utf8.DecodeLastRune(query)
				if size < 1 {
					size = 1
				}
				query = query[:len(query)-size]
			}
		default:
			if character >= 0x20 {
				query = append(query, character)
			}
		}
	}
}

func (browser *Browser) handleKey(character byte) {
	switch character {
	case 'q', 'Q':
		browser.running = false
	case '\t':
		browser.toggleNavigation()
	case '[', '{':
		browser.changePage(-1)
	case ']', '}':
		browser.changePage(1)
	case 0x1b:
		next, status := browser.in.readByte(50 * time.Millisecond)
		if status != inData {
			browser.running = false
			return
		}
		if next != '[' {
			return
		}
		first, status := browser.in.readByte(50 * time.Millisecond)
		if status != inData {
			return
		}
		browser.handleCSI(first)
	default:
		if !browser.navOpen {
			return
		}
		browser.handleNavigationKey(character)
	}
}

func (browser *Browser) handleNavigationKey(character byte) {
	switch character {
	case '/', 's', 'S':
		browser.searchPrompt()
	case 'r', 'R':
		browser.status = "Refreshed"
		browser.buildEntries()
		browser.renderAll()
	case '\r', '\n':
		browser.enterSelected()
	case 0x7f, '\b':
		browser.goBack()
	}
}

func (browser *Browser) handleCSI(first byte) {
	if first == '<' {
		sequence := browser.readCSI(first)
		match := sgrMousePattern.FindStringSubmatch(strings.TrimPrefix(sequence, "<"))
		if match != nil {
			browser.mouseEvent(atoi(match[1]), atoi(match[2]), atoi(match[3]), match[4][0])
		}
		return
	}
	if !browser.navOpen {
		switch first {
		case 'A', 'D':
			browser.changePage(-1)
		case 'B', 'C':
			browser.changePage(1)
		case 'H':
			browser.changePage(1 - browser.page)
		case 'F':
			browser.changePage(browser.pages - browser.page)
		default:
			if first >= '0' && first <= '9' {
				sequence := browser.readCSI(first)
				switch sequence {
				case "1~", "5~", "7~":
					browser.changePage(-1)
				case "4~", "6~", "8~":
					browser.changePage(1)
				}
			}
		}
		return
	}
	switch first {
	case 'A':
		browser.moveSelection(-1)
	case 'B':
		browser.moveSelection(1)
	case 'C':
		browser.enterSelected()
	case 'D':
		browser.goBack()
	case 'H':
		browser.selectIndex(0)
	case 'F':
		browser.selectIndex(len(browser.entries) - 1)
	default:
		if first >= '0' && first <= '9' {
			sequence := browser.readCSI(first)
			switch sequence {
			case "1~", "7~":
				browser.selectIndex(0)
			case "4~", "8~":
				browser.selectIndex(len(browser.entries) - 1)
			case "5~":
				browser.moveSelection(-browser.visible)
			case "6~":
				browser.moveSelection(browser.visible)
			}
		}
	}
}

func (browser *Browser) mouseEvent(code, x, y int, final byte) {
	if final != 'M' {
		return
	}
	button := code & 3
	if code&64 != 0 {
		if (!browser.navOpen || x < browser.listX) && browser.pages > 1 {
			if button == 0 {
				browser.changePage(-1)
			} else if button == 1 {
				browser.changePage(1)
			}
		} else if button == 0 {
			browser.moveSelection(-3)
		} else if button == 1 {
			browser.moveSelection(3)
		}
		return
	}
	if !browser.navOpen {
		return
	}
	if button == 2 {
		browser.goBack()
		return
	}
	if button != 0 || x < browser.listX || y < browser.listTop || y > browser.listBottom {
		return
	}
	index := browser.viewTop + y - browser.listTop
	if index < 0 || index >= len(browser.entries) {
		return
	}
	doubleClick := index == browser.lastClickIdx && time.Since(browser.lastClickAt) <= 350*time.Millisecond
	browser.selectIndex(index)
	browser.lastClickIdx = index
	browser.lastClickAt = time.Now()
	if doubleClick {
		browser.enterSelected()
	}
}

func (browser *Browser) loop() {
	browser.running = true
	for browser.running {
		browser.processSignals()
		if browser.resized {
			browser.resized = false
			browser.status = "Resized"
			browser.renderAll()
		}
		character, status := browser.in.readByte(100 * time.Millisecond)
		switch status {
		case inData:
			browser.handleKey(character)
		case inEOF:
			browser.running = false
		}
	}
}

func (browser *Browser) processSignals() {
	for {
		select {
		case signal := <-browser.sigCh:
			switch signal {
			case os.Interrupt:
				browser.running = false
			case unix.SIGWINCH:
				browser.resized = true
			default:
				browser.running = false
			}
		default:
			return
		}
	}
}
