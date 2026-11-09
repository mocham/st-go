package main

import (
	"strings"
	"unicode"
)

type span struct{ start, end int }

func wordClass(r rune, big bool) int {
	if unicode.IsSpace(r) {
		return 0
	}
	if big || unicode.IsLetter(r) || unicode.IsDigit(r) || r == '_' || unicode.IsMark(r) {
		return 1
	}
	return 2
}
func wordForward(text []rune, pos, count int, big, end bool) int {
	if len(text) == 0 {
		return 0
	}
	at := clamp(pos, 0, len(text)-1)
	for i := 0; i < count; i++ {
		if end {
			if at < len(text)-1 {
				at++
			}
			for at < len(text)-1 && wordClass(text[at], big) == 0 {
				at++
			}
			class := wordClass(text[at], big)
			for at < len(text)-1 && wordClass(text[at+1], big) == class {
				at++
			}
		} else {
			class := wordClass(text[at], big)
			for at < len(text) && wordClass(text[at], big) == class {
				if class == 0 && text[at] == '\n' && (at == 0 || text[at-1] == '\n') {
					at++
					break
				}
				at++
			}
			for at < len(text) && wordClass(text[at], big) == 0 {
				if text[at] == '\n' && (at == 0 || text[at-1] == '\n') {
					break
				}
				at++
			}
		}
		if at >= len(text) {
			return len(text)
		}
	}
	return at
}
func wordBackward(text []rune, pos, count int, big, end bool) int {
	at := clamp(pos, 0, len(text))
	for i := 0; i < count; i++ {
		if at > 0 {
			at--
		}
		if end {
			for at > 0 && wordClass(text[at], big) == wordClass(text[min(len(text)-1, at+1)], big) {
				at--
			}
		}
		for at > 0 && wordClass(text[at], big) == 0 {
			at--
		}
		if !end && len(text) > 0 {
			class := wordClass(text[at], big)
			for at > 0 && wordClass(text[at-1], big) == class {
				at--
			}
		}
	}
	return at
}

