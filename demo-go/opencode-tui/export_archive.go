package main

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

// Keep the documents schema compatible with demo-go/gols/archive.go. The extra
// session_messages table retains complete API records, including attachments and
// message types that are not displayed in the conversation preview.
const sessionArchiveSchema = `
CREATE TABLE archive_meta (key TEXT PRIMARY KEY, value TEXT NOT NULL) WITHOUT ROWID;
CREATE TABLE documents (
 id INTEGER PRIMARY KEY, path TEXT NOT NULL UNIQUE, dir TEXT NOT NULL,
 name TEXT NOT NULL, extension TEXT NOT NULL, title TEXT NOT NULL,
 course TEXT NOT NULL, kind TEXT NOT NULL, status TEXT NOT NULL,
 visibility TEXT NOT NULL, sequence INTEGER NOT NULL, content TEXT NOT NULL,
 search_text TEXT NOT NULL, size INTEGER NOT NULL, mod_time_ns INTEGER NOT NULL,
 sha256 TEXT NOT NULL, indexed_at TEXT NOT NULL
);
CREATE TABLE session_messages (
 sequence INTEGER PRIMARY KEY, id TEXT NOT NULL UNIQUE, type TEXT NOT NULL,
 json TEXT NOT NULL
);`

type archiveStats struct{ documents, messages int }

type sessionArchiveWriter struct {
	ctx     context.Context
	tx      *sql.Tx
	title   string
	created time.Time
	stats   archiveStats
	turn    int
	prompt  string
	content strings.Builder
}

func (writer *sessionArchiveWriter) document(name, title, kind, content string, sequence int) error {
	_, err := writer.tx.ExecContext(writer.ctx, `INSERT INTO documents
 (path, dir, name, extension, title, course, kind, status, visibility,
 sequence, content, search_text, size, mod_time_ns, sha256, indexed_at)
 VALUES (?, '.', ?, '.md', ?, ?, ?, 'current', 'user', ?, ?, ?, ?, ?, ?, ?)`,
		name, name, title, writer.title, kind, sequence, content, content, len(content),
		writer.created.UnixNano(), fmt.Sprintf("%x", sha256.Sum256([]byte(content))), writer.created.Format(time.RFC3339Nano))
	if err == nil {
		writer.stats.documents++
	}
	return err
}

func (writer *sessionArchiveWriter) flushTurn() error {
	if writer.content.Len() == 0 {
		return nil
	}
	title := writer.prompt
	if title == "" {
		title = "Session context"
	}
	err := writer.document(fmt.Sprintf("%06d.md", writer.turn), title, "conversation", writer.content.String(), writer.turn)
	writer.content.Reset()
	return err
}

func (writer *sessionArchiveWriter) message(raw json.RawMessage) error {
	var message apiMessage
	if err := json.Unmarshal(raw, &message); err != nil {
		return fmt.Errorf("decode export message: %w", err)
	}
	if message.ID == "" || message.Type == "" {
		return fmt.Errorf("server returned a message without an ID or type")
	}
	writer.stats.messages++
	if _, err := writer.tx.ExecContext(writer.ctx, `INSERT INTO session_messages (sequence, id, type, json) VALUES (?, ?, ?, ?)`, writer.stats.messages, message.ID, message.Type, string(raw)); err != nil {
		return err
	}
	if message.Type == "user" {
		if err := writer.flushTurn(); err != nil {
			return err
		}
		writer.turn++
		writer.prompt = truncateCells(strings.Join(strings.Fields(message.Text), " "), 120)
		if writer.prompt == "" {
			writer.prompt = fmt.Sprintf("Prompt %d (attachment)", writer.turn)
		}
	}
	if writer.content.Len() > 0 {
		writer.content.WriteString("\n\n---\n\n")
	}
	switch message.Type {
	case "user":
		writer.content.WriteString("# Prompt\n\n" + message.Text)
	case "assistant":
		writer.content.WriteString("## Assistant\n\n")
		for _, part := range message.Content {
			switch part.Type {
			case "reasoning":
				writer.content.WriteString("## Reasoning\n\n" + part.Text + "\n\n")
			case "text":
				writer.content.WriteString(part.Text + "\n\n")
			case "tool":
				writer.content.WriteString(toolSummary(part) + "\n\n")
				for _, output := range part.State.Content {
					if output.Type == "text" {
						writer.content.WriteString(codeBlock(output.Text, "") + "\n\n")
					}
				}
			}
		}
	default:
		writer.content.WriteString("## " + safeText(message.Type) + "\n\n")
		if message.Text != "" {
			writer.content.WriteString(message.Text)
		} else {
			writer.content.WriteString("Recorded in the archive's session_messages table.")
		}
	}
	if len(message.Files) > 0 {
		var names []string
		for _, file := range message.Files {
			names = append(names, file.Name+" ("+file.MIME+")")
		}
		writer.content.WriteString("\n\nAttachments (saved in session_messages):\n\n" + codeBlock(strings.Join(names, "\n"), ""))
	}
	if len(message.Snapshot.Files) > 0 {
		writer.content.WriteString("\n\nChanged files:\n\n" + codeBlock(strings.Join(message.Snapshot.Files, "\n"), ""))
	}
	return nil
}

func (client *apiClient) exportRequest(ctx context.Context, path string, output any) error {
	requestCtx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	return client.request(requestCtx, http.MethodGet, path, nil, output)
}

