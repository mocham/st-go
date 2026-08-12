package main

import (
	"bufio"
	"fmt"
	"strings"
)

// displayText sanitizes a filename for terminal output, replicating the
// shell's `printf %q`: control characters are ANSI-C escaped inside $'...',
// and shell-special printable characters are backslash-escaped, so no
// filename can inject terminal control sequences.
func displayText(s string) string {
	hasCtrl := false
	for i := 0; i < len(s); i++ {
		if c := s[i]; c < 0x20 || c == 0x7f {
			hasCtrl = true
			break
		}
	}
	if !hasCtrl {
		var out strings.Builder
		for i := 0; i < len(s); i++ {
			c := s[i]
			if strings.ContainsRune(" !\"#$&'()*,;<>?[\\]^`{|}~", rune(c)) {
				out.WriteByte('\\')
			}
			out.WriteByte(c)
		}
		return out.String()
	}
	var out strings.Builder
	out.WriteString("$'")
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch c {
		case '\n':
			out.WriteString(`\n`)
		case '\t':
			out.WriteString(`\t`)
		case '\r':
			out.WriteString(`\r`)
		case '\a':
			out.WriteString(`\a`)
		case '\b':
			out.WriteString(`\b`)
		case '\v':
			out.WriteString(`\v`)
		case '\f':
			out.WriteString(`\f`)
		case 0x1b:
			out.WriteString(`\E`)
		case '\\':
			out.WriteString(`\\`)
		case '\'':
			out.WriteString(`\'`)
		default:
			if c < 0x20 || c == 0x7f {
				fmt.Fprintf(&out, `\%03o`, c)
			} else {
				out.WriteByte(c)
			}
		}
	}
	out.WriteByte('\'')
	return out.String()
}

// shorten truncates text to width bytes, appending "..." like the shell.
func shorten(text string, width int) string {
	if width <= 0 {
		return ""
	}
	if len(text) > width {
		if width > 3 {
			return text[:width-3] + "..."
		}
		return text[:width]
	}
	return text
}

func (b *Browser) drawText(row, col, width int, style, text string) {
	if row < 1 || row > b.rows || width < 1 {
		return
	}
	s := shorten(text, width)
	b.cup(row, col)
	fmt.Fprintf(b.out, "\x1b[%dX%s%s%s", width, style, s, resetStyle)
}

func (b *Browser) drawHeader() {
	path := displayText(b.dirLabel)
	fmt.Fprintf(b.out, "\x1b[1;1H%s%*s\x1b[1;2H", headerStyle, b.cols, "")
	fmt.Fprint(b.out, shorten("st-go files  "+path, b.cols-14))
	fmt.Fprintf(b.out, "\x1b[1;%dH%4d items%s", b.cols-11, len(b.files), resetStyle)
}

func (b *Browser) drawInfo() {
	b.drawText(2, b.listX, b.listW, infoStyle, " SELECTED")
	b.drawText(3, b.listX, b.listW, selectedStyle, " "+b.infoName)
	b.drawText(4, b.listX, b.listW, resetStyle, " type  "+b.infoKind)
	b.drawText(5, b.listX, b.listW, resetStyle, " size  "+b.infoSize+"   "+b.infoMode)
	b.drawText(6, b.listX, b.listW, dimStyle, " time  "+b.infoTime)
	first, last := 0, 0
	count := len(b.files)
	if count > 0 {
		first = b.viewTop + 1
		last = b.viewTop + b.visible
		if last > count {
			last = count
		}
	}
	b.drawText(7, b.listX, b.listW, infoStyle, fmt.Sprintf(" FILES  %d-%d / %d", first, last, count))
}

func (b *Browser) drawListRow(slot int) {
	row := b.listTop + slot
	index := b.viewTop + slot
	if row > b.listBottom {
		return
	}
	if index >= len(b.files) {
		b.drawText(row, b.listX, b.listW, resetStyle, "")
		if index == 0 && len(b.files) == 0 {
			b.drawText(row, b.listX, b.listW, dimStyle, " (empty)")
		}
		return
	}
	path := b.files[index]
	label := " " + b.entryLabel(path)
	style := selectedStyle
	if index != b.idx {
		style = b.entryStyle(path)
	}
	b.drawText(row, b.listX, b.listW, style, label)
}

func (b *Browser) drawList() {
	for slot := 0; slot < b.visible; slot++ {
		b.drawListRow(slot)
	}
}

func (b *Browser) drawDivider() {
	col := b.listX - 1
	for row := 2; row < b.rows; row++ {
		b.cup(row, col)
		fmt.Fprintf(b.out, "%s|%s", dimStyle, resetStyle)
	}
}

func (b *Browser) drawStatus() {
	help := "  arrows  Enter open  / path  : cmd  . hidden  r ref  q quit"
	fmt.Fprintf(b.out, "\x1b[%d;1H%s%*s\x1b[%d;2H", b.rows, statusStyle, b.cols, "", b.rows)
	fmt.Fprint(b.out, shorten(b.status, b.cols-len(help)-3))
	if b.cols >= 76 {
		fmt.Fprintf(b.out, "\x1b[%d;%dH%s", b.rows, b.cols-len(help)+1, help)
	}
	fmt.Fprint(b.out, resetStyle)
	b.flush()
}

func (b *Browser) drawOverlay() {
	b.drawHeader()
	b.drawInfo()
	b.drawList()
	b.drawDivider()
	b.drawStatus()
}

func (b *Browser) clearPathPopup() {
	first := b.rows - pathPopupMax
	if first < 2 {
		first = 2
	}
	for row := first; row < b.rows; row++ {
		b.cup(row, 1)
		fmt.Fprintf(b.out, "\x1b[%dX", b.cols)
	}
}

func (b *Browser) clearPreview() {
	for row := 2; row < b.rows; row++ {
		b.cup(row, 1)
		fmt.Fprintf(b.out, "\x1b[%dX", b.previewW)
	}
}

func (b *Browser) drawPreviewTitle(s string) {
	b.drawText(2, 2, b.previewW-2, infoStyle, s)
}

func (b *Browser) drawPreviewMessage(title, msg, extra string) {
	b.drawPreviewTitle(title)
	b.drawText(4, 3, b.previewW-4, resetStyle, msg)
	if extra != "" {
		b.drawText(6, 3, b.previewW-4, dimStyle, extra)
	}
}

// buildUIOut is used by tests to capture drawn output without an X server.
func (b *Browser) setOutput(w *bufio.Writer) {
	b.out = w
}
