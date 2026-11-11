package main

import (
	"context"
	"fmt"
	"strings"
	"time"

	"st-go/tui/readline"
)

const (
	workdirStyle            = "\x1b[0;38;2;239;182;204m"
	sessionNameStyle        = "\x1b[0;38;2;169;204;238m"
	activeSessionStyle      = "\x1b[1;38;5;81m"
	waitingSessionStyle     = "\x1b[1;38;5;220m"
	activeSessionBackground = "\x1b[48;5;236m"
)

type sidebarHit struct{ kind, id string }
type textSpan struct{ text, style string }
type catalogResult struct {
	sequence    uint64
	first, done bool
	sessions    []sessionInfo
}

func (application *app) requestCatalog() {
	if application.catalogLoading || application.api == nil {
		return
	}
	application.catalogLoading, application.catalogError = true, ""
	application.catalogSequence++
	sequence := application.catalogSequence
	application.dirty = true
	go func() {
		cursor, first := "", true
		seen := make(map[string]bool)
		for {
			ctx, cancel := context.WithTimeout(application.ctx, 30*time.Second)
			page, err := application.api.sessionCatalog(ctx, cursor)
			cancel()
			if err == nil && page.Cursor.Next != "" && seen[page.Cursor.Next] {
				err = fmt.Errorf("OpenCode repeated a session cursor")
			}
			done := err != nil || page.Cursor.Next == ""
			result := operationResult{err: err, catalog: &catalogResult{sequence: sequence, first: first, done: done, sessions: page.Data}}
			select {
			case application.operations <- result:
			case <-application.ctx.Done():
				return
			}
			if done {
				return
			}
			cursor, first = page.Cursor.Next, false
			seen[cursor] = true
		}
	}()
}

func (application *app) applyCatalog(result operationResult) {
	page := result.catalog
	if page.sequence != application.catalogSequence {
		return
	}
	application.catalogLoading = !page.done
	if result.err != nil {
		application.catalogError = result.err.Error()
	} else {
		if page.first {
			application.sessions = nil
		}
		indexes := make(map[string]int, len(application.sessions))
		for index, session := range application.sessions {
			indexes[session.ID] = index
		}
		for _, session := range page.sessions {
			if application.deleted[session.ID] {
				continue
			}
			if index, exists := indexes[session.ID]; exists {
				application.sessions[index] = session
			} else {
				indexes[session.ID] = len(application.sessions)
				application.sessions = append(application.sessions, session)
			}
		}
	}
	application.dirty = true
}

func (application *app) cancelAttach() {
	if application.attachCancel != nil {
		application.attachCancel()
		application.attachCancel = nil
	}
	application.attachSequence++
	application.attachTarget = ""
	application.dirty = true
}

func (application *app) attachSession(id string) {
	if application.deleted[id] {
		return
	}
	application.cancelAttach()
	if id == application.session.ID {
		application.sidebarFocus = id
		application.queuePreview()
		return
	}
	application.attachTarget = id
	application.sidebarFocus = id
	application.status = "Opening session…"
	application.queuePreview()
	sequence, currentID, epoch := application.attachSequence, application.session.ID, application.sessionEpoch
	activityRevision := application.activityRevision
	ctx, cancel := context.WithTimeout(application.ctx, 30*time.Second)
	application.attachCancel = cancel
	go func() {
		defer cancel()
		snapshot, err := application.api.snapshot(ctx, id, 10)
		result := operationResult{kind: "attach", attachToken: sequence, sessionID: currentID, epoch: epoch, sessionSnapshot: snapshot, activityRevision: activityRevision, err: err}
		select {
		case application.operations <- result:
		case <-application.ctx.Done():
		}
	}()
}

func (application *app) applyAttach(result operationResult) {
	if result.attachToken != application.attachSequence || application.attachTarget == "" {
		return
	}
	if result.err != nil {
		application.cancelAttach()
		application.status = result.err.Error()
		application.queuePreview()
		return
	}
	application.installSession(result.sessionSnapshot)
}

