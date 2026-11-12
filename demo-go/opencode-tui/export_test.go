package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestExportDefaultAndEditedFilepaths(t *testing.T) {
	for _, test := range []struct{ title, name string }{
		{"First three words and more", "First three words.sqlite3"},
		{"  Two\twords  ", "Two words.sqlite3"},
		{"数学 会话 标题 更多", "数学 会话 标题.sqlite3"},
		{"", "session.sqlite3"},
		{"../nested a\\b c", "..-nested a-b c.sqlite3"},
	} {
		if got := defaultExportPath(sessionInfo{Title: test.title, Location: locationRef{Directory: "/work"}}); got != "/work/"+test.name {
			t.Errorf("default path for %q = %q", test.title, got)
		}
	}
	home := t.TempDir()
	t.Setenv("HOME", home)
	for _, test := range []struct{ input, want string }{
		{"edited name.sqlite3", "/work/edited name.sqlite3"},
		{"/local/file.sqlite3", "/local/file.sqlite3"},
		{"~/file.sqlite3", filepath.Join(home, "file.sqlite3")},
	} {
		got, err := exportFilepath(test.input, "/work")
		if err != nil || got != test.want {
			t.Errorf("edited path %q = %q, %v", test.input, got, err)
		}
	}
	if _, err := exportFilepath("  ", "/work"); err == nil {
		t.Fatal("empty filepath accepted")
	}
	session := sessionInfo{Title: "Relative workdir", Location: locationRef{Directory: "project"}}
	got, err := exportFilepath(defaultExportPath(session), session.Location.Directory)
	want, _ := filepath.Abs("project/Relative workdir.sqlite3")
	if err != nil || got != want {
		t.Fatalf("relative default workdir applied twice: %q, %v", got, err)
	}
}

func openExportedArchive(t *testing.T, file string) *sql.DB {
	t.Helper()
	dsn := url.URL{Scheme: "file", Path: file, RawQuery: "mode=ro&immutable=1"}
	db, err := sql.Open("sqlite", dsn.String())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	var integrity string
	if err := db.QueryRow("PRAGMA integrity_check").Scan(&integrity); err != nil || integrity != "ok" {
		t.Fatalf("invalid SQLite archive: %q, %v", integrity, err)
	}
	return db
}

