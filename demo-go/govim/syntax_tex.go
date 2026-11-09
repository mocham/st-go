package main

import (
	"fmt"
	"sort"
	"strings"
	"time"
	"unicode"

	"github.com/alecthomas/chroma/v2"
)

// A small, restartable TeX scanner. Group meanings follow Vim91 syntax/tex.vim:
// texMath -> Special, texStatement/texMathDelim -> Statement, texSection ->
// PreCondit, texComment -> Comment, texZone -> PreCondit. Never executes TeX.
type texSyntaxState struct {
	math, environments, verbatim string
	textDepth                    int
	textPending                  bool
}

func texMathEnvironment(name string) bool {
	switch strings.TrimSuffix(name, "*") {
	case "math", "displaymath", "equation", "eqnarray", "align", "alignat", "xalignat", "xxalignat", "flalign", "gather", "multline", "split", "aligned", "alignedat", "gathered", "cases", "matrix", "pmatrix", "bmatrix", "Bmatrix", "vmatrix", "Vmatrix", "smallmatrix", "subequations":
		return true
	}
	return false
}

func (d *syntaxDocument) calculateTex(text []rune, target int, point syntaxCheckpoint, stop <-chan struct{}, deadline time.Time) (int, error) {
	state := point.tex
	start, pos := point.at, point.at
	points := []syntaxCheckpoint{point}
	var tokens []syntaxToken
	plain := syntaxForeground(chroma.Text)
	styles := make(map[chroma.TokenType]string)
	emit := func(first, last int, kind chroma.TokenType) {
		style, ok := styles[kind]
		if !ok {
			style = syntaxForeground(kind)
			styles[kind] = style
		}
		if style != plain {
			tokens = appendSyntaxToken(tokens, syntaxToken{first, last, style})
		}
	}
	for pos < len(text) {
		select {
		case <-stop:
			return pos - start, fmt.Errorf("syntax cancelled")
		default:
		}
		end := pos
		for end < len(text) && text[end] != '\n' {
			end++
		}
		if end < len(text) {
			end++
		}
		verbatimName := ""
		var closing []rune
		for pos < end {
			if pos > start && pos%1024 == 0 && time.Now().After(deadline) {
				next := syntaxCheckpoint{at: pos, tex: state}
				points = append(points, next)
				d.replaceSyntax(start, pos, tokens, points)
				d.stopAt(pos)
				return pos - start, nil
			}
			at := pos
			if state.verbatim != "" {
				if verbatimName != state.verbatim {
					verbatimName = state.verbatim
					closing = []rune("\\end{" + state.verbatim + "}")
				}
				if !runePrefix(text[pos:end], closing) {
					emit(pos, pos+1, chroma.LiteralString)
					pos++
					continue
				}
				emit(pos, pos+len(closing), chroma.Keyword)
				pos += len(closing)
				state.verbatim = ""
				continue
			}
			if text[pos] == '%' {
				emit(pos, end, chroma.Comment)
				if strings.HasPrefix(string(text[pos:end]), "%stopzone") {
					state = texSyntaxState{}
				}
				pos = end
				continue
			}
			if text[pos] == '\\' {
				pos++
				for pos < end && (unicode.IsLetter(text[pos]) || text[pos] == '@') {
					pos++
				}
				if pos == at+1 && pos < end {
					pos++
				}
				command := string(text[at+1 : pos])
				kind := chroma.Keyword
				switch command {
				case "(", "[":
					if state.math == "" {
						state.math = command
					}
				case ")", "]":
					if command == ")" && state.math == "(" || command == "]" && state.math == "[" {
						state.math = ""
					}
				case "begin", "end":
					arg := pos
					for arg < end && (text[arg] == ' ' || text[arg] == '\t') {
						arg++
					}
					if arg < end && text[arg] == '{' {
						last := arg + 1
						for last < end && text[last] != '}' && text[last] != '\n' {
							last++
						}
						if last < end && text[last] == '}' {
							name := string(text[arg+1 : last])
							emit(at, pos, kind)
							emit(arg, last+1, chroma.CommentPreproc)
							pos = last + 1
							if texMathEnvironment(name) {
								if command == "begin" {
									if len(state.environments) > 4096 {
										return pos - start, fmt.Errorf("TeX syntax nesting limit exceeded")
									}
									state.environments += "|" + name
								} else if strings.HasSuffix(state.environments, "|"+name) {
									state.environments = strings.TrimSuffix(state.environments, "|"+name)
								}
							}
							if command == "begin" {
								switch name {
								case "verbatim", "verbatim*", "Verbatim", "lstlisting", "minted", "comment":
									state.verbatim = name
								}
							}
							continue
						}
					}
				case "verb":
					emit(at, pos, kind)
					if pos < end && text[pos] == '*' {
						pos++
					}
					if pos < end && text[pos] != '\n' {
						delim := text[pos]
						first := pos
						pos++
						for pos < end && text[pos] != delim && text[pos] != '\n' {
							pos++
						}
						if pos < end && text[pos] == delim {
							pos++
						}
						emit(first, pos, chroma.LiteralString)
					}
					continue
				case "section", "subsection", "subsubsection", "chapter", "part", "paragraph", "subparagraph", "title", "author", "documentclass", "usepackage":
					kind = chroma.CommentPreproc
				case "text", "textrm", "textnormal", "mbox", "hbox":
					state.textPending = state.math != "" || state.environments != ""
				case "%", "$", "&", "#", "_", "{", "}":
					kind = chroma.LiteralStringEscape
				}
				emit(at, pos, kind)
				continue
			}
			if state.textPending && text[pos] == '{' {
				state.textDepth = 1
				state.textPending = false
				emit(pos, pos+1, chroma.Punctuation)
				pos++
				continue
			}
			if state.textDepth > 0 {
				if text[pos] == '{' {
					state.textDepth++
				} else if text[pos] == '}' {
					state.textDepth--
				}
				pos++
				continue
			}
			if text[pos] == '$' {
				pos++
				if pos < end && text[pos] == '$' {
					pos++
				}
				delim := string(text[at:pos])
				if state.math == delim {
					state.math = ""
				} else if state.math == "" {
					state.math = delim
				}
				emit(at, pos, chroma.Keyword)
				continue
			}
			kind := chroma.Text
			if state.math != "" || state.environments != "" {
				kind = chroma.GenericOutput // texMath -> Special
			} else if strings.ContainsRune("{}[]&#_^~", text[pos]) {
				kind = chroma.Punctuation
			}
			emit(pos, pos+1, kind)
			pos++
		}
		next := syntaxCheckpoint{at: pos, tex: state}
		points = append(points, next)
		converged := false
		if pos > start && pos > d.damageEnd {
			index := sort.Search(len(d.points), func(i int) bool { return d.points[i].at >= pos })
			converged = index < len(d.points) && d.points[index].at == pos && equalSyntaxState(d.points[index], next)
		}
		if converged || pos >= target || time.Now().After(deadline) {
			d.replaceSyntax(start, pos, tokens, points)
			d.dirty = pos
			if converged {
				d.dirty = max(pos, d.validUntil)
			} else {
				d.stopAt(pos)
			}
			d.validUntil = max(d.validUntil, pos)
			return pos - start, nil
		}
	}
	d.replaceSyntax(start, pos, tokens, points)
	d.dirty, d.validUntil = pos, pos
	return pos - start, nil
}

func runePrefix(text, prefix []rune) bool {
	if len(prefix) > len(text) {
		return false
	}
	for i, r := range prefix {
		if text[i] != r {
			return false
		}
	}
	return true
}
