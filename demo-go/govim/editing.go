package main

import (
	"fmt"
	"strconv"
	"strings"
	"unicode"
)

func (e *Editor) lineRange(row, count int) span {
	b := e.buffer()
	last := min(len(b.lineStarts())-1, row+max(1, count)-1)
	return span{b.start(row), b.end(last)}
}

func (e *Editor) applyMotionOperator(target int, line, inclusive bool) {
	b := e.buffer()
	start, end := e.pos(), target
	if end < start {
		start, end = end, start
	}
	if e.forceMode == visualLineMode {
		line = true
	}
	if e.forceMode == visualMode {
		line = false
		inclusive = !inclusive
	}
	if line {
		r1, r2 := b.row(start), b.row(end)
		e.applyOperator(e.operator, e.lineRange(r1, r2-r1+1), true)
		e.reset()
		return
	}
	if inclusive && end < len(b.text) {
		end++
	}
	if !inclusive && b.row(end) > b.row(start) && end == b.start(b.row(end)) {
		if start <= firstNonblank(b, b.row(start)) {
			e.applyOperator(e.operator, e.lineRange(b.row(start), b.row(end)-b.row(start)), true)
			e.reset()
			return
		}
		end--
	}
	e.applyOperator(e.operator, span{start, end}, false)
	e.reset()
}

func (e *Editor) applyOperator(op string, region span, line bool) {
	b := e.buffer()
	start, end := clamp(region.start, 0, len(b.text)), clamp(region.end, 0, len(b.text))
	if end < start {
		start, end = end, start
	}
	if op == "y" {
		text := string(b.text[start:end])
		if line {
			text += "\n"
		}
		e.storeRegister(register{Text: text, Line: line}, true, line || e.visual())
		if !line {
			e.setPos(start)
		}
		e.mode = normalMode
		return
	}
	if op == "d" || op == "c" {
		e.deleteRange(start, end, line, op == "c")
		return
	}
	if op == ">" || op == "<" || op == "=" {
		e.indentLines(b.row(start), b.row(end), op, 1)
		return
	}
	if op == "gq" {
		e.formatLines(b.row(start), b.row(end))
		return
	}
	if op == "g~" || op == "gu" || op == "gU" {
		e.transformRange(start, end, strings.TrimPrefix(op, "g"))
		e.setPos(start)
		e.mode = normalMode
	}
}

func (e *Editor) deleteRange(start, end int, line, change bool) {
	b := e.buffer()
	start, end = clamp(start, 0, len(b.text)), clamp(end, 0, len(b.text))
	if end < start {
		start, end = end, start
	}
	reg := register{Text: string(b.text[start:end]), Line: line}
	if line {
		reg.Text += "\n"
	}
	clipboardCopy := line || e.visual()
	group := b.group()
	cursor := start
	replacement := ""
	if line && change {
		replacement = indentation(b, b.row(start))
		cursor = start + len([]rune(replacement))
	}
	allLines := line && !change && start == 0 && end == len(b.text)
	if line && !change {
		if end < len(b.text) {
			end++
		} else if start > 0 {
			start--
			cursor = start
		}
	}
	if end > start || allLines {
		e.emptyResult = allLines
		if !e.edit([]patch{{start, string(b.text[start:end]), replacement}}, cursor, group) {
			return
		}
		e.storeRegister(reg, false, clipboardCopy)
	}
	if change {
		e.enterInsert(insertMode, group, 1)
		e.window().cursor = clamp(cursor, 0, len(b.text))
	} else {
		e.mode = normalMode
		e.setPos(cursor)
		if line {
			e.setPos(firstNonblank(b, b.row(e.pos())))
		}
	}
}

func (e *Editor) storeRegister(value register, yank, copyClipboard bool) {
	selected := e.selectedRegister
	if selected == '_' {
		return
	}
	if len(value.Text) > 8<<20 {
		e.message = "Register exceeds 8 MiB; text changed but register was not replaced"
		return
	}
	if selected == 0 {
		selected = '"'
	}
	if selected >= 'A' && selected <= 'Z' {
		name := unicode.ToLower(selected)
		old := e.registers[name]
		old.Text += value.Text
		old.Keys = nil
		old.Line = old.Line || value.Line
		e.registers[name] = old
	} else if selected != '"' {
		e.registers[selected] = value
	}
	e.registers['"'] = value
	if yank {
		e.registers['0'] = value
	} else if value.Line || strings.Contains(value.Text, "\n") {
		for n := '9'; n > '1'; n-- {
			e.registers[n] = e.registers[n-1]
		}
		e.registers['1'] = value
	} else {
		e.registers['-'] = value
	}
	if selected == '+' || selected == '*' || copyClipboard {
		target := '+'
		if selected == '*' {
			target = '*'
		}
		e.registers[target] = value
		if e.clip != nil {
			if err := e.clip.Set(value.Text, selected == '*'); err != nil {
				e.clipboardError = err.Error()
				e.message = "Clipboard: " + err.Error() + " (internal register retained)"
			}
		} else {
			e.message = "Terminal clipboard unavailable; internal register retained"
		}
	}
}
func (e *Editor) getRegister(name rune) register {
	if name == 0 {
		name = '"'
	}
	switch name {
	case '%':
		return register{Text: e.buffer().meta.Path}
	case '#':
		if e.alternate != nil {
			return register{Text: e.alternate.meta.Path}
		}
	case '/':
		return register{Text: e.search.pattern}
	case ':':
		return register{Text: e.lastEx}
	case '.':
		return register{Text: e.lastInsert}
	}
	return e.registers[unicode.ToLower(name)]
}

