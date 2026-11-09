package main

import (
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"
	"unicode"

	"golang.org/x/text/width"
)

const baseStyle = "\x1b[0;38;5;223;48;5;236m"
const currentLineStyle = "\x1b[0;38;5;223;48;5;237m"
const numberStyle = "\x1b[0;38;5;244;48;5;236m"
const statusStyle = "\x1b[0;38;5;231;48;5;239m"
const activeStatusStyle = "\x1b[1;38;5;223;48;5;240m"
const selectionStyle = "\x1b[0;38;5;231;48;5;60m"
const searchStyle = "\x1b[0;30;48;5;220m"
const activeSearchStyle = "\x1b[0;30;48;5;214m"

type screenCell struct {
	text               string
	pos, column, width int
}
type screenRow struct {
	line, start, end, segment int
	cells                     []screenCell
}
type tile struct {
	text, style  string
	continuation bool
}
type renderer struct {
	previous   []string
	rows, cols int
}

func runeCells(r rune, column int) int {
	if r == '\t' {
		return 4 - column%4
	}
	if r < 32 || r == 127 {
		return 2
	}
	if unicode.Is(unicode.Mn, r) || unicode.Is(unicode.Me, r) || r == 0x200d {
		return 0
	}
	if unicode.IsControl(r) {
		return 1
	}
	switch width.LookupRune(r).Kind() {
	case width.EastAsianWide, width.EastAsianFullwidth, width.EastAsianAmbiguous:
		return 2
	}
	return 1
}
func nativeCells(r rune) int {
	switch width.LookupRune(r).Kind() {
	case width.EastAsianWide, width.EastAsianFullwidth:
		return 2
	}
	return 1
}
func logicalColumn(line []rune, offset int) int {
	column := 0
	for _, r := range line[:clamp(offset, 0, len(line))] {
		column += runeCells(r, column)
	}
	return column
}
func columnOffset(line []rune, column int) int {
	at := 0
	for i, r := range line {
		w := runeCells(r, at)
		if at+w > column {
			return i
		}
		at += w
	}
	return len(line)
}
func numberWidth(b *Buffer) int { return max(3, len(strconv.Itoa(len(b.lineStarts())))) }

func visibleRune(r rune, column int) (string, int) {
	w := runeCells(r, column)
	if r == '\t' {
		return strings.Repeat(" ", w), w
	}
	if r < 32 {
		return "^" + string(r+64), 2
	}
	if r == 127 {
		return "^?", 2
	}
	if unicode.IsControl(r) {
		return "�", 1
	}
	if w == 0 {
		return string(r), 0
	}
	text := string(r)
	if w > nativeCells(r) {
		text += strings.Repeat(" ", w-nativeCells(r))
	}
	return text, w
}

func lineScreen(b *Buffer, row, textWidth, skip, limit int) ([]screenRow, int) {
	textWidth = max(1, textWidth)
	line := b.line(row)
	base := b.start(row)
	segment, column, logical := 0, 0, 0
	start := base
	var rows []screenRow
	var cells []screenCell
	finish := func(end int) {
		if segment >= skip && len(rows) < limit {
			rows = append(rows, screenRow{line: row, start: start, end: end, segment: segment, cells: cells})
		}
		cells = nil
		segment++
		column = 0
		start = end
	}
	for i, r := range line {
		text, w := visibleRune(r, logical)
		if w > textWidth {
			text, w = "?", 1
		}
		if w == 0 {
			if segment >= skip && len(cells) > 0 {
				cells[len(cells)-1].text += text
			}
			continue
		}
		if column+w > textWidth {
			finish(base + i)
		}
		if segment >= skip && len(rows) < limit {
			cells = append(cells, screenCell{text, base + i, column, w})
		}
		column += w
		logical += runeCells(r, logical)
		if column == textWidth && i+1 < len(line) {
			finish(base + i + 1)
		}
	}
	finish(base + len(line))
	return rows, segment
}
func cursorSegment(b *Buffer, row, pos, textWidth int) (int, int) {
	textWidth = max(1, textWidth)
	line := b.line(row)
	offset := clamp(pos-b.start(row), 0, len(line))
	segment, column, logical := 0, 0, 0
	for i, r := range line {
		if i == offset {
			break
		}
		w := min(textWidth, runeCells(r, logical))
		if column+w > textWidth {
			segment++
			column = 0
		}
		column += w
		logical += runeCells(r, logical)
		if column == textWidth {
			segment++
			column = 0
		}
	}
	if offset < len(line) && column+min(textWidth, runeCells(line[offset], logical)) > textWidth {
		segment++
		column = 0
	}
	return segment, column
}

