package main

import (
	"fmt"
	"io"
	"strings"
	"unicode"
	"unicode/utf8"

	"st-go/tui/readline"
)

const (
	resetStyle          = "\x1b[0m"
	headerStyle         = "\x1b[1;38;5;231;48;5;24m"
	userStyle           = "\x1b[1;38;5;81m"
	assistantStyle      = "\x1b[1;38;5;120m"
	selectedStyle       = "\x1b[1;38;5;229m"
	selectedPromptStyle = "\x1b[1;38;5;231;48;5;24m"
	bodyStyle           = "\x1b[0;38;5;252m"
	dimStyle            = "\x1b[0;38;5;244m"
	statusStyle         = "\x1b[0;38;5;254;48;5;236m"
	warningStyle        = "\x1b[1;38;5;231;48;5;130m"
	promptStyle         = "\x1b[0;38;5;231;48;5;234m"
	candidateStyle      = "\x1b[0;38;5;220;48;5;234m"
)

type displayRow struct {
	text      string
	style     string
	promptID  string
	sessionID string
	action    string
	spans     []textSpan
	deleteID  string
	exportID  string
	fileID    string
}

func safeText(value string) string {
	var output strings.Builder
	for len(value) > 0 {
		r, size := utf8.DecodeRuneInString(value)
		if r == utf8.RuneError && size == 1 {
			value = value[1:]
			continue
		}
		value = value[size:]
		switch r {
		case '\n':
			output.WriteRune(r)
		case '\t':
			output.WriteByte(' ')
		default:
			if !unicode.IsControl(r) {
				output.WriteRune(r)
			}
		}
	}
	return output.String()
}

func textWidth(value string) int {
	width := 0
	for _, r := range value {
		width += readline.CellWidth(r, width)
	}
	return width
}

func truncateCells(value string, cells int) string {
	if cells <= 0 {
		return ""
	}
	value = safeText(value)
	value = strings.ReplaceAll(value, "\n", " ")
	var output strings.Builder
	used := 0
	for _, r := range value {
		width := readline.CellWidth(r, used)
		if used+width > cells {
			break
		}
		output.WriteRune(r)
		used += width
	}
	return output.String()
}

func padCells(value string, cells int) string {
	value = truncateCells(value, cells)
	return value + strings.Repeat(" ", max(0, cells-textWidth(value)))
}

func wrapLine(value string, cells int) []string {
	if cells <= 0 {
		return nil
	}
	if value == "" {
		return []string{""}
	}
	var rows []string
	var row strings.Builder
	width := 0
	for _, r := range value {
		characterWidth := readline.CellWidth(r, width)
		if width > 0 && width+characterWidth > cells {
			rows = append(rows, row.String())
			row.Reset()
			width = 0
			characterWidth = readline.CellWidth(r, width)
		}
		if characterWidth > cells {
			r = '?'
			characterWidth = 1
		}
		row.WriteRune(r)
		width += characterWidth
	}
	rows = append(rows, row.String())
	return rows
}

func wrapText(value string, cells int) []string {
	value = safeText(value)
	var rows []string
	for _, line := range strings.Split(value, "\n") {
		rows = append(rows, wrapLine(line, cells)...)
	}
	return rows
}

func shortID(id string) string {
	if len(id) <= 12 {
		return id
	}
	return id[:12]
}

func (application *app) header() string {
	if application.headerHeight() == 1 {
		return application.compactDetails()
	}
	return " Model: " + application.modelLabel()
}

func (application *app) statusLine() (string, string) {
	if dialog := application.exportDialog; dialog != nil {
		if dialog.err != "" {
			return " " + dialog.err, warningStyle
		}
		if dialog.submitting {
			return " Exporting full session history… · Esc cancels ", statusStyle
		}
		return " Enter saves the GoLS archive · Esc cancels ", statusStyle
	}
	if confirmation := application.deleteConfirmation; confirmation != nil {
		if confirmation.err != "" {
			return " " + confirmation.err, warningStyle
		}
		return " Type DELETE to confirm session deletion · Esc cancels ", warningStyle
	}
	if application.attachTarget != "" {
		return " Opening session… · Esc cancels ", statusStyle
	}
	if len(application.permissions) > 0 {
		request := application.permissions[0]
		detail := request.Message
		if detail == "" {
			detail = request.Action
		}
		if len(request.Resources) > 0 {
			detail += " · " + strings.Join(request.Resources, ", ")
		}
		if application.permissionBusy {
			return " Permission response sending… ", warningStyle
		}
		return " PERMISSION  " + detail + "  [y] once  [a] always  [n] reject ", warningStyle
	}
	if picker := application.picker; picker != nil {
		if picker.err != "" {
			return " " + picker.err, warningStyle
		}
		if picker.submitting {
			return " Applying selection… ", statusStyle
		}
		if picker.loading {
			return " Loading " + picker.kind + "… ", statusStyle
		}
		options := picker.visible()
		if len(options) > 0 {
			selected := max(0, min(picker.selected, len(options)-1))
			return fmt.Sprintf(" %d/%d · %s ", selected+1, len(options), options[selected].detail), statusStyle
		}
		return " No matches · edit filter or Esc to close ", statusStyle
	}
	connection := "offline"
	if application.streamConnected {
		connection = "live"
	}
	return " " + application.status + " · events " + connection + " ", statusStyle
}

