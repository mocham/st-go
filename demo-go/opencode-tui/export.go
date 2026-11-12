package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"unicode"
	"unicode/utf8"

	"st-go/tui/readline"
)

const exportPrefix = "File: "

type sessionExportDialog struct {
	session    sessionInfo
	input      *readline.Line
	sequence   uint64
	cancel     context.CancelFunc
	submitting bool
	err        string
}

type exportResult struct {
	sequence  uint64
	path      string
	documents int
	messages  int
}

func defaultExportPath(session sessionInfo) string {
	words := strings.Fields(session.Title)
	name := strings.Join(words[:min(3, len(words))], " ")
	name = strings.Map(func(r rune) rune {
		if r == '/' || r == '\\' || unicode.IsControl(r) {
			return '-'
		}
		return r
	}, name)
	// Leave room for the extension within ordinary filesystem component limits.
	for len(name) > 200 {
		_, n := utf8.DecodeLastRuneInString(name)
		name = name[:len(name)-n]
	}
	name = strings.TrimSpace(name)
	if name == "" || name == "." || name == ".." {
		name = "session"
	}
	file := filepath.Join(session.Location.Directory, name+".sqlite3")
	if absolute, err := filepath.Abs(file); err == nil {
		file = absolute
	}
	return file
}

func exportFilepath(value, directory string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", fmt.Errorf("Enter a filepath for the GoLS archive")
	}
	if strings.HasPrefix(value, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		value = filepath.Join(home, value[2:])
	} else if !filepath.IsAbs(value) {
		value = filepath.Join(directory, value)
	}
	return filepath.Abs(value)
}

func (application *app) sessionByID(id string) (sessionInfo, bool) {
	if id == "" || application.deleted[id] {
		return sessionInfo{}, false
	}
	if id == application.session.ID {
		return application.session, true
	}
	for _, session := range application.sessions {
		if session.ID == id {
			return session, true
		}
	}
	return sessionInfo{}, false
}

func (application *app) exportSession(id, initialPath string) {
	if application.exportDialog != nil || application.deleteConfirmation != nil || application.deleting[id] {
		return
	}
	session, ok := application.sessionByID(id)
	if !ok {
		application.status, application.dirty = "Select a session to export", true
		return
	}
	if initialPath == "" {
		initialPath = defaultExportPath(session)
	}
	line := readline.New(exportPrefix, len(exportPrefix), 4096)
	if err := line.Insert(initialPath); err != nil {
		line.Close()
		application.status, application.dirty = err.Error(), true
		return
	}
	application.exportSequence++
	application.exportDialog = &sessionExportDialog{session: session, input: line, sequence: application.exportSequence}
	application.sidebarHits, application.deleteHits, application.exportHits = nil, nil, nil
	application.dirty = true
}

func (application *app) closeExportDialog() {
	if dialog := application.exportDialog; dialog != nil {
		if dialog.cancel != nil {
			dialog.cancel()
		}
		dialog.input.Close()
		application.exportDialog = nil
		application.dirty = true
	}
}

func (application *app) handleExportInput(event inputEvent) {
	dialog := application.exportDialog
	if event.mouse != nil {
		return
	}
	application.dirty = true
	switch event.key {
	case "\x11":
		application.running = false
		application.closeExportDialog()
		return
	case "<Esc>", "\x03":
		application.closeExportDialog()
		application.status = "Export canceled"
		return
	}
	if dialog.submitting {
		return
	}
	if event.paste != "" {
		dialog.err = ""
		if err := dialog.input.Insert(event.paste); err != nil {
			dialog.err = err.Error()
		}
		return
	}
	result := dialog.input.Handle(event.key)
	if result.Err != nil {
		dialog.err = result.Err.Error()
	} else if result.Changed {
		dialog.err = ""
	}
	if !result.Submit {
		return
	}
	file, err := exportFilepath(string([]rune(dialog.input.Text())[dialog.input.Min:]), dialog.session.Location.Directory)
	if err != nil {
		dialog.err = err.Error()
		return
	}
	ctx, cancel := context.WithCancel(application.ctx)
	dialog.cancel, dialog.submitting, dialog.err = cancel, true, ""
	id, sequence := dialog.session.ID, dialog.sequence
	go func() {
		defer cancel()
		stats, err := application.api.exportGoLS(ctx, id, file)
		result := operationResult{kind: "export", err: err, export: &exportResult{sequence: sequence, path: file, documents: stats.documents, messages: stats.messages}}
		select {
		case application.operations <- result:
		case <-application.ctx.Done():
		}
	}()
}

func (application *app) applyExport(result operationResult) {
	dialog := application.exportDialog
	if dialog == nil || dialog.sequence != result.export.sequence {
		return
	}
	dialog.submitting = false
	if result.err != nil {
		dialog.err = result.err.Error()
		application.dirty = true
		return
	}
	application.closeExportDialog()
	application.status = fmt.Sprintf("Exported %d messages to GoLS: %s", result.export.messages, result.export.path)
}

func (dialog *sessionExportDialog) rows(width int) []displayRow {
	var rows []displayRow
	for _, part := range []textSpan{
		{"Export session to GoLS", assistantStyle},
		{dialog.session.Title, sessionNameStyle},
		{dialog.session.ID, dimStyle},
		{"Workdir: " + dialog.session.Location.Directory, workdirStyle},
		{"Save the full recorded session as a local SQLite archive.", bodyStyle},
		{"Edit the filepath below, then press Enter. Esc cancels.", bodyStyle},
	} {
		for _, line := range wrapText(part.text, width) {
			rows = append(rows, displayRow{text: line, style: part.style})
		}
	}
	return rows
}