func (e *Editor) motion(key string, count int) (target int, line, inclusive, ok bool) {
	b, w := e.buffer(), e.window()
	pos := e.pos()
	row := b.row(pos)
	start, end := b.start(row), b.end(row)
	last := len(b.lineStarts()) - 1
	target, ok = pos, true
	switch key {
	case "h", "<Left>", "\b", "<BS>":
		target = max(start, pos-count)
	case "l", "<Right>", " ":
		target = min(end, pos+count)
	case "j", "k", "<Up>", "<Down>":
		goal := w.goal
		if goal < 0 {
			goal = logicalColumn(b.line(row), pos-start)
		}
		e.motionGoal = goal
		delta := count
		if key == "k" || key == "<Up>" {
			delta = -count
		}
		r := clamp(row+delta, 0, last)
		target = b.start(r) + columnOffset(b.line(r), goal)
		line = true
	case "0", "<Home>":
		target = start
	case "^":
		target = firstNonblank(b, row)
	case "$", "<End>":
		r := min(last, row+count-1)
		target = max(b.start(r), b.end(r)-1)
		inclusive = true
	case "g_":
		r := min(last, row+count-1)
		target = b.end(r)
		for target > b.start(r) && unicode.IsSpace(b.text[target-1]) {
			target--
		}
		target = max(b.start(r), target-1)
		inclusive = true
	case "|":
		target = start + columnOffset(b.line(row), count-1)
	case "w", "W":
		target = wordForward(b.text, pos, count, key == "W", false)
	case "e", "E":
		target = wordForward(b.text, pos, count, key == "E", true)
		inclusive = true
	case "b", "B":
		target = wordBackward(b.text, pos, count, key == "B", false)
	case "ge", "gE":
		target = wordBackward(b.text, pos, count, key == "gE", true)
		inclusive = true
	case "gg":
		r := 0
		if e.count != "" {
			r = clamp(count-1, 0, last)
		}
		target = firstNonblank(b, r)
		line = true
	case "G":
		r := last
		if e.count != "" {
			r = clamp(count-1, 0, last)
		}
		target = firstNonblank(b, r)
		line = true
	case "_":
		target = firstNonblank(b, min(last, row+count-1))
		line = true
	case "+", "<Enter>", "\r", "\n":
		target = firstNonblank(b, min(last, row+count))
		line = true
	case "-":
		target = firstNonblank(b, max(0, row-count))
		line = true
	case "H", "M", "L":
		r := w.topLine
		if key == "M" {
			r += max(0, (w.rect.h-2)/2)
		}
		if key == "L" {
			r += max(0, w.rect.h-2) - count + 1
		} else if key == "H" {
			r += count - 1
		}
		target = firstNonblank(b, clamp(r, 0, last))
		line = true
	case "{", "}":
		r := row
		direction := 1
		if key == "{" {
			direction = -1
		}
		for i := 0; i < count; i++ {
			r = clamp(r+direction, 0, last)
			for r > 0 && r < last && len(strings.TrimSpace(string(b.line(r)))) == 0 {
				r += direction
			}
			for r > 0 && r < last && len(strings.TrimSpace(string(b.line(r)))) != 0 {
				r += direction
			}
		}
		target = b.start(r)
	case "(", ")":
		direction := 1
		if key == "(" {
			direction = -1
		}
		at := pos
		for i := 0; i < count; i++ {
			at = clamp(at+direction, 0, len(b.text))
			for at > 0 && at < len(b.text) {
				if sentenceStart(b.text, at) {
					break
				}
				at += direction
			}
		}
		target = at
	case "%":
		if e.count != "" {
			target = firstNonblank(b, clamp((last+1)*min(count, 100)/100-1, 0, last))
			line = true
		} else {
			at := pos
			for at < end && !strings.ContainsRune("()[]{}", b.text[at]) {
				at++
			}
			if at < end {
				if pair, found := matchingBracket(b.text, at); found {
					target = pair
					inclusive = true
				}
			}
		}
	case ";", ",":
		find := e.findKey
		if key == "," {
			find = map[string]string{"f": "F", "F": "f", "t": "T", "T": "t"}[find]
		}
		if find != "" {
			original := w.cursor
			if find == "t" && w.cursor < b.end(row) {
				w.cursor++
			}
			if find == "T" && w.cursor > b.start(row) {
				w.cursor--
			}
			if at, found := e.findCharacter(find, e.findRune, count); found {
				target = at
				inclusive = find == "f" || find == "t"
			}
			w.cursor = original
		}
	case "n", "N":
		if e.operator == "" {
			ok = false
			break
		}
		match, _, err := e.nextSearchMatch(key == "N", count)
		if err != nil {
			e.error(err)
			target = pos
		} else {
			target = match.start
			e.search.active = match
			e.search.highlight = true
		}
	case "g0", "g^", "g$", "gm":
		width := max(1, w.rect.w-numberWidth(b)-1)
		column := logicalColumn(b.line(row), pos-start)
		base := (column / width) * width
		switch key {
		case "g$":
			base += width - 1
			inclusive = true
		case "gm":
			base += width / 2
		}
		target = start + columnOffset(b.line(row), base)
		if key == "g^" {
			for target < end && (b.text[target] == ' ' || b.text[target] == '\t') {
				target++
			}
		}
	case "gM":
		target = start + len(b.line(row))/2
	case "gj", "gk":
		width := max(1, w.rect.w-numberWidth(b)-1)
		column := logicalColumn(b.line(row), pos-start)
		delta := width * count
		if key == "gk" {
			delta = -delta
		}
		desired := column + delta
		if desired < 0 && row > 0 {
			target = b.start(row-1) + columnOffset(b.line(row-1), max(0, logicalColumn(b.line(row-1), len(b.line(row-1)))+desired))
		} else if desired > logicalColumn(b.line(row), len(b.line(row))) && row < last {
			target = b.start(row+1) + columnOffset(b.line(row+1), desired-logicalColumn(b.line(row), len(b.line(row))))
		} else {
			target = start + columnOffset(b.line(row), max(0, desired))
		}
	case "[[", "]]", "[]", "][":
		direction := 1
		if key[0] == '[' {
			direction = -1
		}
		r := row
		for i := 0; i < count; i++ {
			r = clamp(r+direction, 0, last)
			for r > 0 && r < last {
				value := strings.TrimSpace(string(b.line(r)))
				if strings.HasPrefix(value, "{") || strings.HasPrefix(value, "}") || strings.HasPrefix(value, "\\section") || strings.HasPrefix(value, "# ") {
					break
				}
				r += direction
			}
		}
		target = b.start(r)
		line = true
	default:
		ok = false
	}
	target = clamp(target, 0, len(b.text))
	return
}

func sentenceStart(text []rune, pos int) bool {
	if pos == 0 {
		return true
	}
	if pos >= len(text) || unicode.IsSpace(text[pos]) {
		return false
	}
	at := pos - 1
	if !unicode.IsSpace(text[at]) {
		return false
	}
	for at > 0 && unicode.IsSpace(text[at]) {
		if text[at] == '\n' && text[at-1] == '\n' {
			return true
		}
		at--
	}
	for at > 0 && strings.ContainsRune("]\"')", text[at]) {
		at--
	}
	return strings.ContainsRune(".!?", text[at])
}
func matchingBracket(text []rune, pos int) (int, bool) {
	if pos < 0 || pos >= len(text) {
		return 0, false
	}
	r := text[pos]
	pairs := map[rune]rune{'(': ')', '[': ']', '{': '}', ')': '(', ']': '[', '}': '{', '<': '>', '>': '<'}
	pair, ok := pairs[r]
	if !ok {
		return 0, false
	}
	direction := 1
	if strings.ContainsRune(")]}", r) || r == '>' {
		direction = -1
	}
	depth := 1
	for at := pos + direction; at >= 0 && at < len(text); at += direction {
		if at > 0 && text[at-1] == '\\' {
			continue
		}
		if text[at] == r {
			depth++
		}
		if text[at] == pair {
			depth--
			if depth == 0 {
				return at, true
			}
		}
	}
	return 0, false
}
func (e *Editor) findCharacter(key string, r rune, count int) (int, bool) {
	b := e.buffer()
	row := b.row(e.pos())
	direction := 1
	if key == "F" || key == "T" {
		direction = -1
	}
	at := e.pos()
	// A repeated till must step past its previous target, not rediscover it.
	for i := 0; i < count; i++ {
		at += direction
		for at >= b.start(row) && at < b.end(row) && b.text[at] != r {
			at += direction
		}
		if at < b.start(row) || at >= b.end(row) {
			return e.pos(), false
		}
	}
	if key == "t" {
		at--
	}
	if key == "T" {
		at++
	}
	return at, true
}