func (application *app) helpLine() string {
	if application.exportDialog != nil {
		return "Edit filepath · Enter exports · Esc/Ctrl-C cancel · Ctrl-Q quit"
	}
	if application.deleteConfirmation != nil {
		return "Enter confirms only DELETE · Esc/Ctrl-C cancel · Ctrl-Q quit"
	}
	if len(application.permissions) > 0 {
		return "Permission input has focus · Ctrl-U/D preview page · Ctrl-C interrupt · Ctrl-Q quit · PgUp/PgDn prompts"
	}
	if application.picker != nil {
		return "↑/↓ select · Enter apply · Esc cancel · type to filter · PgUp/PgDn · Ctrl-R reload"
	}
	if application.filesMode {
		return "Tab prompts · Ctrl-U/D preview page · Click file to preview · Alt-↑/↓ file · F3 reload · PgUp/PgDn files · Ctrl-Q quit"
	}
	previewKey := "F2 rich pane"
	if application.preview == nil || !application.preview.enabled {
		previewKey = "text response"
	}
	return "Tab files · Ctrl-U/D preview page · Reclick prompt: end · More/F3 +10 · Alt-↑/↓ select · /sessions /models /variants /export · Ctrl-Q quit · " + previewKey
}

func writeFull(writer io.Writer, data []byte) error {
	for len(data) > 0 {
		count, err := writer.Write(data)
		if err != nil {
			return err
		}
		if count == 0 {
			return io.ErrShortWrite
		}
		data = data[count:]
	}
	return nil
}

func drawAt(output *strings.Builder, row int, style, text string, width int) {
	fmt.Fprintf(output, "\x1b[%d;1H\x1b[2K%s%s%s", row, style, truncateCells(text, width), resetStyle)
}

func drawCells(output *strings.Builder, x, y, width int, style, text string) {
	if width <= 0 {
		return
	}
	fmt.Fprintf(output, "\x1b[%d;%dH%s%s%s", y, x, style, padCells(text, width), resetStyle)
}

