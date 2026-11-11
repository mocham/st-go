package main

import (
	"fmt"
	"strings"
)

func (application *app) selectPrompt(id string) {
	index, ok := application.messageIndex[id]
	if !ok || application.messages[index].Role != "user" {
		return
	}
	application.selectedPrompt = id
	application.followPreview = false
	application.responseScroll = 0
	application.status = "Showing response to selected prompt"
	application.queuePreview()
	application.dirty = true
}

// A turn may contain several assistant messages (including tool steps). Keep
// all of them together, using prompt IDs rather than the current list index.
func (application *app) selectedResponse() string {
	if application.attachTarget != "" {
		return "Loading selected session…"
	}
	var parts []string
	for _, message := range application.messages {
		if message.Role == "assistant" && message.PromptID == application.selectedPrompt && strings.TrimSpace(message.Text) != "" {
			parts = append(parts, message.Text)
		}
	}
	if len(parts) > 0 {
		return strings.Join(parts, "\n\n---\n\n")
	}
	if index, ok := application.messageIndex[application.selectedPrompt]; ok {
		message := application.messages[index]
		if message.Failed {
			return "This prompt could not be sent or was canceled."
		}
		if message.Pending || application.busy {
			return "Waiting for a response to this prompt…"
		}
		return "No response for this prompt yet."
	}
	return "Select a prompt to view its response."
}

func (application *app) promptRows(width int) []displayRow {
	var rows []displayRow
	indexes := application.promptIndexes()
	for number := len(indexes) - 1; number >= max(0, len(indexes)-application.promptLimit); number-- {
		index := indexes[number]
		message := application.messages[index]
		style, marker := userStyle, "  "
		if message.ID == application.selectedPrompt {
			style, marker = selectedPromptStyle, "› "
		}
		prefix := fmt.Sprintf("  %s%d. ", marker, len(indexes)-number)
		// On tiny terminals leave at least one cell for the prompt itself.
		prefix = truncateCells(prefix, max(0, width-1))
		indent := textWidth(prefix)
		text := message.Text
		if strings.TrimSpace(text) == "" {
			text = "(attachment)"
		}
		for line, value := range wrapText(text, max(1, width-indent)) {
			leader := strings.Repeat(" ", indent)
			if line == 0 {
				leader = prefix
			}
			rows = append(rows, displayRow{text: leader + value, style: style, promptID: message.ID})
		}
		rows = append(rows, displayRow{})
	}
	if len(rows) == 0 {
		rows = append(rows, displayRow{text: "Send a prompt below.", style: dimStyle})
	}
	return rows
}

type paneRect struct{ x, y, width, height int }

func (rect paneRect) contains(x, y int) bool {
	return x >= rect.x && x < rect.x+rect.width && y >= rect.y && y < rect.y+rect.height
}

type paneLayout struct {
	prompts, response paneRect
	titleRow          int
	divider           int
}

func (application *app) bodyLayout() paneLayout {
	height := max(1, application.rows-4)
	layout := paneLayout{prompts: paneRect{1, 2, application.cols, height}}
	if application.preview == nil || !application.preview.visible {
		percent := 58
		if application.preview != nil && application.preview.width > 0 {
			percent = application.preview.width
		}
		left := max(3, min(application.cols-4, application.cols*(100-percent)/100))
		layout.prompts.width = left
		layout.divider = left + 1
		layout.response = paneRect{left + 2, 2, application.cols - left - 1, height}
	}
	if height >= 3 {
		layout.titleRow = 2
		layout.prompts.y++
		layout.prompts.height--
		if layout.response.width > 0 {
			layout.response.y++
			layout.response.height--
		}
	}
	return layout
}

func (application *app) ensurePromptVisible() {
	rect := application.bodyLayout().prompts
	indexes := application.promptIndexes()
	for number, index := range indexes {
		if application.messages[index].ID == application.selectedPrompt {
			needed := len(indexes) - number
			if needed > application.promptLimit {
				application.promptLimit = ((needed + 9) / 10) * 10
			}
			break
		}
	}
	rows := application.sidebarRows(rect.width)
	first, last := -1, -1
	for index, row := range rows {
		if row.promptID == application.selectedPrompt {
			if first < 0 {
				first = index
			}
			last = index
		}
	}
	if first < 0 {
		return
	}
	start := application.scroll
	end := start + rect.height
	if first < start || last-first+1 > rect.height {
		application.scroll = first
	} else if last >= end {
		application.scroll = max(0, last-rect.height+1)
	}
	application.sidebarFocus = ""
}

func (application *app) scrollResponse(direction int) {
	if application.preview != nil && application.preview.visible {
		if err := application.preview.command(fmt.Sprintf("region secondary view %d", direction*500000)); err != nil {
			application.status = err.Error()
		}
	} else {
		application.responseScroll = max(0, application.responseScroll+direction*max(1, application.bodyLayout().response.height/2))
	}
	application.dirty = true
}

func (application *app) handleMouse(mouse mouseEvent) {
	if mouse.release || mouse.button&32 != 0 || mouse.button&128 != 0 {
		return
	}
	// The hit map describes the last painted frame, including wrapping and
	// scroll offset. Resize invalidates it before any new clicks are accepted.
	if application.paintedRows != application.rows || application.paintedCols != application.cols {
		return
	}
	if mouse.x < 1 || mouse.x > application.cols || mouse.y < 2 || mouse.y > application.rows-3 {
		return
	}
	if application.picker != nil {
		if mouse.button&64 != 0 {
			key := "<Up>"
			if mouse.button&3 == 1 {
				key = "<Down>"
			}
			application.handlePickerInput(inputEvent{key: key})
		}
		return
	}
	if mouse.button&64 != 0 {
		direction := -1
		if mouse.button&3 == 1 {
			direction = 1
		} else if mouse.button&3 != 0 {
			return
		}
		if application.paintedLayout.prompts.contains(mouse.x, mouse.y) {
			application.scroll = max(0, application.scroll+direction*3)
			application.dirty = true
		} else if application.paintedLayout.response.contains(mouse.x, mouse.y) {
			application.scrollResponse(direction)
		}
		return
	}
	if mouse.button&3 == 0 && application.paintedLayout.prompts.contains(mouse.x, mouse.y) {
		hit := application.sidebarHits[mouse.y]
		switch hit.kind {
		case "session":
			application.attachSession(hit.id)
		case "prompt":
			application.selectPrompt(hit.id)
		case "more":
			application.morePrompts()
		case "reload":
			application.requestCatalog()
		}
	}
}
