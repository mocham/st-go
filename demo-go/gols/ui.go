package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"unicode"
	"unicode/utf8"

	"golang.org/x/sys/unix"
)

func safeText(value string) string {
	var output strings.Builder
	for len(value) > 0 {
		r, size := utf8.DecodeRuneInString(value)
		if r == utf8.RuneError && size == 1 {
			value = value[1:]
			continue
		}
		value = value[size:]
		if unicode.IsControl(r) {
			if unicode.IsSpace(r) {
				output.WriteByte(' ')
			}
			continue
		}
		output.WriteRune(r)
	}
	return output.String()
}

func shorten(value string, width int) string {
	if width <= 0 {
		return ""
	}
	runes := []rune(safeText(value))
	if len(runes) <= width {
		return string(runes)
	}
	if width <= 3 {
		return string(runes[:width])
	}
	return string(runes[:width-3]) + "..."
}

func (browser *Browser) terminalSize() {
	rows, columns := 24, 80
	pixelWidth, pixelHeight := 0, 0
	if size, err := unix.IoctlGetWinsize(browser.fd, unix.TIOCGWINSZ); err == nil {
		if size.Row > 0 {
			rows = int(size.Row)
		}
		if size.Col > 0 {
			columns = int(size.Col)
		}
		pixelWidth, pixelHeight = int(size.Xpixel), int(size.Ypixel)
	}
	browser.rows, browser.cols = rows, columns
	browser.cellPixelW, browser.cellPixelH = 8, 16
	if pixelWidth > 0 {
		browser.cellPixelW = float64(pixelWidth) / float64(columns)
	}
	if pixelHeight > 0 {
		browser.cellPixelH = float64(pixelHeight) / float64(rows)
	}
	browser.compact = rows < 12 || columns < 52
	if browser.compact {
		return
	}
	listWidth := 42
	if listWidth > columns/2 {
		listWidth = columns / 2
	}
	if listWidth < 28 {
		listWidth = 28
	}
	browser.listW = listWidth
	browser.listX = columns - listWidth + 1
	browser.previewW = browser.navigation.PreviewWidth(columns, browser.listX)
	browser.listTop = 9
	browser.listBottom = rows - 2
	browser.visible = browser.listBottom - browser.listTop + 1
	if browser.visible < 1 {
		browser.visible = 1
	}
}

func (browser *Browser) previewPDFPageSize() (float64, float64) {
	heightCells := browser.rows - 3
	if heightCells < 1 {
		heightCells = 1
	}
	const millimetresPerPixel = 25.4 / 96.0
	width := float64(browser.previewW) * browser.cellPixelW * millimetresPerPixel
	height := float64(heightCells) * browser.cellPixelH * millimetresPerPixel
	if width <= 0 || height <= 0 {
		return 0, 0
	}
	scale := 1.0
	if width < 60 {
		scale = 60 / width
	}
	if height*scale < 60 {
		scale = 60 / height
	}
	return width * scale, height * scale
}

func (browser *Browser) cup(row, column int) {
	fmt.Fprintf(browser.out, "\x1b[%d;%dH", row, column)
}

func (browser *Browser) dcs(command string) {
	fmt.Fprintf(browser.out, "\x1bP%s\x1b\\", command)
}

func (browser *Browser) drawText(row, column, width int, style, text string) {
	if row < 1 || row > browser.rows || width < 1 {
		return
	}
	browser.cup(row, column)
	fmt.Fprintf(browser.out, "\x1b[%dX%s%s%s", width, style, shorten(text, width), resetStyle)
}

func (browser *Browser) viewLabel() string {
	switch browser.view.kind {
	case viewDatabases:
		if browser.recentMode {
			return "recent databases"
		}
	case viewCourses:
		if corpus := browser.currentCorpus(); corpus != nil {
			return corpus.label
		}
	case viewDocuments:
		if corpus := browser.currentCorpus(); corpus != nil {
			return corpus.label + " / " + browser.view.course
		}
	case viewTOC:
		if corpus := browser.currentCorpus(); corpus != nil {
			if document, ok := browser.currentDocument(); ok {
				return corpus.label + " / " + document.Course + " / " + document.Title
			}
		}
	case viewSearch:
		return "search / " + browser.view.query
	}
	return "databases"
}