func (application *app) drawPanes(output *strings.Builder) {
	layout := application.bodyLayout()
	application.paintedLayout = layout
	application.sidebarHits = make(map[int]sidebarHit)
	application.deleteHits = make(map[int]string)
	application.exportHits = make(map[int]string)
	if layout.titleRow != 0 {
		title := "SESSIONS · Tab message files"
		if application.filesMode {
			title = "MESSAGE FILES · Tab prompts"
		}
		drawCells(output, 1, layout.titleRow, layout.prompts.width, dimStyle, title)
		if layout.response.width > 0 {
			label := "RESPONSE"
			if application.filesMode && application.fileSelection != nil {
				label = application.fileSelection.ref.Name
			}
			drawCells(output, layout.response.x, layout.titleRow, layout.response.width, assistantStyle, label)
		}
	}
	if layout.divider != 0 {
		for row := application.headerHeight() + 1; row <= application.rows-3; row++ {
			drawCells(output, layout.divider, row, 1, dimStyle, "│")
		}
	}
	rows := application.sidebarRows(layout.prompts.width)
	if application.filesMode {
		rows = application.fileRows(layout.prompts.width)
	}
	if application.sidebarFocus != "" {
		for index, row := range rows {
			if row.sessionID == application.sidebarFocus {
				application.scroll = index
				break
			}
		}
		application.sidebarFocus = ""
	}
	application.scroll = max(0, min(application.scroll, max(0, len(rows)-layout.prompts.height)))
	start := application.scroll
	end := min(len(rows), start+layout.prompts.height)
	for slot, row := range rows[start:end] {
		y := layout.prompts.y + slot
		drawCells(output, 1, y, layout.prompts.width, row.style, row.text)
		if len(row.spans) > 0 {
			x := 1
			for _, span := range row.spans {
				width := min(textWidth(span.text), layout.prompts.width-x+1)
				drawCells(output, x, y, width, span.style, span.text)
				x += width
			}
		}
		if row.deleteID != "" {
			label := "[Delete]"
			if application.deleting[row.deleteID] {
				label = "[Wait…]"
			}
			drawCells(output, layout.prompts.width-8, y, 8, warningStyle, label)
			application.deleteHits[y] = row.deleteID
		}
		if row.exportID != "" {
			drawCells(output, layout.prompts.width-8, y, 8, sessionNameStyle, "[Export]")
			application.exportHits[y] = row.exportID
		}
		switch {
		case row.fileID != "":
			application.sidebarHits[y] = sidebarHit{"file", row.fileID}
		case row.sessionID != "":
			application.sidebarHits[y] = sidebarHit{"session", row.sessionID}
		case row.promptID != "":
			application.sidebarHits[y] = sidebarHit{"prompt", row.promptID}
		case row.action != "":
			application.sidebarHits[y] = sidebarHit{kind: row.action}
		}
	}
	if layout.response.width > 0 {
		lines := wrapText(application.selectedResponse(), layout.response.width)
		application.responseScroll = max(0, min(application.responseScroll, max(0, len(lines)-layout.response.height)))
		end := min(len(lines), application.responseScroll+layout.response.height)
		for slot, text := range lines[application.responseScroll:end] {
			drawCells(output, layout.response.x, layout.response.y+slot, layout.response.width, bodyStyle, text)
		}
	}
}

func (application *app) draw() error {
	application.rows = max(5, min(500, application.rows))
	application.cols = max(8, min(1000, application.cols))
	headers := application.headerHeight()
	bodyHeight := max(1, application.rows-headers-3)
	var visible []displayRow
	header := application.header()
	if application.deleteConfirmation != nil {
		visible = application.deleteConfirmation.rows(application.cols)
	} else if application.exportDialog != nil {
		visible = application.exportDialog.rows(application.cols)
	} else if application.picker != nil {
		visible = append([]displayRow{{text: " /" + application.picker.kind + " · " + application.session.Title, style: headerStyle}}, application.picker.rows(max(0, bodyHeight-1))...)
	}

	var output strings.Builder
	output.Grow(application.rows * (application.cols + 32))
	output.WriteString("\x1b[?2026h\x1b[?25l")
	drawAt(&output, 1, headerStyle, padCells(header, application.cols), application.cols)
	if headers == 2 {
		drawAt(&output, 2, headerStyle, padCells(application.detailsLine(), application.cols), application.cols)
	}
	for slot := 0; slot < bodyHeight; slot++ {
		row := slot + headers + 1
		if slot < len(visible) {
			drawAt(&output, row, visible[slot].style, visible[slot].text, application.cols)
		} else {
			drawAt(&output, row, resetStyle, "", application.cols)
		}
	}
	if application.picker == nil && application.deleteConfirmation == nil && application.exportDialog == nil {
		application.drawPanes(&output)
	} else {
		application.sidebarHits = nil
		application.deleteHits = nil
		application.exportHits = nil
	}
	status, style := application.statusLine()
	drawAt(&output, application.rows-2, style, padCells(status, application.cols), application.cols)

	display := application.activeInput().ViewDisplay(application.cols, readline.CellWidth)
	fmt.Fprintf(&output, "\x1b[%d;1H\x1b[2K%s%s", application.rows-1, promptStyle, display.Before)
	if display.Candidates != "" {
		output.WriteString(candidateStyle)
		output.WriteString(display.Candidates)
		output.WriteString(promptStyle)
	}
	output.WriteString(display.After)
	output.WriteString(resetStyle)
	drawAt(&output, application.rows, dimStyle, application.helpLine(), application.cols)
	fmt.Fprintf(&output, "\x1b[%d;%dH\x1b[6 q\x1b[?25h\x1b[?2026l", application.rows-1, min(application.cols, display.Cursor+1))
	if err := writeFull(application.out, []byte(output.String())); err != nil {
		return err
	}
	application.dirty = false
	application.paintedRows, application.paintedCols = application.rows, application.cols
	return nil
}
