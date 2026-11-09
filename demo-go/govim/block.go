package main

import (
	"fmt"
	"strings"
	"unicode"
)

func expandTabs(line []rune) []rune {
	var out []rune
	column := 0
	for _, r := range line {
		if r == '\t' {
			n := 4 - column%4
			out = append(out, []rune(strings.Repeat(" ", n))...)
			column += n
		} else {
			out = append(out, r)
			column += runeCells(r, column)
		}
	}
	return out
}
func (e *Editor) blockBounds() (first, last, left, right int) {
	b := e.buffer()
	first, last = b.row(e.anchor), b.row(e.pos())
	if last < first {
		first, last = last, first
	}
	a := logicalColumn(b.line(b.row(e.anchor)), e.anchor-b.start(b.row(e.anchor)))
	c := logicalColumn(b.line(b.row(e.pos())), e.pos()-b.start(b.row(e.pos())))
	if e.window().goal >= 0 {
		c = max(c, e.window().goal)
	}
	left, right = min(a, c), max(a, c)+1
	return
}

func (e *Editor) blockOperation(op string) {
	b := e.buffer()
	first, last, left, right := e.blockBounds()
	var copied []string
	var edits []patch
	for row := first; row <= last; row++ {
		line := expandTabs(b.line(row))
		a, z := columnOffset(line, left), columnOffset(line, right)
		part := string(line[a:z])
		copied = append(copied, part)
	}
	if op == "y" || op == "d" || op == "x" || op == "c" || op == "s" {
		e.storeRegister(register{Text: strings.Join(copied, "\n"), Block: true, Width: right - left}, op == "y", true)
	}
	if op == "y" {
		e.mode = normalMode
		e.setPos(b.start(first) + columnOffset(b.line(first), left))
		return
	}
	for row := last; row >= first; row-- {
		old := string(b.line(row))
		line := expandTabs(b.line(row))
		a, z := columnOffset(line, left), columnOffset(line, right)
		part := []rune(string(line[a:z]))
		if op == "~" || op == "u" || op == "U" {
			for i, r := range part {
				if op == "u" {
					part[i] = unicode.ToLower(r)
				} else if op == "U" {
					part[i] = unicode.ToUpper(r)
				} else if unicode.IsUpper(r) {
					part[i] = unicode.ToLower(r)
				} else {
					part[i] = unicode.ToUpper(r)
				}
			}
		} else {
			part = nil
		}
		result := append(append(append([]rune(nil), line[:a]...), part...), line[z:]...)
		edits = append(edits, patch{b.start(row), old, string(result)})
	}
	group := b.group()
	cursor := b.start(first) + columnOffset(b.line(first), left)
	e.edit(edits, cursor, group)
	e.mode = normalMode
	e.setPos(cursor)
	if op == "c" || op == "s" {
		e.anchor = e.pos()
		e.mode = visualBlockMode
		e.blockInsert = &blockInsertion{column: left, rows: make([]int, 0, last-first), start: e.pos()}
		for row := first + 1; row <= last; row++ {
			e.blockInsert.rows = append(e.blockInsert.rows, row)
		}
		e.enterInsert(insertMode, group, 1)
	}
}

func (e *Editor) beginBlockInsert(key string) {
	if e.mode != visualBlockMode {
		region, _ := e.visualSpan()
		if key == "I" {
			e.setPos(firstNonblank(e.buffer(), e.buffer().row(region.start)))
		} else {
			e.setPos(e.buffer().end(e.buffer().row(max(region.start, region.end-1))))
		}
		e.beginInsert("i", 1)
		return
	}
	e.rememberVisual()
	b := e.buffer()
	first, last, left, right := e.blockBounds()
	column := left
	if key == "A" {
		column = right
	}
	group := b.group()
	line := expandTabs(b.line(first))
	width := logicalColumn(line, len(line))
	if width < column {
		line = append(line, []rune(strings.Repeat(" ", column-width))...)
	}
	start := b.start(first)
	e.edit([]patch{{start, string(b.line(first)), string(line)}}, start+columnOffset(line, column), group)
	e.window().cursor = start + columnOffset(line, column)
	e.blockInsert = &blockInsertion{column: column, start: e.pos()}
	for row := first + 1; row <= last; row++ {
		e.blockInsert.rows = append(e.blockInsert.rows, row)
	}
	e.enterInsert(insertMode, group, 1)
}
func (e *Editor) finishBlockInsert() {
	block := e.blockInsert
	e.blockInsert = nil
	text := e.insertText
	if text == "" {
		return
	}
	if strings.Contains(text, "\n") {
		e.message = "Block insert containing newline kept on its first line only"
		return
	}
	b := e.buffer()
	var edits []patch
	for i := len(block.rows) - 1; i >= 0; i-- {
		row := block.rows[i]
		if row >= len(b.lineStarts()) {
			continue
		}
		line := expandTabs(b.line(row))
		width := logicalColumn(line, len(line))
		if width < block.column {
			line = append(line, []rune(strings.Repeat(" ", block.column-width))...)
		}
		at := columnOffset(line, block.column)
		result := string(line[:at]) + text + string(line[at:])
		edits = append(edits, patch{b.start(row), string(b.line(row)), result})
	}
	e.edit(edits, e.pos(), e.insertGroup)
}
func (e *Editor) putBlock(value register, before bool, count int, group uint64) {
	b := e.buffer()
	row := b.row(e.pos())
	column := logicalColumn(b.line(row), e.pos()-b.start(row))
	if !before {
		column++
	}
	parts := strings.Split(value.Text, "\n")
	lines := strings.Split(string(b.text), "\n")
	for len(lines) < row+len(parts) {
		lines = append(lines, "")
	}
	for i, part := range parts {
		width := logicalColumn([]rune(part), len([]rune(part)))
		part += strings.Repeat(" ", max(0, value.Width-width))
		text, err := repeatText(part, count)
		if err != nil {
			e.error(err)
			return
		}
		line := expandTabs([]rune(lines[row+i]))
		lineWidth := logicalColumn(line, len(line))
		if lineWidth < column {
			line = append(line, []rune(strings.Repeat(" ", column-lineWidth))...)
		}
		at := columnOffset(line, column)
		lines[row+i] = string(line[:at]) + text + string(line[at:])
	}
	e.replace(0, len(b.text), strings.Join(lines, "\n"), group)
	e.setPos(b.start(row) + columnOffset(b.line(row), column))
}
func (e *Editor) fillVisual(r rune) {
	if r == '\n' {
		e.error(fmt.Errorf("visual replacement by newline is unsupported"))
		e.mode = normalMode
		return
	}
	if e.mode == visualBlockMode {
		b := e.buffer()
		first, last, left, right := e.blockBounds()
		var edits []patch
		for row := last; row >= first; row-- {
			line := expandTabs(b.line(row))
			a, z := columnOffset(line, left), columnOffset(line, right)
			for at := a; at < z; at++ {
				line[at] = r
			}
			edits = append(edits, patch{b.start(row), string(b.line(row)), string(line)})
		}
		e.edit(edits, b.start(first), 0)
	} else {
		region, _ := e.visualSpan()
		text := append([]rune(nil), e.buffer().text[region.start:region.end]...)
		for i := range text {
			if text[i] != '\n' {
				text[i] = r
			}
		}
		e.replace(region.start, region.end, string(text), 0)
		e.setPos(region.start)
	}
	e.rememberVisual()
	e.mode = normalMode
}