func repeatText(text string, count int) (string, error) {
	if count < 1 {
		count = 1
	}
	if len(text) > 0 && count > maxBufferBytes/len(text) {
		return "", fmt.Errorf("repeated insertion exceeds buffer limit")
	}
	return strings.Repeat(text, count), nil
}

func (e *Editor) put(value register, before bool, count int) {
	if value.Text == "" {
		e.message = "Nothing in register"
		return
	}
	b := e.buffer()
	group := b.group()
	if value.Block {
		e.putBlock(value, before, count, group)
		return
	}
	if value.Line {
		logical := strings.TrimSuffix(value.Text, "\n")
		repeated, err := repeatText(logical+"\n", count)
		if err != nil {
			e.error(err)
			return
		}
		logical = strings.TrimSuffix(repeated, "\n")
		row := b.row(e.pos())
		at := b.start(row)
		text := logical + "\n"
		cursor := at
		if len(b.text) == 0 {
			at = 0
			text = logical
		} else if !before {
			at = b.end(row)
			text = "\n" + logical
			cursor = at + 1
		}
		if e.edit([]patch{{at, "", text}}, cursor, group) {
			e.setPos(firstNonblank(b, b.row(cursor)))
		}
		return
	}
	text, err := repeatText(value.Text, count)
	if err != nil {
		e.error(err)
		return
	}
	at := e.pos()
	if !before {
		at = min(b.end(b.row(at)), at+1)
	}
	if e.replace(at, at, text, group) {
		e.setPos(at + max(0, len([]rune(text))-1))
	}
}

func (e *Editor) transformRange(start, end int, operation string) {
	b := e.buffer()
	start, end = clamp(start, 0, len(b.text)), clamp(end, 0, len(b.text))
	if end < start {
		start, end = end, start
	}
	text := []rune(string(b.text[start:end]))
	for i, r := range text {
		switch operation {
		case "u":
			text[i] = unicode.ToLower(r)
		case "U":
			text[i] = unicode.ToUpper(r)
		default:
			if unicode.IsUpper(r) {
				text[i] = unicode.ToLower(r)
			} else {
				text[i] = unicode.ToUpper(r)
			}
		}
	}
	e.replace(start, end, string(text), 0)
}

func (e *Editor) joinLines(row, count int, raw bool) {
	b := e.buffer()
	last := min(len(b.lineStarts())-1, row+count-1)
	if last <= row {
		return
	}
	start, end := b.start(row), b.end(last)
	result := string(b.line(row))
	cursor := len([]rune(result))
	for r := row + 1; r <= last; r++ {
		next := string(b.line(r))
		if !raw {
			next = strings.TrimLeft(next, " \t")
			if !strings.HasSuffix(result, " ") && !strings.HasSuffix(result, "\t") && next != "" && !strings.HasPrefix(next, ")") {
				result += " "
				trimmed := strings.TrimSpace(strings.TrimSuffix(result, " "))
				if len(trimmed) > 0 && strings.ContainsRune(".!?", rune(trimmed[len(trimmed)-1])) {
					result += " "
				}
			}
		}
		result += next
	}
	if e.replace(start, end, result, 0) {
		e.setPos(start + cursor)
	}
}

func indentCells(line []rune) int {
	at := 0
	for _, r := range line {
		if r == ' ' {
			at++
		} else if r == '\t' {
			at += 4 - at%4
		} else {
			break
		}
	}
	return at
}
func indentation(b *Buffer, row int) string { return strings.Repeat(" ", indentCells(b.line(row))) }
func smartIndent(b *Buffer, row int) int {
	if row <= 0 {
		return 0
	}
	previous := strings.TrimSpace(string(b.line(row - 1)))
	width := indentCells(b.line(row - 1))
	if strings.HasSuffix(previous, "{") || strings.HasSuffix(previous, ":") || strings.HasPrefix(previous, "\\begin{") {
		width += 4
	}
	current := strings.TrimSpace(string(b.line(row)))
	if strings.HasPrefix(current, "}") || strings.HasPrefix(current, "\\end{") {
		width = max(0, width-4)
	}
	return width
}
func (e *Editor) indentLines(first, last int, op string, levels int) {
	b := e.buffer()
	first, last = clamp(first, 0, len(b.lineStarts())-1), clamp(last, 0, len(b.lineStarts())-1)
	if last < first {
		first, last = last, first
	}
	var edits []patch
	for row := last; row >= first; row-- {
		line := b.line(row)
		at := 0
		for at < len(line) && (line[at] == ' ' || line[at] == '\t') {
			at++
		}
		width := indentCells(line)
		if len(line) == 0 && op == ">" {
			continue
		}
		if op == ">" {
			width += 4 * levels
		} else if op == "<" {
			width = max(0, width-4*levels)
		} else {
			width = smartIndent(b, row)
		}
		edits = append(edits, patch{b.start(row), string(line[:at]), strings.Repeat(" ", width)})
	}
	if e.edit(edits, b.start(first), 0) {
		e.setPos(firstNonblank(b, first))
	}
}