func (e *Editor) viewport(w *Window) {
	b := w.buffer
	height := max(1, w.rect.h-1)
	textWidth := max(1, w.rect.w-numberWidth(b)-1)
	w.cursor = clamp(w.cursor, 0, len(b.text))
	row := b.row(w.cursor)
	segment, _ := cursorSegment(b, row, w.cursor, textWidth)
	w.topLine = clamp(w.topLine, 0, len(b.lineStarts())-1)
	w.topWrap = max(0, w.topWrap)
	if row < w.topLine || (row == w.topLine && segment < w.topWrap) {
		w.topLine, w.topWrap = row, segment
	}
	distance := segment - w.topWrap
	for r := w.topLine; r < row; r++ {
		_, n := lineScreen(b, r, textWidth, 0, 0)
		distance += n
	}
	for distance >= height {
		_, n := lineScreen(b, w.topLine, textWidth, 0, 0)
		remaining := n - w.topWrap
		if distance-height+1 < remaining {
			w.topWrap += distance - height + 1
			break
		}
		distance -= remaining
		w.topLine++
		w.topWrap = 0
		if w.topLine > row {
			w.topLine = row
			break
		}
	}
	w.rows = nil
	for r := w.topLine; r < len(b.lineStarts()) && len(w.rows) < height; r++ {
		skip := 0
		if r == w.topLine {
			skip = w.topWrap
		}
		rows, _ := lineScreen(b, r, textWidth, skip, height-len(w.rows))
		w.rows = append(w.rows, rows...)
		if r == row && e.window() == w && (e.mode == insertMode || e.mode == replaceMode) && segment >= len(rows)+skip && len(w.rows) < height {
			w.rows = append(w.rows, screenRow{line: r, start: b.end(r), end: b.end(r), segment: segment})
		}
	}
}

func putCanvas(canvas [][]tile, x, y int, text, style string, limit int) {
	if y < 0 || y >= len(canvas) {
		return
	}
	column := 0
	previous := -1
	for _, r := range text {
		value, w := visibleRune(r, column)
		if w == 0 {
			if previous >= 0 && previous < len(canvas[y]) {
				canvas[y][previous].text += value
			}
			continue
		}
		if column+w > limit {
			break
		}
		if x+column >= 0 && x+column+w <= len(canvas[y]) {
			canvas[y][x+column] = tile{text: value, style: style}
			previous = x + column
			for i := 1; i < w; i++ {
				canvas[y][x+column+i] = tile{continuation: true, style: style}
			}
		}
		column += w
	}
}