func (application *app) installSession(snapshot sessionSnapshot) {
	application.cancelAttach()
	application.resetFiles()
	application.sessionEpoch++
	application.revision++
	application.session = *snapshot.session
	application.busy = snapshot.busy
	application.sessionActivity[application.session.ID] = snapshot.busy
	application.syncing, application.permissionBusy = false, false
	application.permissions = snapshot.permissions
	application.followPreview, application.selectedPrompt, application.activePrompt = true, "", ""
	application.messages = nil
	application.sidebarHits = nil
	application.deleteHits = nil
	application.promptLimit = 10
	application.historyCursor = snapshot.messageCursor
	application.details, application.usage = sessionDetails{}, nil
	application.detailsLoading = false
	application.detailsRefresh = time.Now()
	application.sidebarFocus = application.session.ID
	application.responseScroll = 0
	application.previewGeneration = ""
	application.status = "Attached to " + application.session.ID
	if err := application.preview.clear(); err != nil {
		application.status = err.Error()
	}
	application.replaceMessages(snapshot.messages)
	application.queuePreview()
	// Catch events received while the target session snapshot was loading.
	application.requestSync()
}

func (application *app) morePrompts() {
	if application.attachTarget != "" {
		return
	}
	count := len(application.promptIndexes())
	if application.promptLimit >= count && application.historyCursor == "" {
		return
	}
	if application.promptLimit <= count {
		application.promptLimit += 10
	}
	if application.promptLimit > count && application.historyCursor != "" {
		application.requestSync()
	}
	application.dirty = true
}

// Wrap styled headers without inserting control sequences into user text.
// Every wrapped row remains one hit target for its session.
func sessionHeader(session sessionInfo, active bool, width int) []displayRow {
	marker, emphasis := "▸ ", ""
	if active {
		marker, emphasis = "▾ ", "\x1b[1m"
	}
	title := session.Title
	if strings.TrimSpace(title) == "" {
		title = shortID(session.ID)
	}
	directory := session.Location.Directory
	if directory == "" {
		directory = "(workdir unavailable)"
	}
	parts := []textSpan{{marker, dimStyle}, {title, sessionNameStyle + emphasis}, {" · ", dimStyle}, {directory, workdirStyle + emphasis}}
	rows := []displayRow{{sessionID: session.ID}}
	used := 0
	for _, part := range parts {
		for _, r := range strings.ReplaceAll(safeText(part.text), "\n", " ") {
			cells := readline.CellWidth(r, used)
			if used > 0 && used+cells > width {
				rows = append(rows, displayRow{sessionID: session.ID})
				used = 0
				cells = readline.CellWidth(r, 0)
			}
			row := &rows[len(rows)-1]
			if cells > width {
				r, cells = '?', 1
			}
			text := string(r)
			row.text += text
			if n := len(row.spans); n > 0 && row.spans[n-1].style == part.style {
				row.spans[n-1].text += text
			} else {
				row.spans = append(row.spans, textSpan{text, part.style})
			}
			used += cells
		}
	}
	return rows
}

// Keep borders separate from the styled content and from row hit targets.
// Prompts and actions inside a box retain their original click behavior.
func sessionBoxRow(row displayRow, width int, border, background string, heavy bool) displayRow {
	edge := "│"
	if heavy {
		edge = "┃"
	}
	inner := max(1, width-2)
	if row.style == selectedPromptStyle {
		background = "" // Keep the selected prompt's own contrasting background.
	}
	spans := []textSpan{{edge, border}}
	if len(row.spans) == 0 {
		spans = append(spans, textSpan{padCells(row.text, inner), row.style + background})
	} else {
		for _, span := range row.spans {
			spans = append(spans, textSpan{span.text, span.style + background})
		}
		spans = append(spans, textSpan{strings.Repeat(" ", max(0, inner-textWidth(row.text))), resetStyle + background})
	}
	row.text = edge + padCells(row.text, inner) + edge
	row.spans = append(spans, textSpan{edge, border})
	row.style = border
	return row
}

