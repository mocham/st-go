package main

import (
	"fmt"
	"strings"
	"unicode"

	"golang.org/x/text/unicode/norm"
	"st-go/pdfmeta"
	"st-go/term"
)

type panelMode uint8

const (
	panelNone panelMode = iota
	panelTOC
	panelText
)

func (v *viewer) togglePanel(mode panelMode) {
	if v.panel == mode {
		mode = panelNone
	}
	v.showPanel(mode)
}

func (v *viewer) showPanel(mode panelMode) {
	v.panel, v.panelDirty, v.lastStatus = mode, true, ""
	v.prefix, v.count, v.message, v.panelError = "", "", "", ""
	v.write("\x1b[?1000l\x1b[?1006l")
	if mode == panelNone {
		v.write("\x1bPregion stack 1\x1b\\")
	} else {
		width := 65
		if mode == panelText {
			width = 40
		}
		v.write(fmt.Sprintf("\x1bPregion split %d%%\x1b\\", width))
		if mode == panelTOC {
			v.write("\x1b[?1000h\x1b[?1006h")
		}
	}
	v.syncPanel()
}

func (v *viewer) syncPanel() {
	if v.panel == panelNone || v.pages < 1 || v.panelError != "" {
		return
	}
	if v.panel == panelTOC {
		if v.tocLoaded || v.tocPending {
			return
		}
		v.tocToken = v.nextToken()
		v.tocPending = true
		v.backend.submit(backendJob{kind: jobTOC, token: v.tocToken})
		v.panelDirty = true
		return
	}
	if v.textPage == v.page {
		return
	}
	if v.textPending {
		// A page change supersedes an unstarted extraction request.
		v.textPending = false
	}
	v.textToken = v.nextToken()
	v.textPending = true
	v.backend.submit(backendJob{kind: jobText, token: v.textToken, page: v.page})
	v.panelDirty = true
}

func (v *viewer) panelRows() int { return max(1, v.rows-3) }

func (v *viewer) panelKey(key string) bool {
	if v.panel == panelNone {
		return false
	}
	if strings.HasPrefix(key, "mouse:") {
		if v.panel != panelTOC {
			return true
		}
		var button, x, y int
		var final rune
		if _, err := fmt.Sscanf(strings.TrimPrefix(key, "mouse:"), "<%d;%d;%d%c", &button, &x, &y, &final); err != nil || final != 'M' {
			return true
		}
		if button&64 != 0 {
			if button&1 == 0 {
				v.movePanel(-3)
			} else {
				v.movePanel(3)
			}
			return true
		}
		index := v.tocSelection.Top + y - 3
		if button&32 == 0 && button&3 == 0 && x >= 1 && x <= v.cols && y >= 3 && y < v.rows && index < len(v.toc) {
			v.tocSelection.Select(index, len(v.toc), v.panelRows())
			v.panelDirty = true
			v.jumpTOC()
		}
		return true
	}
	switch key {
	case "j", "down":
		v.movePanel(1)
	case "k", "up":
		v.movePanel(-1)
	case "pagedown", "\x06", " ":
		v.movePanel(v.panelRows())
	case "pageup", "\x02":
		v.movePanel(-v.panelRows())
	case "home":
		v.movePanel(-pdfmeta.MaxBytes)
	case "end":
		v.movePanel(pdfmeta.MaxBytes)
	case "\r", "\n":
		if v.panel == panelTOC {
			v.jumpTOC()
		}
	default:
		return false
	}
	return true
}

func (v *viewer) movePanel(delta int) {
	if v.panel == panelTOC {
		v.tocSelection.Select(v.tocSelection.Index+delta, len(v.toc), v.panelRows())
	} else {
		v.textTop = max(0, min(v.textTop+delta, max(0, len(v.textLines)-v.panelRows())))
	}
	v.panelDirty = true
}

func (v *viewer) jumpTOC() {
	index := v.tocSelection.Index
	if index < 0 || index >= len(v.toc) {
		return
	}
	if v.toc[index].Page < 1 {
		v.message = "This bookmark has no local page destination"
		return
	}
	v.jumpPage(v.toc[index].Page)
}