func (e *Editor) draw(r *renderer, out io.Writer) error {
	if len(e.tabs) == 0 {
		return nil
	}
	e.pollSyntax()
	if e.fullRedraw {
		r.previous = nil
		e.fullRedraw = false
	}
	e.rows, e.cols = clamp(e.rows, 2, 500), clamp(e.cols, 8, 1000)
	canvas := make([][]tile, e.rows)
	for y := range canvas {
		canvas[y] = make([]tile, e.cols)
	}
	top := 0
	if len(e.tabs) > 1 {
		var labels strings.Builder
		for i := range e.tabs {
			if i == e.activeTab {
				fmt.Fprintf(&labels, " [%d] ", i+1)
			} else {
				fmt.Fprintf(&labels, "  %d  ", i+1)
			}
		}
		putCanvas(canvas, 0, 0, labels.String(), statusStyle, e.cols)
		top = 1
	}
	layoutPane(e.tabs[e.activeTab].root, rectangle{0, top, e.cols, max(1, e.rows-top-1)})
	if err := e.refreshSearch(); err != nil {
		e.message = err.Error()
		e.search.highlight = false
	}
	matchParen := -1
	b := e.buffer()
	pos := e.pos()
	if pos < len(b.text) {
		if at, ok := matchingBracket(b.text, pos); ok {
			matchParen = at
		}
	}
	cursorX, cursorY := 0, 0
	for _, w := range e.windows() {
		tokens := e.syntaxTokens(w.buffer)
		e.viewport(w)
		gutter := numberWidth(w.buffer) + 1
		textWidth := max(1, w.rect.w-gutter)
		for slot := 0; slot < max(0, w.rect.h-1); slot++ {
			y := w.rect.y + slot
			if y < 0 || y >= e.rows-1 {
				continue
			}
			if slot >= len(w.rows) {
				putCanvas(canvas, w.rect.x, y, "~", numberStyle, w.rect.w)
				continue
			}
			row := w.rows[slot]
			style := baseStyle
			if w == e.window() && row.line == b.row(e.pos()) {
				style = currentLineStyle
			}
			for x := max(0, w.rect.x); x < min(e.cols, w.rect.x+w.rect.w); x++ {
				canvas[y][x].style = style
			}
			number := ""
			if row.segment == 0 {
				number = fmt.Sprintf("%*d", gutter-1, row.line+1)
			}
			putCanvas(canvas, w.rect.x, y, number, numberStyle, gutter)
			tokenIndex := sort.Search(len(tokens), func(i int) bool { return tokens[i].end > row.start })
			for _, cell := range row.cells {
				cellStyle := style
				for tokenIndex < len(tokens) && tokens[tokenIndex].end <= cell.pos {
					tokenIndex++
				}
				if tokenIndex < len(tokens) && tokens[tokenIndex].start <= cell.pos {
					cellStyle = syntaxCellStyle(tokens[tokenIndex].style, style == currentLineStyle)
				}
				if e.search.highlight && w.buffer == e.search.buffer {
					index := sort.Search(len(e.search.matches), func(i int) bool { return e.search.matches[i].start > cell.pos }) - 1
					if index >= 0 {
						match := e.search.matches[index]
						if cell.pos >= match.start && cell.pos < max(match.end, match.start+1) {
							cellStyle = searchStyle
							if match == e.search.active {
								cellStyle = activeSearchStyle
							}
						}
					}
				}
				if w.buffer == b && e.selectedCell(cell.pos) {
					cellStyle = selectionStyle
				}
				if w.buffer == b && cell.pos == matchParen {
					cellStyle = "\x1b[0;4;38;5;250;48;5;237m"
				}
				x := w.rect.x + gutter + cell.column
				if x >= e.cols || x+cell.width > min(e.cols, w.rect.x+w.rect.w) {
					continue
				}
				canvas[y][x] = tile{text: cell.text, style: cellStyle}
				for offset := 1; offset < cell.width; offset++ {
					canvas[y][x+offset] = tile{continuation: true, style: cellStyle}
				}
			}
			if w == e.window() && row.line == b.row(e.pos()) {
				segment, col := cursorSegment(b, row.line, e.pos(), textWidth)
				if row.segment == segment {
					cursorX, cursorY = w.rect.x+gutter+col, y
				}
			}
		}
		statusY := w.rect.y + w.rect.h - 1
		if statusY >= 0 && statusY < e.rows-1 {
			status := fileLabel(w.buffer)
			if w.buffer.dirty() {
				status += " [+]"
			}
			if w.buffer.readonly {
				status += " [RO]"
			}
			status += " " + w.buffer.meta.Encoding
			position := fmt.Sprintf("%d,%d", w.buffer.row(w.cursor)+1, logicalColumn(w.buffer.line(w.buffer.row(w.cursor)), w.cursor-w.buffer.start(w.buffer.row(w.cursor)))+1)
			style := statusStyle
			if w == e.window() {
				style = activeStatusStyle
			}
			for x := max(0, w.rect.x); x < min(e.cols, w.rect.x+w.rect.w); x++ {
				canvas[statusY][x].style = style
			}
			putCanvas(canvas, w.rect.x, statusY, truncateCells(status, max(0, w.rect.w-len(position)-2)), style, w.rect.w)
			putCanvas(canvas, w.rect.x+max(0, w.rect.w-len(position)-1), statusY, position, style, len(position))
		}
	}
	message := e.message
	if e.mode == insertMode {
		message = "-- INSERT -- " + message
	}
	if e.mode == replaceMode {
		message = "-- REPLACE -- " + message
	}
	if e.visual() {
		message = "-- VISUAL -- " + message
	}
	if e.recording != 0 {
		message += " recording @" + string(e.recording)
	}
	if e.prompt != "" {
		message, cursorX = e.promptLine(e.cols - 1)
		cursorY = e.rows - 1
	}
	putCanvas(canvas, 0, e.rows-1, truncateCells(message, e.cols-1), baseStyle, e.cols-1)
	if len(e.pager) > 0 {
		for y := range canvas {
			for x := range canvas[y] {
				canvas[y][x] = tile{}
			}
		}
		for i, line := range e.pager {
			if i >= e.rows-1 {
				break
			}
			putCanvas(canvas, 0, i, truncateCells(line, e.cols), baseStyle, e.cols)
		}
		putCanvas(canvas, 0, e.rows-1, "Press any key to continue", statusStyle, e.cols)
		cursorX, cursorY = 0, e.rows-1
	}
	lines := make([]string, e.rows)
	for y, row := range canvas {
		var line strings.Builder
		style := ""
		for _, cell := range row {
			if cell.continuation {
				continue
			}
			next := cell.style
			if next == "" {
				next = baseStyle
			}
			if next != style {
				line.WriteString(next)
				style = next
			}
			if cell.text == "" {
				line.WriteByte(' ')
			} else {
				line.WriteString(cell.text)
			}
		}
		line.WriteString("\x1b[0m")
		lines[y] = line.String()
	}
	var output strings.Builder
	output.WriteString("\x1b[?2026h\x1b[?25l")
	if r.rows != e.rows || r.cols != e.cols {
		output.WriteString("\x1b[2J")
		r.previous = nil
		r.rows, r.cols = e.rows, e.cols
	}
	for y, line := range lines {
		if y >= len(r.previous) || r.previous[y] != line {
			fmt.Fprintf(&output, "\x1b[%d;1H%s", y+1, line)
		}
	}
	shape := 2
	if e.mode == insertMode || e.prompt != "" {
		shape = 6
	}
	if e.mode == replaceMode {
		shape = 4
	}
	fmt.Fprintf(&output, "\x1b[%d q\x1b[%d;%dH\x1b[?25h\x1b[?2026l", shape, clamp(cursorY, 0, e.rows-1)+1, clamp(cursorX, 0, e.cols-1)+1)
	if err := writeAll(out, []byte(output.String())); err != nil {
		return err
	}
	r.previous = lines
	e.dirty = false
	return nil
}

func (e *Editor) selectedCell(pos int) bool {
	if !e.visual() {
		return false
	}
	b := e.buffer()
	if e.mode == visualBlockMode {
		first, last, left, right := e.blockBounds()
		row := b.row(pos)
		col := logicalColumn(b.line(row), pos-b.start(row))
		return row >= first && row <= last && col >= left && col < right
	}
	region, _ := e.visualSpan()
	return pos >= region.start && pos < region.end
}
func truncateCells(text string, limit int) string {
	if limit <= 0 {
		return ""
	}
	var out strings.Builder
	column := 0
	for _, r := range text {
		_, w := visibleRune(r, column)
		if column+w > limit {
			break
		}
		out.WriteRune(r)
		column += w
	}
	return out.String()
}
