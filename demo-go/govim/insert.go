package main

import (
	"fmt"
	"strings"
	"unicode"
)

type completionState struct {
	at      int
	values  []string
	index   int
	current string
}

func (e *Editor) enterInsert(kind mode, group uint64, count int) {
	if group == 0 {
		group = e.buffer().group()
	}
	e.mode = kind
	e.insertGroup = group
	e.insertCount = count
	e.insertStart = e.pos()
	e.insertText = ""
	e.replaceBack = nil
	e.completion = nil
	e.insertSeparator = ""
	e.reset()
}
func (e *Editor) beginInsert(key string, count int) {
	b := e.buffer()
	row := b.row(e.pos())
	group := b.group()
	pos := e.pos()
	switch key {
	case "a":
		pos = min(b.end(row), pos+1)
	case "A":
		pos = b.end(row)
	case "I":
		pos = firstNonblank(b, row)
	}
	kind := insertMode
	if key == "R" {
		kind = replaceMode
	}
	e.enterInsert(kind, group, count)
	e.window().cursor = pos
	e.insertStart = pos
	if key == "o" || key == "O" {
		indent := indentation(b, row)
		if key == "o" {
			previous := strings.TrimSpace(string(b.line(row)))
			if strings.HasSuffix(previous, "{") || strings.HasSuffix(previous, ":") || strings.HasPrefix(previous, "\\begin{") {
				indent += "    "
			}
			pos = b.end(row)
			e.replace(pos, pos, "\n"+indent, group)
		} else {
			pos = b.start(row)
			if e.replace(pos, pos, indent+"\n", group) {
				e.window().cursor = pos + len([]rune(indent))
			}
		}
		e.insertStart = e.pos()
		e.insertSeparator = "\n" + indent
	}
}

func (e *Editor) insertKey(key string) {
	b := e.buffer()
	pos := e.pos()
	row := b.row(pos)
	if e.pending == "insert-register" {
		e.pending = ""
		r := []rune(key)
		if len(r) == 1 {
			e.withRegister(r[0], func(value register) { e.insertLiteral(value.Text) })
		}
		return
	}
	if e.pending == "literal" {
		e.pending = ""
		value := key
		if key == "<Esc>" {
			value = "\x1b"
		}
		if key == "<Enter>" {
			value = "\n"
		}
		if key == "<Tab>" {
			value = "\t"
		}
		if len([]rune(value)) == 1 {
			e.insertLiteral(value)
		}
		return
	}
	if key != "\x0e" && key != "\x10" {
		e.completion = nil
	}
	switch key {
	case "<Esc>", "\x03", "\x1c":
		e.finishInsert()
	case "<Left>", "<Right>", "<Up>", "<Down>", "<Home>", "<End>", "<PageUp>", "<PageDown>":
		e.insertCount = 1
		e.insertText = ""
		e.insertGroup = b.group()
		target, _, _, ok := e.motion(key, 1)
		if ok {
			e.window().cursor = clamp(target, 0, len(b.text))
			e.window().goal = -1
		}
	case "<BS>", "\x7f", "\b":
		if e.mode == replaceMode && len(e.replaceBack) > 0 {
			p := e.replaceBack[len(e.replaceBack)-1]
			if e.edit([]patch{{p.At, p.New, p.Old}}, p.At, e.insertGroup) {
				e.replaceBack = e.replaceBack[:len(e.replaceBack)-1]
			}
			return
		}
		if pos > 0 {
			at := pos - 1
			if pos == firstNonblank(b, row) && pos > b.start(row) {
				column := logicalColumn(b.line(row), pos-b.start(row))
				remove := column % 4
				if remove == 0 {
					remove = 4
				}
				at = max(b.start(row), pos-remove)
			}
			e.replace(at, pos, "", e.insertGroup)
			r := []rune(e.insertText)
			if len(r) > 0 {
				e.insertText = string(r[:len(r)-1])
			}
		}
	case "<Delete>":
		if pos < len(b.text) {
			e.replace(pos, pos+1, "", e.insertGroup)
			e.window().cursor = pos
		}
	case "<Enter>", "\r", "\n":
		indent := indentation(b, row)
		before := strings.TrimSpace(string(b.text[b.start(row):pos]))
		if strings.HasSuffix(before, "{") || strings.HasSuffix(before, ":") || strings.HasPrefix(before, "\\begin{") {
			indent += "    "
		}
		e.insertLiteral("\n" + indent)
	case "<Tab>", "\t":
		column := logicalColumn(b.line(row), pos-b.start(row))
		e.insertLiteral(strings.Repeat(" ", 4-column%4))
	case "\x17":
		at := pos
		for at > 0 && unicode.IsSpace(b.text[at-1]) {
			at--
		}
		if at > 0 {
			class := wordClass(b.text[at-1], false)
			for at > 0 && wordClass(b.text[at-1], false) == class {
				at--
			}
		}
		e.replace(at, pos, "", e.insertGroup)
		e.insertText = ""
		e.insertCount = 1
	case "\x15":
		at := firstNonblank(b, row)
		if pos <= at {
			at = b.start(row)
		}
		e.replace(at, pos, "", e.insertGroup)
		e.insertText = ""
		e.insertCount = 1
	case "\x14", "\x04":
		start := b.start(row)
		end := firstNonblank(b, row)
		width := indentCells(b.line(row))
		if key == "\x14" {
			width += 4
		} else {
			width = max(0, width-4)
		}
		newIndent := strings.Repeat(" ", width)
		if e.edit([]patch{{start, string(b.text[start:end]), newIndent}}, max(start, pos+width-(end-start)), e.insertGroup) {
			e.window().cursor = clamp(pos+width-(end-start), start, len(b.text))
		}
	case "\x12":
		e.pending = "insert-register"
	case "\x16":
		e.pending = "literal"
	case "\x0f":
		e.mode = normalMode
		e.oneNormal = true
		e.commandKeys = nil
	case "\x0e", "\x10":
		e.completeWord(key == "\x10")
	case "\x01":
		e.insertLiteral(e.lastInsert)
	case "<F5>", "<C-F5>":
		e.execute("Gn")
	default:
		if len([]rune(key)) == 1 && []rune(key)[0] >= 0x20 {
			if key == "}" && strings.TrimSpace(string(b.text[b.start(row):pos])) == "" {
				start := b.start(row)
				end := firstNonblank(b, row)
				indent := max(0, indentCells(b.line(row))-4)
				if e.edit([]patch{{start, string(b.text[start:end]), strings.Repeat(" ", indent)}}, start+indent, e.insertGroup) {
					e.window().cursor = start + indent
				}
			}
			e.insertLiteral(key)
		}
	}
}

