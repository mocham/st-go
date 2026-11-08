package main

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"
	"unicode"

	"golang.org/x/text/unicode/norm"
	"st-go/limits"
	"st-go/pdfmeta"
	"st-go/term"
)

type panelMode uint8

const (
	panelNone panelMode = iota
	panelTOC
	panelText
)

type metadataRequest struct {
	token, page int
	kind        string
	data        []byte
	started     time.Time
}

func (v *viewer) togglePanel(mode panelMode) {
	if v.panel == mode {
		mode = panelNone
	}
	v.showPanel(mode)
}

func (v *viewer) showPanel(mode panelMode) {
	v.panel, v.panelDirty, v.lastStatus = mode, true, ""
	v.prefix, v.count, v.message, v.panelError = "", "", "", ""
	v.pendingMetadata = nil
	v.write("\x1b[?1000l\x1b[?1006l")
	if mode == panelNone {
		v.write("\x1bPregion stack 1\x1b\\")
	} else {
		width := 65 // PDF remains on the right; the left is ordinary terminal cells.
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
	if v.panel == panelNone || v.loading || v.failed || v.pages < 1 || v.panelError != "" {
		return
	}
	kind, page := "toc", 0
	if v.panel == panelTOC && v.tocLoaded {
		return
	}
	if v.panel == panelText {
		kind, page = "text", v.page
		if v.textPage == page {
			if v.pendingMetadata != nil {
				v.pendingMetadata, v.panelDirty = nil, true
			}
			return
		}
	}
	if v.pendingMetadata != nil && v.pendingMetadata.kind == kind && v.pendingMetadata.page == page {
		return
	}
	v.metadataToken = v.metadataToken%(1<<30) + 1
	v.pendingMetadata = &metadataRequest{token: v.metadataToken, kind: kind, page: page, started: time.Now()}
	v.panelDirty = true
	v.requestMetadataChunk()
}

func (v *viewer) requestMetadataChunk() {
	r := v.pendingMetadata
	if r == nil {
		return
	}
	if r.kind == "toc" {
		v.command(fmt.Sprintf("toc %d %d", r.token, len(r.data)))
	} else {
		v.command(fmt.Sprintf("text %d %d %d", r.token, r.page, len(r.data)))
	}
}

func (v *viewer) metadataReply(payload string) {
	f := strings.Fields(payload)
	r := v.pendingMetadata
	if len(f) != 9 || r == nil || f[4] != r.kind {
		return
	}
	token, e1 := strconv.Atoi(f[3])
	page, e2 := strconv.Atoi(f[5])
	offset, e3 := strconv.Atoi(f[6])
	next, e4 := strconv.Atoi(f[7])
	if e1 != nil || e2 != nil || e3 != nil || e4 != nil || token != r.token || page != r.page || offset != len(r.data) {
		return
	}
	data, err := base64.StdEncoding.DecodeString(f[8])
	if err != nil || len(data) > pdfmeta.ChunkBytes || len(r.data)+len(data) > pdfmeta.MaxBytes ||
		(next != -1 && (next != offset+len(data) || len(data) == 0)) {
		v.metadataFailure("Invalid or oversized PDF metadata reply")
		return
	}
	r.data = append(r.data, data...)
	r.started = time.Now()
	if next != -1 {
		v.requestMetadataChunk()
		return
	}
	var result pdfmeta.Payload
	if err := json.Unmarshal(r.data, &result); err != nil || len(result.TOC) > pdfmeta.MaxEntries {
		v.metadataFailure("Invalid PDF metadata")
		return
	}
	if result.Error != "" {
		v.metadataFailure(cleanPanelText(result.Error))
		return
	}
	if r.kind == "toc" {
		for _, entry := range result.TOC {
			if entry.Page < 0 || entry.Page > limits.PreviewPages || entry.Level < 0 || entry.Level > 31 {
				v.metadataFailure("Invalid PDF outline entry")
				return
			}
		}
		v.toc, v.tocLoaded = result.TOC, true
		v.tocSelection.Normalize(len(v.toc), v.panelRows())
	} else {
		v.pageText, v.textPage, v.textTop = cleanPanelText(result.Text), r.page, 0
		v.reflowText()
		v.updateTextSearch(true)
	}
	if result.Truncated {
		v.message = "PDF metadata truncated at the preview limit"
	}
	v.pendingMetadata, v.panelDirty = nil, true
}

func (v *viewer) metadataFailure(message string) {
	v.pendingMetadata, v.panelError, v.message, v.panelDirty = nil, message, message, true
}

func (v *viewer) expireMetadata(now time.Time) {
	// The native worker also scans document occurrences. A metadata request
	// queued behind a long search is not evidence of an unsupported terminal.
	if v.searchPending {
		return
	}
	if v.pendingMetadata != nil && now.Sub(v.pendingMetadata.started) > 3*time.Second {
		v.metadataFailure("PDF metadata unavailable; use updated st-go with PDF support")
	}
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
	if v.toc[index].Page == 0 {
		v.message = "This bookmark has no local page destination"
		return
	}
	v.jump("page", v.toc[index].Page)
}

func cleanPanelText(text string) string {
	text = strings.ReplaceAll(strings.ReplaceAll(text, "\r\n", "\n"), "\r", "\n")
	var out strings.Builder
	for _, r := range norm.NFC.String(text) {
		switch r {
		case '\n', '\f':
			out.WriteByte('\n')
		case '\t':
			out.WriteString("    ")
		default:
			if !unicode.IsControl(r) && !unicode.Is(unicode.Cf, r) {
				out.WriteRune(r)
			}
		}
	}
	return out.String()
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
	for offset, r := range text {
		if r == '\n' {
			lines = append(lines, line.String())
			line.Reset()
			width = 0
			offsets = append(offsets, offset+1)
			continue
		}
		cells := term.CellWidth(r)
		if cells > columns {
			r, cells = '?', 1
		}
		if width+cells > columns {
			lines = append(lines, line.String())
			line.Reset()
			width = 0
			offsets = append(offsets, offset)
		}
		line.WriteRune(r)
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

// An empty cache is not evidence that the PDF has no text. In particular, a
// panel opened during raster loading has not sent a metadata request yet.
func (v *viewer) panelNotice() string {
	if v.panelError != "" {
		return v.panelError
	}
	if v.pendingMetadata != nil {
		return "Loading page metadata..."
	}
	if (v.panel == panelTOC && !v.tocLoaded) || (v.panel == panelText && (v.textPage != v.page || v.page < 1)) {
		if v.failed {
			return "PDF unavailable (:reload to retry)"
		}
		return "Loading PDF..."
	}
	return ""
}

func (v *viewer) drawPanel() {
	if v.panel == panelNone || !v.panelDirty || v.rows < 2 {
		return
	}
	v.panelDirty = false
	title, footer := "CONTENTS  (Enter jumps)", "Tab closes | j/k move | J/K PDF page"
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
				text = "No table of contents in this PDF."
			}
		} else {
			index := v.textTop + slot
			if emptyText {
				if slot == 0 {
					text = "No text returned for this page."
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
