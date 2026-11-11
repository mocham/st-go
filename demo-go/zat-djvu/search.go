package main

import (
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"

	"st-go/limits"
	"st-go/tui/textsearch"
)

func expandSearch(source string) string { return textsearch.Expand(source) }

type textHit struct{ start, end int }

func foldSearchRune(character rune) rune {
	minimum := character
	for next := unicode.SimpleFold(character); next != character; next = unicode.SimpleFold(next) {
		if next < minimum {
			minimum = next
		}
	}
	return minimum
}

func foldedSearch(text string) (string, []int) {
	var folded strings.Builder
	var offsets []int
	for offset, character := range text {
		value := foldSearchRune(character)
		folded.WriteRune(value)
		for index := 0; index < utf8.RuneLen(value); index++ {
			offsets = append(offsets, offset)
		}
	}
	return folded.String(), append(offsets, len(text))
}

func textSearchHits(text, query string) []textHit {
	if query == "" {
		return nil
	}
	folded, offsets := foldedSearch(text)
	needle, _ := foldedSearch(query)
	var hits []textHit
	for from := 0; from <= len(folded)-len(needle) && len(hits) < limits.PreviewSearchMatches; {
		at := strings.Index(folded[from:], needle)
		if at < 0 {
			break
		}
		at += from
		end := at + len(needle)
		hits = append(hits, textHit{offsets[at], offsets[end]})
		from = end
	}
	return hits
}

func (v *viewer) activeTextHit() int {
	if v.textPage == v.searchPage && v.searchPageMatch > 0 && v.searchPageMatch <= len(v.textHits) {
		return v.searchPageMatch - 1
	}
	return -1
}

func (v *viewer) updateTextSearch(reveal bool) {
	v.textHits = nil
	if v.searchVisible {
		v.textHits = textSearchHits(v.pageText, expandSearch(v.search))
	}
	if reveal {
		v.revealTextSearch()
	}
}

func (v *viewer) revealTextSearch() {
	index := v.activeTextHit()
	if index < 0 || len(v.textLineOffsets) == 0 {
		return
	}
	line := max(0, sort.Search(len(v.textLineOffsets), func(i int) bool { return v.textLineOffsets[i] > v.textHits[index].start })-1)
	if line < v.textTop || line >= v.textTop+v.panelRows() {
		v.textTop = min(line, max(0, len(v.textLines)-v.panelRows()))
	}
}

func (v *viewer) drawTextSearchLine(row, line int) {
	v.panelLine(row, "", "")
	text, offset := v.textLines[line], v.textLineOffsets[line]
	active, written := v.activeTextHit(), 0
	for index, hit := range v.textHits {
		start, end := max(0, hit.start-offset), min(len(text), hit.end-offset)
		if start >= end {
			continue
		}
		v.write(text[written:start])
		style := "\x1b[30;48;5;220m"
		if index == active {
			style = "\x1b[30;48;5;214m"
		}
		v.write(style + text[start:end] + "\x1b[0m")
		written = end
	}
	v.write(text[written:])
}