func cleanPanelText(text string) string {
	text = strings.ReplaceAll(strings.ReplaceAll(text, "\r\n", "\n"), "\r", "\n")
	var output strings.Builder
	for _, character := range norm.NFC.String(text) {
		switch character {
		case '\n', '\f':
			output.WriteByte('\n')
		case '\t':
			output.WriteString("    ")
		default:
			if !unicode.IsControl(character) && !unicode.Is(unicode.Cf, character) {
				output.WriteRune(character)
			}
		}
	}
	return output.String()
}

func panelLines(text string, columns int) []string {
	lines, _ := wrapPanelLines(text, columns)
	return lines
}

func wrapPanelLines(text string, columns int) ([]string, []int) {
	columns = max(1, columns)
	var lines []string
	offsets := []int{0}
	var line strings.Builder
	width := 0
	for offset, character := range text {
		if character == '\n' {
			lines = append(lines, line.String())
			line.Reset()
			width = 0
			offsets = append(offsets, offset+1)
			continue
		}
		cells := term.CellWidth(character)
		if cells > columns {
			character, cells = '?', 1
		}
		if width+cells > columns {
			lines = append(lines, line.String())
			line.Reset()
			width = 0
			offsets = append(offsets, offset)
		}
		line.WriteRune(character)
		width += cells
	}
	lines = append(lines, line.String())
	return lines, offsets
}

func (v *viewer) reflowText() {
	v.textLines, v.textLineOffsets = wrapPanelLines(v.pageText, max(1, v.cols-1))
	v.textTop = max(0, min(v.textTop, max(0, len(v.textLines)-v.panelRows())))
	v.revealTextSearch()
}

func (v *viewer) panelLine(row int, text, style string) {
	line := panelLines(cleanPanelText(text), max(1, v.cols-1))[0]
	v.write(fmt.Sprintf("\x1b[%d;1H\x1b[0m\x1b[2K%s%s\x1b[0m", row, style, line))
}

func (v *viewer) panelNotice() string {
	if v.panelError != "" {
		return v.panelError
	}
	if v.panel == panelTOC && v.tocPending || v.panel == panelText && v.textPending {
		return "Loading DjVu metadata..."
	}
	if v.panel == panelTOC && !v.tocLoaded || v.panel == panelText && (v.textPage != v.page || v.page < 1) {
		if v.failed {
			return "DjVu unavailable (:reload to retry)"
		}
		return "Loading DjVu..."
	}
	return ""
}

func (v *viewer) drawPanel() {
	if v.panel == panelNone || !v.panelDirty || v.rows < 2 {
		return
	}
	v.panelDirty = false
	title, footer := "CONTENTS  (Enter jumps)", "Tab closes | j/k move | J/K DjVu page"
	if v.panel == panelText {
		title = fmt.Sprintf("PAGE %d TEXT", v.page)
		footer = "Drag to select; Ctrl+Shift+C copies"
	}
	v.panelLine(2, title, "\x1b[1m")
	if v.rows < 4 {
		return
	}
	v.tocSelection.Normalize(len(v.toc), v.panelRows())
	emptyText := strings.TrimSpace(v.pageText) == ""
	notice := v.panelNotice()
	for slot := 0; slot < v.panelRows(); slot++ {
		text, style := "", ""
		if notice != "" {
			if slot == 0 {
				text = notice
			}
		} else if v.panel == panelTOC {
			index := v.tocSelection.Top + slot
			if index >= 0 && index < len(v.toc) {
				entry := v.toc[index]
				text = fmt.Sprintf("%s%s  [%d]", strings.Repeat("  ", entry.Level), entry.Title, entry.Page)
				if index == v.tocSelection.Index {
					style = "\x1b[7m"
				}
			} else if slot == 0 && v.tocLoaded {
				text = "No table of contents in this DjVu."
			}
		} else {
			index := v.textTop + slot
			if emptyText {
				if slot == 0 {
					text = "No hidden text returned for this page."
				}
			} else if index < len(v.textLines) {
				v.drawTextSearchLine(slot+3, index)
				continue
			}
		}
		v.panelLine(slot+3, text, style)
	}
	v.panelLine(v.rows, footer, "\x1b[2m")
}