func (e *Editor) formatLines(first, last int) {
	b := e.buffer()
	start, end := b.start(first), b.end(last)
	indent := indentation(b, first)
	words := strings.Fields(string(b.text[start:end]))
	var lines []string
	line := indent
	for _, word := range words {
		if len([]rune(line))+len([]rune(word))+1 > 79 && line != indent {
			lines = append(lines, line)
			line = indent
		}
		if line != indent {
			line += " "
		}
		line += word
	}
	lines = append(lines, line)
	e.replace(start, end, strings.Join(lines, "\n"), 0)
	e.setPos(start)
}

func (e *Editor) incrementNumber(count int, subtract bool) {
	b := e.buffer()
	row := b.row(e.pos())
	at := e.pos()
	end := b.end(row)
	for at < end && !unicode.IsDigit(b.text[at]) {
		at++
	}
	if at == end {
		e.message = "No number under or after cursor"
		return
	}
	start := at
	if start > b.start(row) && b.text[start-1] == '-' {
		start--
	}
	for at < end && ((b.text[at] >= '0' && b.text[at] <= '9') || (b.text[at] >= 'a' && b.text[at] <= 'f') || (b.text[at] >= 'A' && b.text[at] <= 'F') || b.text[at] == 'x' || b.text[at] == 'X') {
		at++
	}
	text := string(b.text[start:at])
	base := 10
	if strings.HasPrefix(strings.TrimPrefix(text, "-"), "0x") || strings.HasPrefix(strings.TrimPrefix(text, "-"), "0X") {
		base = 0
	}
	number, err := strconv.ParseInt(text, base, 64)
	if err != nil {
		e.error(err)
		return
	}
	delta := int64(count)
	if subtract {
		delta = -delta
	}
	if (delta > 0 && number > int64(^uint64(0)>>1)-delta) || (delta < 0 && number < (-int64(^uint64(0)>>1)-1)-delta) {
		e.error(fmt.Errorf("number overflow"))
		return
	}
	number += delta
	replacement := strconv.FormatInt(number, 10)
	if base == 0 {
		replacement = fmt.Sprintf("0x%x", number)
	} else if len(text) > 1 && text[0] == '0' && number >= 0 {
		replacement = fmt.Sprintf("%0*d", len(text), number)
	}
	if e.replace(start, at, replacement, 0) {
		e.setPos(start + len([]rune(replacement)) - 1)
	}
}

func (e *Editor) visualSpan() (span, bool) {
	b := e.buffer()
	start, end := min(e.anchor, e.pos()), max(e.anchor, e.pos())
	line := e.mode == visualLineMode
	if line {
		return span{b.start(b.row(start)), b.end(b.row(end))}, true
	}
	return span{start, min(len(b.text), end+1)}, false
}
func (e *Editor) visualOperation(op string) {
	e.rememberVisual()
	region, line := e.visualSpan()
	b := e.buffer()
	if e.mode == visualBlockMode && (op == "d" || op == "x" || op == "y" || op == "c" || op == "s" || op == "~" || op == "u" || op == "U") {
		e.blockOperation(op)
		return
	}
	if op == "D" || op == "Y" || op == "C" {
		line = true
		region = e.lineRange(b.row(region.start), b.row(max(region.start, region.end-1))-b.row(region.start)+1)
	}
	if op == "J" {
		e.joinLines(b.row(region.start), max(2, b.row(max(region.start, region.end-1))-b.row(region.start)+1), false)
		e.mode = normalMode
		return
	}
	if op == "~" || op == "u" || op == "U" {
		e.transformRange(region.start, region.end, op)
		e.setPos(region.start)
		e.mode = normalMode
		return
	}
	if op == ">" || op == "<" || op == "=" {
		e.indentLines(b.row(region.start), b.row(max(region.start, region.end-1)), op, e.number())
		e.mode = normalMode
		return
	}
	if mapped, ok := map[string]string{"x": "d", "s": "c", "D": "d", "C": "c", "Y": "y"}[op]; ok {
		op = mapped
	}
	e.applyOperator(op, region, line)
	if e.visual() {
		e.mode = normalMode
	}
}
