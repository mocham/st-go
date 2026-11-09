package main

import (
	_ "embed"
	"strings"
	"unicode"
	"unicode/utf8"

	"st-go/tui/imap"
)

// The editor supplies only context and journaled edits. All suffix mappings,
// filetype guards, cursor stops and template bodies live in embedded data.
//
//go:embed imap/tex.json
var texIMAPJSON []byte

var texIMAP = func() *imap.Set {
	set, err := imap.Parse(texIMAPJSON)
	if err != nil {
		panic(err)
	}
	return set
}()

func texEditing(b *Buffer) bool { return texIMAP.Supports(b.meta.Path) }

func (e *Editor) expandTexIMAP(key string) bool {
	b, pos := e.buffer(), e.pos()
	if !texEditing(b) {
		return false
	}
	match, ok := texIMAP.Match(b.text[:pos], key)
	if !ok {
		return false
	}
	start := pos - match.Remove
	removed := string(b.text[start:pos])
	if e.edit([]patch{{start, removed, match.Text}}, start+match.Cursor, e.insertGroup) {
		if match.Cursor == utf8.RuneCountInString(match.Text) {
			e.insertText = strings.TrimSuffix(e.insertText, removed) + match.Text
		} else {
			e.insertText = ""
			e.insertCount = 1
		}
	}
	return true
}

// Match the suite's preamble rule: an uncommented \documentclass precedes the
// insertion, and \begin{document} has not yet been reached. A standalone TeX
// fragment with no document class defaults to environment insertion.
func texPreamble(text []rune, until int) bool {
	until = clamp(until, 0, len(text))
	class := false
	for at := 0; at < min(until, len(text)); {
		if text[at] == '%' {
			for at < until && text[at] != '\n' {
				at++
			}
			continue
		}
		if text[at] != '\\' {
			at++
			continue
		}
		at++
		first := at
		for at < until && (unicode.IsLetter(text[at]) || text[at] == '@') {
			at++
		}
		if at == first {
			if at < until {
				at++
			}
			continue
		}
		switch string(text[first:at]) {
		case "documentclass":
			class = true
		case "begin":
			arg := at
			for arg < until && unicode.IsSpace(text[arg]) {
				arg++
			}
			if arg < until && text[arg] == '{' {
				end := arg + 1
				for end < until && text[end] != '}' {
					end++
				}
				if end < until && string(text[arg+1:end]) == "document" {
					return false
				}
			}
		}
	}
	return class
}

func texSnippetName(name string, preamble bool) bool {
	if name == "" || len(name) > 256 {
		return false
	}
	for i, r := range name {
		if unicode.IsLetter(r) || unicode.IsDigit(r) || r == '_' {
			continue
		}
		if preamble && r == '-' {
			continue
		}
		if !preamble && r == '*' && i == len(name)-1 {
			continue
		}
		return false
	}
	return true
}

func (e *Editor) expandTexLine() {
	b := e.buffer()
	row := b.row(e.pos())
	line := b.line(row)
	first := 0
	for first < len(line) && (line[first] == ' ' || line[first] == '\t') {
		first++
	}
	indent := string(line[:first])
	name := strings.TrimSpace(string(line[first:]))
	preamble := texPreamble(b.text, b.start(row)+first)
	if !texSnippetName(name, preamble) {
		e.message = "F5: put an environment/package name on a line by itself"
		return
	}
	template := "environment"
	if preamble {
		template = "package"
	}
	expansion, err := texIMAP.Template(template, map[string]string{"name": name}, indent)
	if err != nil {
		e.error(err)
		return
	}
	oldMode := e.mode
	if oldMode != insertMode {
		e.enterInsert(insertMode, 0, 1)
	}
	if !e.edit([]patch{{b.start(row), string(line), expansion.Text}}, b.start(row)+expansion.Cursor, e.insertGroup) {
		e.mode = oldMode
		return
	}
	e.insertText, e.insertCount, e.replaceBack = "", 1, nil
}

func (e *Editor) jumpTexPlaceholder() {
	b := e.buffer()
	target, last, ok := texIMAP.NextPlaceholder(b.text, e.pos())
	if !ok {
		e.message = "No TeX placeholders"
		return
	}
	oldMode := e.mode
	if oldMode != insertMode {
		e.enterInsert(insertMode, 0, 1)
	}
	if !e.edit([]patch{{target, string(b.text[target:last]), ""}}, target, e.insertGroup) {
		e.mode = oldMode
		return
	}
	e.insertText, e.insertCount, e.replaceBack = "", 1, nil
}