func TestExportFullHistoryAndGoLSContent(t *testing.T) {
	directory := t.TempDir()
	session := sessionInfo{ID: "ses_archive", Title: "A full session export", Location: locationRef{Directory: directory}}
	var messages []json.RawMessage
	for turn := 0; turn < 106; turn++ {
		user, _ := json.Marshal(map[string]any{"id": fmt.Sprintf("msg_%03d_user", turn), "type": "user", "text": fmt.Sprintf("Prompt %d", turn)})
		assistant, _ := json.Marshal(map[string]any{
			"id": fmt.Sprintf("msg_%03d_assistant", turn), "type": "assistant", "extra": "preserve unknown fields",
			"content": []any{map[string]any{"type": "text", "text": fmt.Sprintf("Response %d\n\n$x^2$\n\n| A | B |\n| --- | --- |\n| 1 | 2 |", turn)}},
		})
		messages = append(messages, user, assistant)
	}
	messages[1] = json.RawMessage(`{"id":"msg_000_assistant","type":"assistant","extra":"preserve unknown fields","content":[{"type":"text","text":"Before tool"},{"type":"tool","id":"call_1","name":"shell","state":{"status":"completed","input":{"command":"printf output"},"content":[{"type":"text","text":"saved output\n` + "```literal fence```" + `"}]}},{"type":"text","text":"After tool"}],"snapshot":{"files":["src/main.go"]}}`)
	messages[2] = json.RawMessage(`{"id":"msg_001_user","type":"user","text":"Prompt 1","files":[{"name":"notes.txt","mime":"text/plain","data":"aGVsbG8="}]}`)
	var pages atomic.Int32
	application := pickerTestApp(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" || r.Header.Get("Authorization") != "Bearer test" {
			t.Errorf("export request lost method/auth: %s %s", r.Method, r.URL)
		}
		if r.URL.Path == "/api/session/ses_archive" {
			json.NewEncoder(w).Encode(map[string]any{"data": session})
			return
		}
		if r.URL.Path != "/api/session/ses_archive/message" {
			t.Errorf("unexpected endpoint: %s", r.URL)
			http.NotFound(w, r)
			return
		}
		q := r.URL.Query()
		if q.Get("limit") == "1" && q.Get("order") == "desc" {
			json.NewEncoder(w).Encode(map[string]any{"data": messages[len(messages)-1:]})
			return
		}
		pages.Add(1)
		switch q.Get("cursor") {
		case "":
			if q.Get("order") != "asc" || q.Get("limit") != "200" {
				t.Errorf("incorrect first-page query: %s", r.URL)
			}
			json.NewEncoder(w).Encode(map[string]any{"data": messages[:200], "cursor": map[string]string{"next": "older+page&2"}})
		case "older+page&2":
			if q.Has("order") {
				t.Error("cursor page overrode cursor ordering")
			}
			// A running session appends new messages after the initial boundary.
			data := append(append([]json.RawMessage(nil), messages[200:]...), json.RawMessage(`{"id":"msg_new","type":"user","text":"Too new"}`))
			json.NewEncoder(w).Encode(map[string]any{"data": data, "cursor": map[string]string{"next": "do-not-follow"}})
		default:
			t.Errorf("unexpected cursor: %q", q.Get("cursor"))
			http.NotFound(w, r)
		}
	}, session)
	file := filepath.Join(directory, "archive # ? with spaces.sqlite3")
	stats, err := application.api.exportGoLS(context.Background(), session.ID, file)
	if err != nil {
		t.Fatal(err)
	}
	if pages.Load() != 2 || stats.messages != len(messages) || stats.documents != 107 {
		t.Fatalf("incomplete export: %+v (%d pages)", stats, pages.Load())
	}
	db := openExportedArchive(t, file)
	var title string
	if err := db.QueryRow("SELECT value FROM archive_meta WHERE key='catalogue_title'").Scan(&title); err != nil || title != session.Title {
		t.Fatalf("archive title = %q, %v", title, err)
	}
	// Query exactly the metadata columns consumed by GoLS, then check the
	// Markdown payloads and checksums independently of the writer's stats.
	rows, err := db.Query(`SELECT id, path, name, extension, title, course, kind, status, visibility,
		sequence, size, mod_time_ns, sha256, content FROM documents ORDER BY sequence`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	count := 0
	for rows.Next() {
		var id, sequence, size, modTime int64
		var path, name, extension, title, course, kind, status, visibility, digest, content string
		if err := rows.Scan(&id, &path, &name, &extension, &title, &course, &kind, &status, &visibility, &sequence, &size, &modTime, &digest, &content); err != nil {
			t.Fatal(err)
		}
		count++
		if extension != ".md" || visibility != "user" || course != session.Title || size != int64(len(content)) || digest != fmt.Sprintf("%x", sha256.Sum256([]byte(content))) {
			t.Fatalf("invalid GoLS document metadata for %s", path)
		}
		if sequence == 1 {
			for _, want := range []string{"Prompt 0", "Before tool", "printf output", "saved output\n```literal fence```", "After tool", "src/main.go"} {
				if !strings.Contains(content, want) {
					t.Errorf("first turn lost %q", want)
				}
			}
			if strings.Index(content, "Before tool") > strings.Index(content, "saved output") || strings.Index(content, "After tool") < strings.Index(content, "saved output") {
				t.Error("tool timeline reordered")
			}
		}
		if sequence == 106 && (!strings.Contains(content, "Response 105") || !strings.Contains(content, "$x^2$") || !strings.Contains(content, "| 1 | 2 |")) {
			t.Error("last turn lost response, math, or table")
		}
	}
	if err := rows.Err(); err != nil || count != 107 {
		t.Fatalf("GoLS documents: count %d, %v", count, err)
	}
	var raw string
	if err := db.QueryRow("SELECT json FROM session_messages WHERE id='msg_001_user'").Scan(&raw); err != nil || !strings.Contains(raw, "aGVsbG8=") {
		t.Fatalf("attachment lost: %q, %v", raw, err)
	}
	if err := db.QueryRow("SELECT json FROM session_messages WHERE id='msg_000_assistant'").Scan(&raw); err != nil || !strings.Contains(raw, "preserve unknown fields") {
		t.Fatalf("unknown fields lost: %q, %v", raw, err)
	}
	var messageCount int
	if err := db.QueryRow("SELECT count(*) FROM session_messages").Scan(&messageCount); err != nil || messageCount != len(messages) {
		t.Fatalf("message count = %d, %v", messageCount, err)
	}
	entries, _ := os.ReadDir(directory)
	if len(entries) != 1 {
		t.Fatalf("export left temporary files: %v", entries)
	}
}

