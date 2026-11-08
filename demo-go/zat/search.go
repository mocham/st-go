package main

import (
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"

	"golang.org/x/text/unicode/norm"
	"st-go/limits"
)

var greekSearchNames = map[string]string{
	"alpha": "α", "beta": "β", "gamma": "γ", "delta": "δ", "epsilon": "ε", "zeta": "ζ", "eta": "η", "theta": "θ",
	"iota": "ι", "kappa": "κ", "lambda": "λ", "mu": "μ", "nu": "ν", "xi": "ξ", "omicron": "ο", "pi": "π",
	"rho": "ρ", "sigma": "σ", "tau": "τ", "upsilon": "υ", "phi": "φ", "chi": "χ", "psi": "ψ", "omega": "ω",
	"varepsilon": "ϵ", "vartheta": "ϑ", "varpi": "ϖ", "varrho": "ϱ", "varsigma": "ς", "varphi": "ϕ",
}

var searchAccents = map[byte]rune{
	'\'': '\u0301', '`': '\u0300', '^': '\u0302', '"': '\u0308', '~': '\u0303', '=': '\u0304', '.': '\u0307',
	'c': '\u0327', 'v': '\u030c', 'u': '\u0306', 'H': '\u030b', 'r': '\u030a', 'k': '\u0328',
}

// Expand only recognized shorthand; unknown names/commands remain literal.
// Backslash-escaped braces and doubled backslashes permit literal searches.
func expandSearch(source string) string {
	var out strings.Builder
	for i := 0; i < len(source); {
		if source[i] == '{' {
			if end := strings.IndexByte(source[i:], '}'); end >= 0 && end <= 16 {
				name := source[i+1 : i+end]
				if letter, ok := greekSearchNames[strings.ToLower(name)]; ok {
					if len(name) > 0 && name[0] >= 'A' && name[0] <= 'Z' {
						letter = strings.ToUpper(letter)
					}
					out.WriteString(letter)
					i += end + 1
					continue
				}
			}
		}
		if source[i] == '\\' && i+1 < len(source) {
			op := source[i+1]
			if op == '\\' || op == '{' || op == '}' {
				out.WriteByte(op)
				i += 2
				continue
			}
			accent := searchAccents[op]
			if accent != 0 && i+2 < len(source) {
				start, end := i+2, 0
				braced := source[start] == '{'
				if braced {
					start++
				}
				letter, size := utf8.DecodeRuneInString(source[start:])
				end = start + size
				if unicode.IsLetter(letter) && (!braced || (end < len(source) && source[end] == '}')) && (braced || !unicode.IsLetter(rune(op))) {
					out.WriteRune(letter)
					out.WriteRune(accent)
					if braced {
						end++
					}
					i = end
					continue
				}
			}
		}
		r, size := utf8.DecodeRuneInString(source[i:])
		out.WriteRune(r)
		i += size
	}
	return norm.NFC.String(out.String())
}

type textHit struct{ start, end int }

func foldSearchRune(r rune) rune {
	minimum := r
	for next := unicode.SimpleFold(r); next != r; next = unicode.SimpleFold(next) {
		if next < minimum {
			minimum = next
		}
	}
	return minimum
}

func foldedSearch(text string) (string, []int) {
	var folded strings.Builder
	var offsets []int
	for at, r := range text {
		f := foldSearchRune(r)
		folded.WriteRune(f)
		for i := 0; i < utf8.RuneLen(f); i++ {
			offsets = append(offsets, at)
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
	for i, hit := range v.textHits {
		start, end := max(0, hit.start-offset), min(len(text), hit.end-offset)
		if start >= end {
			continue
		}
		v.write(text[written:start])
		style := "\x1b[30;48;5;220m"
		if i == active {
			style = "\x1b[30;48;5;214m"
		}
		v.write(style + text[start:end] + "\x1b[0m")
		written = end
	}
	v.write(text[written:])
}