func sessionBoxBorder(width int, label, style string, heavy, bottom bool) displayRow {
	left, line, right := "┌", "─", "┐"
	if bottom {
		left, right = "└", "┘"
	}
	if heavy {
		left, line, right = "┏", "━", "┓"
		if bottom {
			left, right = "┗", "┛"
		}
	}
	inside := ""
	if label != "" {
		inside = truncateCells(" "+label+" ", max(0, width-2))
	}
	return displayRow{text: left + inside + strings.Repeat(line, max(0, width-2-textWidth(inside))) + right, style: style}
}

func (application *app) sessionBoxState(id string, active bool) (label, style string, highlighted bool) {
	switch {
	case application.deleting[id]:
		return "Deleting…", waitingSessionStyle, true
	case id == application.attachTarget:
		return "Connecting…", activeSessionStyle, true
	case id == application.session.ID && (application.busy || application.pendingPrompts()), id != application.session.ID && application.sessionActivity[id]:
		return "Awaiting response", waitingSessionStyle, true
	case active:
		if !application.streamConnected {
			return "Active · Reconnecting…", activeSessionStyle, true
		}
		return "Active · Ready", activeSessionStyle, true
	default:
		return "Inactive", dimStyle, false
	}
}

func (application *app) sidebarRows(width int) []displayRow {
	sessions := append([]sessionInfo(nil), application.sessions...)
	found := false
	for index := range sessions {
		if sessions[index].ID == application.session.ID {
			sessions[index] = application.session
			found = true
		}
	}
	if !found && application.session.ID != "" {
		sessions = append([]sessionInfo{application.session}, sessions...)
	}
	expanded := application.session.ID
	if application.attachTarget != "" {
		expanded = application.attachTarget
	}
	rows := []displayRow{{text: "+ New session", style: sessionNameStyle, action: "new"}, {}}
	for _, session := range sessions {
		active := session.ID == expanded
		label, border, highlighted := application.sessionBoxState(session.ID, active)
		background := ""
		if highlighted {
			background = activeSessionBackground
		}
		top := sessionBoxBorder(width, label, border, highlighted, false)
		top.sessionID = session.ID
		rows = append(rows, top)
		inner := max(1, width-2)
		headerWidth := inner
		if inner >= 12 {
			headerWidth -= 9
		}
		header := sessionHeader(session, active, max(1, headerWidth))
		if inner >= 12 {
			header[0].deleteID = session.ID
		}
		content := header
		if active {
			if application.attachTarget != "" {
				for _, line := range wrapText("  Loading prompts…", inner) {
					content = append(content, displayRow{text: line, style: dimStyle})
				}
			} else {
				content = append(content, application.promptRows(inner)...)
				count := len(application.promptIndexes())
				if count > application.promptLimit || application.historyCursor != "" {
					label := "  More (+10 older prompts)"
					if application.syncing && count < application.promptLimit {
						label = "  Loading older prompts…"
					}
					for _, line := range wrapText(label, inner) {
						content = append(content, displayRow{text: line, style: sessionNameStyle, action: "more"})
					}
				}
			}
		}
		for _, row := range content {
			rows = append(rows, sessionBoxRow(row, width, border, background, highlighted))
		}
		bottom := sessionBoxBorder(width, "", border, highlighted, true)
		bottom.sessionID = session.ID
		rows = append(rows, bottom)
		rows = append(rows, displayRow{})
	}
	if application.catalogError != "" {
		rows = append(rows, displayRow{text: "Retry session list: " + application.catalogError, style: warningStyle, action: "reload"})
	} else if application.catalogLoading {
		rows = append(rows, displayRow{text: "Loading sessions…", style: dimStyle})
	}
	return rows
}