func (browser *Browser) drawHeader() {
	fmt.Fprintf(browser.out, "\x1b[1;1H%s%*s\x1b[1;2H", headerStyle, browser.cols, "")
	fmt.Fprint(browser.out, shorten("GoLS corpus  "+browser.viewLabel(), browser.cols-15))
	fmt.Fprintf(browser.out, "\x1b[1;%dH%4d items%s", browser.cols-11, len(browser.entries), resetStyle)
}

func (browser *Browser) drawInfo() {
	browser.drawText(2, browser.listX, browser.listW, infoStyle, " SELECTED")
	entry := browser.selectedEntry()
	if entry == nil {
		browser.drawText(3, browser.listX, browser.listW, selectedStyle, " (empty)")
		for row := 4; row <= 7; row++ {
			browser.drawText(row, browser.listX, browser.listW, resetStyle, "")
		}
		return
	}
	browser.drawText(3, browser.listX, browser.listW, selectedStyle, " "+entry.label)
	switch entry.kind {
	case entryParent:
		browser.drawText(4, browser.listX, browser.listW, resetStyle, " type  parent")
		browser.drawText(5, browser.listX, browser.listW, resetStyle, " Enter to go back")
		browser.drawText(6, browser.listX, browser.listW, dimStyle, "")
	case entryDatabase:
		corpus := browser.corpora[entry.corpusIndex]
		browser.drawText(4, browser.listX, browser.listW, resetStyle, fmt.Sprintf(" type  database   %d documents", entry.count))
		browser.drawText(5, browser.listX, browser.listW, resetStyle, " file  "+filepath.Base(corpus.file))
		browser.drawText(6, browser.listX, browser.listW, dimStyle, " path  "+corpus.file)
	case entryCourse:
		browser.drawText(4, browser.listX, browser.listW, resetStyle, fmt.Sprintf(" type  corpus   %d documents", entry.count))
		browser.drawText(5, browser.listX, browser.listW, resetStyle, " corpus  "+browser.corpora[entry.corpusIndex].label)
		browser.drawText(6, browser.listX, browser.listW, dimStyle, " Enter to browse")
	case entryDocument:
		document := entry.document
		browser.drawText(4, browser.listX, browser.listW, resetStyle, " type  "+document.Kind+"   "+document.Status)
		browser.drawText(5, browser.listX, browser.listW, resetStyle, " size  "+humanSize(document.Size)+"   "+document.Extension)
		browser.drawText(6, browser.listX, browser.listW, dimStyle, " path  "+document.Path)
	case entryTOC:
		browser.drawText(4, browser.listX, browser.listW, resetStyle, fmt.Sprintf(" type  contents   page %d", entry.toc.Page))
		browser.drawText(5, browser.listX, browser.listW, resetStyle, " section  "+entry.toc.Number)
		browser.drawText(6, browser.listX, browser.listW, dimStyle, " Enter to jump")
	}
	first, last := 0, 0
	if len(browser.entries) > 0 {
		first = browser.viewTop + 1
		last = browser.viewTop + browser.visible
		if last > len(browser.entries) {
			last = len(browser.entries)
		}
	}
	browser.drawText(7, browser.listX, browser.listW, infoStyle, fmt.Sprintf(" ITEMS  %d-%d / %d", first, last, len(browser.entries)))
}

func humanSize(size int64) string {
	if size < 1024 {
		return fmt.Sprintf("%d B", size)
	}
	if size < 1024*1024 {
		return fmt.Sprintf("%.1f KiB", float64(size)/1024)
	}
	return fmt.Sprintf("%.1f MiB", float64(size)/(1024*1024))
}

func (browser *Browser) entryLabel(entry browserEntry) string {
	switch entry.kind {
	case entryParent:
		return "↑  .."
	case entryDatabase:
		return fmt.Sprintf("▦  %s  (%d)", entry.label, entry.count)
	case entryCourse:
		return fmt.Sprintf("▸  %s  (%d)", entry.label, entry.count)
	case entryDocument:
		icon := "≡"
		if strings.EqualFold(entry.document.Extension, ".tex") {
			icon = "λ"
		}
		label := entry.document.Title
		if browser.view.kind == viewSearch && len(browser.corpora) > 1 {
			label = browser.corpora[entry.corpusIndex].label + " / " + label
		}
		return icon + "  " + label
	case entryTOC:
		indent := strings.Repeat("  ", max(0, entry.toc.Level-1))
		number := ""
		if entry.toc.Number != "" {
			number = entry.toc.Number + "  "
		}
		return fmt.Sprintf("%s•  %s%s  · %d", indent, number, entry.toc.Title, entry.toc.Page)
	}
	return entry.label
}