// Start with the newest recorded ID, then page oldest-first up to that boundary.
// This avoids the sidebar's history limit and never chases a running session's
// newly appended messages forever. Each network page is bounded independently.
func (client *apiClient) archiveMessages(ctx context.Context, id string, consume func(json.RawMessage) error) error {
	path := "/api/session/" + url.PathEscape(id) + "/message"
	var latest struct {
		Data []apiMessage `json:"data"`
	}
	if err := client.exportRequest(ctx, path+"?limit=1&order=desc", &latest); err != nil {
		return err
	}
	if len(latest.Data) == 0 {
		return nil
	}
	boundary := latest.Data[0].ID
	if boundary == "" {
		return fmt.Errorf("server returned a message without an ID")
	}
	cursor := ""
	seen := make(map[string]bool)
	for {
		query := url.Values{"limit": {"200"}}
		if cursor == "" {
			query.Set("order", "asc")
		} else {
			query.Set("cursor", cursor)
		}
		var page struct {
			Data   []json.RawMessage `json:"data"`
			Cursor struct {
				Next string `json:"next"`
			} `json:"cursor"`
		}
		if err := client.exportRequest(ctx, path+"?"+query.Encode(), &page); err != nil {
			return err
		}
		for _, raw := range page.Data {
			if err := ctx.Err(); err != nil {
				return err
			}
			if err := consume(raw); err != nil {
				return err
			}
			var message struct {
				ID string `json:"id"`
			}
			if err := json.Unmarshal(raw, &message); err != nil {
				return err
			}
			if message.ID == boundary {
				return nil
			}
		}
		cursor = page.Cursor.Next
		if cursor == "" {
			return fmt.Errorf("session history changed during export; retry the export")
		}
		if seen[cursor] {
			return fmt.Errorf("OpenCode repeated a message cursor")
		}
		seen[cursor] = true
	}
}

func (client *apiClient) exportGoLS(ctx context.Context, id, destination string) (stats archiveStats, err error) {
	if _, err := os.Lstat(destination); err == nil {
		return stats, fmt.Errorf("File already exists; choose another filepath: %s", destination)
	} else if !os.IsNotExist(err) {
		return stats, err
	}
	file, err := os.CreateTemp(filepath.Dir(destination), ".opencode-export-*.sqlite3")
	if err != nil {
		return stats, err
	}
	staging := file.Name()
	defer os.Remove(staging)
	if err := file.Close(); err != nil {
		return stats, err
	}
	dsn := url.URL{Scheme: "file", Path: staging}
	db, err := sql.Open("sqlite", dsn.String())
	if err != nil {
		return stats, err
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	// Pin the connection so Close waits for database/sql's asynchronous rollback
	// on cancellation. Tx.Rollback alone can return ErrTxDone while that rollback
	// still owns the SQLite journal, racing staging-file cleanup and completion.
	conn, err := db.Conn(ctx)
	if err != nil {
		return stats, err
	}
	defer conn.Close()
	if _, err := conn.ExecContext(ctx, "PRAGMA journal_mode=DELETE"); err != nil {
		return stats, err
	}
	tx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		return stats, err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, sessionArchiveSchema); err != nil {
		return stats, err
	}
	var response struct {
		Data json.RawMessage `json:"data"`
	}
	if err := client.exportRequest(ctx, "/api/session/"+url.PathEscape(id), &response); err != nil {
		return stats, err
	}
	var session sessionInfo
	if err := json.Unmarshal(response.Data, &session); err != nil {
		return stats, err
	}
	if session.ID != id || id == "" {
		return stats, fmt.Errorf("server returned an invalid export session")
	}
	title := strings.TrimSpace(safeText(session.Title))
	if title == "" {
		title = id
	}
	now := time.Now().UTC()
	metadata := map[string]string{
		"catalogue_title":       title,
		"catalogue_description": "OpenCode session " + id + " · " + session.Location.Directory,
		"generated_at":          now.Format(time.RFC3339Nano),
		"export_format":         "opencode-session-v1",
		"session_id":            id, "workdir": session.Location.Directory,
		"session_json": string(response.Data),
	}
	for key, value := range metadata {
		if _, err := tx.ExecContext(ctx, "INSERT INTO archive_meta VALUES (?, ?)", key, value); err != nil {
			return stats, err
		}
	}
	writer := &sessionArchiveWriter{ctx: ctx, tx: tx, title: title, created: now}
	overview := "# Session\n\n" + title + "\n\n" + codeBlock("Session: "+id+"\nWorkdir: "+session.Location.Directory+"\nExported: "+now.Format(time.RFC3339), "")
	if err := writer.document("session.md", title, "session", overview, -1); err != nil {
		return stats, err
	}
	if err := client.archiveMessages(ctx, id, writer.message); err != nil {
		return stats, err
	}
	if err := writer.flushTurn(); err != nil {
		return stats, err
	}
	if err := tx.Commit(); err != nil {
		return stats, err
	}
	if err := conn.Close(); err != nil {
		return stats, err
	}
	if err := db.Close(); err != nil {
		return stats, err
	}
	file, err = os.OpenFile(staging, os.O_RDWR, 0)
	if err != nil {
		return stats, err
	}
	err = file.Sync()
	closeErr := file.Close()
	if err != nil {
		return stats, err
	}
	if closeErr != nil {
		return stats, closeErr
	}
	if err := ctx.Err(); err != nil {
		return stats, err
	}
	// Publish a complete standalone database atomically, without replacing a
	// file created by somebody else while the history was being fetched.
	if err := os.Link(staging, destination); err != nil {
		return stats, err
	}
	return writer.stats, nil
}