func (e *Editor) textObject(key string, around bool, count int) (int, int, bool, bool) {
	b := e.buffer()
	text := b.text
	pos := e.pos()
	if len(text) == 0 {
		return pos, pos, false, false
	}
	if key == "w" || key == "W" {
		big := key == "W"
		pos = min(pos, len(text)-1)
		class := wordClass(text[pos], big)
		start, end := pos, pos+1
		for start > 0 && wordClass(text[start-1], big) == class {
			if class == 0 && text[start-1] == '\n' {
				break
			}
			start--
		}
		for end < len(text) && wordClass(text[end], big) == class {
			if class == 0 && text[end] == '\n' {
				break
			}
			end++
		}
		for i := 1; i < count; i++ {
			for end < len(text) && wordClass(text[end], big) == 0 {
				end++
			}
			if end < len(text) {
				class = wordClass(text[end], big)
				for end < len(text) && wordClass(text[end], big) == class {
					end++
				}
			}
		}
		if around {
			if class == 0 && end < len(text) && text[end] != '\n' {
				nextClass := wordClass(text[end], big)
				for end < len(text) && wordClass(text[end], big) == nextClass {
					end++
				}
				return start, end, false, true
			}
			oldEnd := end
			for end < len(text) && unicode.IsSpace(text[end]) && text[end] != '\n' {
				end++
			}
			if end == oldEnd {
				for start > 0 && unicode.IsSpace(text[start-1]) && text[start-1] != '\n' {
					start--
				}
			}
		}
		return start, end, false, true
	}
	if key == "p" {
		row := b.row(pos)
		first, last := row, row
		blank := func(r int) bool { return strings.TrimSpace(string(b.line(r))) == "" }
		for first > 0 && !blank(first-1) {
			first--
		}
		for i := 0; i < count; i++ {
			for last+1 < len(b.lineStarts()) && !blank(last+1) {
				last++
			}
			if i+1 < count && last+1 < len(b.lineStarts()) {
				last++
				for last+1 < len(b.lineStarts()) && blank(last) {
					last++
				}
			}
		}
		if around {
			for last+1 < len(b.lineStarts()) && blank(last+1) {
				last++
			}
		}
		return b.start(first), b.end(last), true, true
	}
	if key == "s" {
		start, end := pos, pos
		for start > 0 && !sentenceStart(text, start) {
			start--
		}
		for i := 0; i < count; i++ {
			end = min(len(text), end+1)
			for end < len(text) && !sentenceStart(text, end) {
				end++
			}
		}
		if !around {
			for end > start && unicode.IsSpace(text[end-1]) {
				end--
			}
		}
		return start, end, false, true
	}
	if key == "\"" || key == "'" || key == "`" {
		r := []rune(key)[0]
		row := b.row(pos)
		start := -1
		for at := b.start(row); at < b.end(row); at++ {
			if text[at] != r || (at > 0 && text[at-1] == '\\') {
				continue
			}
			if start < 0 {
				start = at
			} else {
				if pos >= start && pos <= at {
					if around {
						return start, at + 1, false, true
					}
					return start + 1, at, false, true
				}
				start = -1
			}
		}
		return 0, 0, false, false
	}
	open := map[string]rune{"(": '(', ")": '(', "b": '(', "[": '[', "]": '[', "{": '{', "}": '{', "B": '{', "<": '<', ">": '<'}[key]
	if open != 0 {
		start := -1
		end := -1
		for at := min(pos, len(text)-1); at >= 0; at-- {
			if text[at] == open {
				if pair, ok := matchingBracket(text, at); ok && pair >= pos {
					count--
					if count == 0 {
						start, end = at, pair
						break
					}
				}
			}
		}
		if start >= 0 {
			if around {
				return start, end + 1, false, true
			}
			return start + 1, end, false, true
		}
		for at := pos; at < b.end(b.row(pos)); at++ {
			if text[at] == open {
				if pair, ok := matchingBracket(text, at); ok {
					if around {
						return at, pair + 1, false, true
					}
					return at + 1, pair, false, true
				}
			}
		}
	}
	return 0, 0, false, false
}