func (browser *Browser) entryStyle(entry browserEntry) string {
	switch entry.kind {
	case entryParent, entryDatabase, entryCourse:
		return directoryStyle
	case entryTOC:
		return infoStyle
	case entryDocument:
		switch entry.document.Status {
		case "current":
			return currentStyle
		case "obsolete":
			return obsoleteStyle
		default:
			return documentStyle
		}
	default:
		return resetStyle
	}
}

func (browser *Browser) drawList() {
	for slot := 0; slot < browser.visible; slot++ {
		row := browser.listTop + slot
		index := browser.viewTop + slot
		if index >= len(browser.entries) {
			browser.drawText(row, browser.listX, browser.listW, resetStyle, "")
			continue
		}
		entry := browser.entries[index]
		style := browser.entryStyle(entry)
		if index == browser.idx {
			style = selectedStyle
		}
		browser.drawText(row, browser.listX, browser.listW, style, " "+browser.entryLabel(entry))
	}
}

func (browser *Browser) drawDivider() {
	column := browser.listX - 1
	for row := 2; row < browser.rows; row++ {
		browser.cup(row, column)
		fmt.Fprintf(browser.out, "%s|%s", dimStyle, resetStyle)
	}
}

func (browser *Browser) drawStatus() {
	help := " Tab nav  [ ] page  / text  n/N match  q"
	if browser.navigation.Visible {
		help = " Tab nav  arrows Enter  / text  n/N match  s corpus"
	}
	if len(browser.searchPromptBuffer) > 0 {
		browser.drawPrompt(string(browser.searchPromptBuffer))
		return
	}
	if browser.documentSearch.visible {
		help = " n/N match  / search  Esc clear  Tab nav"
	}
	fmt.Fprintf(browser.out, "\x1b[%d;1H%s%*s\x1b[%d;2H", browser.rows, statusStyle, browser.cols, "", browser.rows)
	width := browser.cols - 3
	if browser.cols >= 78 {
		width -= len(help)
	}
	fmt.Fprint(browser.out, shorten(browser.status, width))
	if browser.cols >= 78 {
		fmt.Fprintf(browser.out, "\x1b[%d;%dH%s", browser.rows, browser.cols-len(help)+1, help)
	}
	fmt.Fprint(browser.out, resetStyle)
	browser.out.Flush()
}

func (browser *Browser) drawPrompt(prompt string) {
	fmt.Fprintf(browser.out, "\x1b[%d;1H%s%*s\x1b[%d;2H%s%s", browser.rows, statusStyle, browser.cols, "", browser.rows, shorten(prompt, browser.cols-3), resetStyle)
	browser.out.Flush()
}

func (browser *Browser) clearPreview() {
	for row := 2; row < browser.rows; row++ {
		browser.cup(row, 1)
		fmt.Fprintf(browser.out, "\x1b[%dX", browser.previewW)
	}
}

func (browser *Browser) drawPreviewTitle(title string) {
	browser.drawText(2, 2, browser.previewW-2, infoStyle, title)
}

func (browser *Browser) drawPreviewMessage(title, message, extra string) {
	browser.drawPreviewTitle(title)
	browser.drawText(4, 3, browser.previewW-4, resetStyle, message)
	if extra != "" {
		browser.drawText(6, 3, browser.previewW-4, dimStyle, extra)
	}
}

func (browser *Browser) setPreviewLink(target string) {
	_ = os.Remove(browser.previewLink)
	_ = os.Symlink(target, browser.previewLink)
}