func (e *Editor) insertLiteral(text string) {
	if text == "" {
		return
	}
	b := e.buffer()
	pos := e.pos()
	end := pos
	if e.mode == replaceMode && !strings.Contains(text, "\n") {
		end = min(b.end(b.row(pos)), pos+len([]rune(text)))
	}
	p := patch{pos, string(b.text[pos:end]), text}
	if e.edit([]patch{p}, pos+len([]rune(text)), e.insertGroup) {
		e.insertText += text
		if e.mode == replaceMode {
			e.replaceBack = append(e.replaceBack, p)
		}
	}
}
func (e *Editor) finishInsert() {
	if e.insertCount > 1 && e.insertText != "" {
		text, err := repeatText(e.insertSeparator+e.insertText, e.insertCount-1)
		if err != nil {
			e.error(err)
		} else {
			e.insertLiteral(text)
		}
	}
	if e.blockInsert != nil {
		e.finishBlockInsert()
	}
	e.lastInsert = e.insertText
	e.mode = normalMode
	e.pending = ""
	e.window().cursor = e.buffer().normal(max(e.buffer().start(e.buffer().row(e.pos())), e.pos()-1))
	e.insertGroup = 0
	e.reset()
}

func (e *Editor) paste(text string) {
	if len(text) > maxBufferBytes {
		e.error(fmt.Errorf("paste exceeds buffer limit"))
		return
	}
	if e.prompt != "" {
		text = strings.ReplaceAll(text, "\r\n", " ")
		text = strings.NewReplacer("\r", " ", "\n", " ", "\t", " ").Replace(text)
		e.insertPrompt(text)
		return
	}
	text = strings.ReplaceAll(text, "\r\n", "\n")
	text = strings.ReplaceAll(text, "\r", "\n")
	if e.visual() {
		e.visualOperation("d")
	}
	if e.mode != insertMode && e.mode != replaceMode {
		e.beginInsert("i", 1)
		e.insertLiteral(text)
		e.finishInsert()
	} else {
		e.insertLiteral(text)
	}
}

func (e *Editor) completeWord(backward bool) {
	b := e.buffer()
	if e.completion == nil {
		at := e.pos()
		for at > 0 && wordClass(b.text[at-1], false) == 1 {
			at--
		}
		prefix := string(b.text[at:e.pos()])
		if prefix == "" {
			return
		}
		seen := map[string]bool{prefix: true}
		values := []string{prefix}
		for start := 0; start < len(b.text); {
			for start < len(b.text) && wordClass(b.text[start], false) != 1 {
				start++
			}
			end := start
			for end < len(b.text) && wordClass(b.text[end], false) == 1 {
				end++
			}
			word := string(b.text[start:end])
			if strings.HasPrefix(word, prefix) && !seen[word] {
				seen[word] = true
				values = append(values, word)
				if len(values) >= 1000 {
					break
				}
			}
			start = max(end, start+1)
		}
		e.completion = &completionState{at: at, values: values, current: prefix}
	}
	c := e.completion
	if len(c.values) < 2 {
		e.message = "No completion"
		return
	}
	delta := 1
	if backward {
		delta = -1
	}
	c.index = (c.index + delta + len(c.values)) % len(c.values)
	next := c.values[c.index]
	if e.replace(c.at, c.at+len([]rune(c.current)), next, e.insertGroup) {
		c.current = next
		e.completion = c
		e.insertCount = 1
		e.insertText = ""
	}
}