func TestExportFailuresPreserveDestinationAndCleanStaging(t *testing.T) {
	for _, mode := range []string{"exists", "racing-file", "cancel", "http", "repeated-cursor", "missing-boundary"} {
		t.Run(mode, func(t *testing.T) {
			directory := t.TempDir()
			file := filepath.Join(directory, "archive.sqlite3")
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			var calls atomic.Int32
			application := pickerTestApp(t, func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				if r.URL.Path == "/api/session/ses_export" {
					if mode == "racing-file" {
						if err := os.WriteFile(file, []byte("keep me"), 0600); err != nil {
							t.Error(err)
						}
					}
					io.WriteString(w, `{"data":{"id":"ses_export","title":"Export"}}`)
					return
				}
				if mode == "cancel" {
					cancel()
					<-r.Context().Done()
					return
				}
				if mode == "http" {
					http.Error(w, "export unavailable", 500)
					return
				}
				if mode == "repeated-cursor" || mode == "missing-boundary" {
					if r.URL.Query().Get("order") == "desc" {
						io.WriteString(w, `{"data":[{"id":"msg_boundary","type":"user","text":"Newest"}]}`)
					} else if mode == "repeated-cursor" {
						io.WriteString(w, `{"data":[],"cursor":{"next":"same"}}`)
					} else {
						io.WriteString(w, `{"data":[]}`)
					}
					return
				}
				io.WriteString(w, `{"data":[]}`)
			}, sessionInfo{ID: "ses_export"})
			if mode == "exists" {
				if err := os.WriteFile(file, []byte("keep me"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := application.api.exportGoLS(ctx, "ses_export", file); err == nil {
				t.Fatal("export unexpectedly succeeded")
			}
			if mode == "exists" && calls.Load() != 0 {
				t.Fatal("existing destination was not rejected before fetching")
			}
			entries, _ := os.ReadDir(directory)
			if mode == "exists" || mode == "racing-file" {
				data, err := os.ReadFile(file)
				if err != nil || string(data) != "keep me" || len(entries) != 1 {
					t.Fatalf("destination changed or staging leaked: %q, %v, %v", data, err, entries)
				}
			} else if len(entries) != 0 {
				t.Fatalf("failed export left files: %v", entries)
			}
		})
	}
}

func clickExport(t *testing.T, application *app, id string) {
	t.Helper()
	application.rows, application.cols = 40, 120
	application.sidebarFocus, application.scroll = "", 0
	if err := application.draw(); err != nil {
		t.Fatal(err)
	}
	for y, target := range application.exportHits {
		if target == id {
			application.handleInput(inputEvent{mouse: &mouseEvent{x: application.paintedLayout.prompts.width - 2, y: y}})
			return
		}
	}
	t.Fatalf("no export button for %s", id)
}

func TestExportDialogTargetsClickedSessionAndPreservesDraft(t *testing.T) {
	directory := t.TempDir()
	session := sessionInfo{ID: "ses_export", Title: "First three words extra", Location: locationRef{Directory: directory}}
	var calls atomic.Int32
	application := pickerTestApp(t, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		switch r.URL.Path {
		case "/api/session/ses_export":
			json.NewEncoder(w).Encode(map[string]any{"data": session})
		case "/api/session/ses_export/message":
			io.WriteString(w, `{"data":[]}`)
		default:
			t.Errorf("export retargeted: %s", r.URL)
			http.NotFound(w, r)
		}
	}, sessionInfo{ID: "ses_current", Title: "Current session"})
	application.sessions = []sessionInfo{application.session, session}
	application.input.Insert("unfinished draft")
	draft := application.input.Text()
	application.selectedPrompt, application.followPreview = "pinned", false
	for _, cancel := range []string{"<Esc>", "\x03"} {
		clickExport(t, application, session.ID)
		if dialog := application.exportDialog; dialog == nil || dialog.session.ID != session.ID || dialog.input.Text() != exportPrefix+filepath.Join(directory, "First three words.sqlite3") {
			t.Fatalf("wrong export target/default: %+v", dialog)
		}
		application.handleInput(inputEvent{key: cancel})
		if application.exportDialog != nil || !application.running || application.input.Text() != draft || calls.Load() != 0 {
			t.Fatal("canceled dialog changed composer, quit, or started export")
		}
	}
	clickExport(t, application, session.ID)
	var output bytes.Buffer
	application.out = &output
	if err := application.draw(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), "Export session to GoLS") || len(application.exportHits) != 0 {
		t.Fatal("export modal not rendered or left active hit targets")
	}
	application.permissions = []permissionRequest{{ID: "pending", SessionID: "ses_current"}}
	application.handleInput(inputEvent{mouse: &mouseEvent{x: 2, y: 5}})
	application.deleteSession("ses_current")
	application.exportSession("ses_current", "")
	if application.deleteConfirmation != nil || application.exportDialog.session.ID != session.ID {
		t.Fatal("background UI retargeted export")
	}
	// An existing path reports an editable error; a retry uses the same target.
	file := defaultExportPath(session)
	if err := os.WriteFile(file, []byte("keep"), 0600); err != nil {
		t.Fatal(err)
	}
	application.handleInput(inputEvent{key: "<Enter>"})
	awaitOperation(t, application, "export")
	if application.exportDialog == nil || application.exportDialog.err == "" || calls.Load() != 0 {
		t.Fatal("write error did not keep the filepath editable")
	}
	application.handleInput(inputEvent{key: "\x15"}) // Ctrl-U clears only the filepath.
	application.handleInput(inputEvent{paste: "chosen.sqlite3"})
	application.handleInput(inputEvent{key: "<Enter>"})
	application.handleInput(inputEvent{key: "y"}) // Cannot answer a background permission.
	awaitOperation(t, application, "export")
	if application.exportDialog != nil || !strings.Contains(application.status, "Exported 0 messages") || application.input.Text() != draft || application.selectedPrompt != "pinned" || application.followPreview || application.permissionBusy {
		t.Fatalf("export altered UI state: %s", application.status)
	}
	db := openExportedArchive(t, filepath.Join(directory, "chosen.sqlite3"))
	var count int
	if err := db.QueryRow("SELECT count(*) FROM documents").Scan(&count); err != nil || count != 1 {
		t.Fatalf("empty session overview missing: %d, %v", count, err)
	}
}