func (browser *Browser) renderPreview() {
	browser.clearPreview()
	browser.pages = 0
	if browser.view.kind == viewTOC {
		document, ok := browser.currentDocument()
		if !ok {
			browser.drawPreviewMessage("MISSING", "Document is no longer available.", "")
			return
		}
		if browser.openedDocumentID != document.ID || browser.openedPreview.path == "" {
			browser.drawPreviewMessage("NOT OPEN", document.Title, "Return to documents and press Enter to open it.")
			return
		}
		browser.displayDocumentPreview(document, browser.openedPreview)
		return
	}
	entry := browser.selectedEntry()
	if entry == nil {
		if browser.view.kind == viewDatabases && browser.recentMode {
			browser.drawPreviewMessage("RECENT DATABASES", "No recent databases.", "Run gols DATABASE to add one.")
			return
		}
		browser.drawPreviewMessage("EMPTY", "No entries in this view.", "")
		return
	}
	switch entry.kind {
	case entryParent:
		browser.drawPreviewMessage("PARENT", "Return to the previous level.", "Press Enter, Backspace, or Left.")
	case entryDatabase:
		corpus := browser.corpora[entry.corpusIndex]
		browser.drawPreviewMessage("DATABASE", corpus.label, corpus.description)
		if corpus.generated != "" {
			browser.drawText(8, 3, browser.previewW-4, dimStyle, "Generated "+corpus.generated)
		}
	case entryCourse:
		browser.drawPreviewMessage("CORPUS", entry.label, fmt.Sprintf("%d documents. Press Enter to browse.", entry.count))
	case entryDocument:
		preview, err := browser.previewDocument(entry.corpusIndex, entry.document)
		if err != nil {
			browser.drawPreviewMessage("PREVIEW FAILED", entry.document.Title, err.Error())
			return
		}
		browser.displayDocumentPreview(entry.document, preview)
	}
}

func (browser *Browser) displayDocumentPreview(document documentMeta, preview renderedPreview) {
	browser.previewDrawn = true
	if browser.documentSearch.path != "" && browser.documentSearch.path != preview.path {
		browser.clearDocumentSearch()
	}
	browser.displayedPreview = preview
	index := browser.view.corpusIndex
	if entry := browser.selectedEntry(); browser.view.kind != viewTOC && entry != nil {
		index = entry.corpusIndex
	}
	if index >= 0 && index < len(browser.corpora) {
		browser.displayedSource = previewSourceID{corpus: browser.corpora[index].file, id: document.ID}
	}
	browser.pages = preview.pageCount
	if browser.pages < 1 {
		browser.pages = 1
	}
	if browser.page > browser.pages {
		browser.page = browser.pages
	}
	if browser.page < 1 {
		browser.page = 1
	}
	browser.setPreviewLink(preview.path)
	heightCells := browser.rows - 3
	if heightCells < 1 {
		heightCells = 1
	}
	if preview.pdf {
		title := fmt.Sprintf("TEX  %d/%d  %s", browser.page, browser.pages, document.Title)
		if preview.mathErrors > 0 {
			title += fmt.Sprintf("  (%d math errors)", preview.mathErrors)
		}
		browser.drawPreviewTitle(title)
		searchOptions := ""
		if search := &browser.documentSearch; search.visible && !search.pending && search.path == preview.path && len(search.pages) > 0 {
			searchOptions = fmt.Sprintf(" pdfsearch %d %d", search.token, search.active+1)
		}
		browser.dcs(fmt.Sprintf("open '%s' rect 1 3 %d %d fit-height page %d%s", browser.previewLink, browser.previewW, heightCells, browser.page, searchOptions))
	} else {
		browser.drawPreviewTitle("SOURCE  " + document.Title)
		browser.dcs(fmt.Sprintf("open '%s' rect 2 3 %d %d", browser.previewLink, browser.previewW-3, heightCells))
	}
}

func (browser *Browser) renderAll() {
	fmt.Fprint(browser.out, "\x1b[?2026h")
	browser.terminalSize()
	if browser.compact {
		fmt.Fprintf(browser.out, "\x1b[2J\x1b[H%s enlarge window %s", headerStyle, resetStyle)
		fmt.Fprint(browser.out, "\x1b[?2026l")
		browser.out.Flush()
		return
	}
	browser.ensureVisible()
	fmt.Fprint(browser.out, "\x1b[2J")
	browser.previewDrawn = false
	browser.renderPreview()
	if !browser.previewDrawn {
		browser.displayedPreview = renderedPreview{}
		browser.clearDocumentSearch()
	}
	browser.drawHeader()
	if browser.navigation.Visible {
		browser.drawInfo()
		browser.drawList()
		browser.drawDivider()
	}
	browser.drawStatus()
	fmt.Fprint(browser.out, "\x1b[?2026l")
	browser.out.Flush()
}