func TestExportCommandCancellationAndStaleCompletion(t *testing.T) {
	started := make(chan struct{})
	application := pickerTestApp(t, func(w http.ResponseWriter, r *http.Request) {
		close(started)
		<-r.Context().Done()
	}, sessionInfo{ID: "ses_export", Location: locationRef{Directory: t.TempDir()}})
	enterCommand(t, application, "/export chosen.sqlite3")
	if application.exportDialog == nil || application.exportDialog.input.Text() != exportPrefix+"chosen.sqlite3" {
		t.Fatal("/export did not prompt with the supplied filepath")
	}
	application.handleInput(inputEvent{key: "<Enter>"})
	select {
	case <-started:
	case <-time.After(3 * time.Second):
		t.Fatal("export request did not start")
	}
	application.handleInput(inputEvent{key: "<Esc>"})
	enterCommand(t, application, "/export newer.sqlite3")
	awaitOperation(t, application, "export")
	if dialog := application.exportDialog; dialog == nil || dialog.err != "" || dialog.input.Text() != exportPrefix+"newer.sqlite3" || dialog.submitting {
		t.Fatalf("stale completion changed new export dialog: %+v", dialog)
	}
	entries, _ := os.ReadDir(application.session.Location.Directory)
	if len(entries) != 0 {
		t.Fatalf("cancel left partial export: %v", entries)
	}
	application.sessions = append(application.sessions, sessionInfo{ID: "ses_child", ParentID: "ses_parent"})
	application.closeExportDialog()
	application.exportSession("ses_child", "")
	application.removeSession("ses_parent")
	if application.exportDialog != nil {
		t.Fatal("external deletion left stale export dialog")
	}
}
