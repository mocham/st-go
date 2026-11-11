package main

import (
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
)

func clickMessageFile(t *testing.T, application *app, id string) {
	t.Helper()
	if err := application.draw(); err != nil {
		t.Fatal(err)
	}
	for row, hit := range application.sidebarHits {
		if hit.kind == "file" && hit.id == id {
			application.handleInput(inputEvent{mouse: &mouseEvent{x: 2, y: row}})
			return
		}
	}
	t.Fatalf("file not clickable: %s", id)
}

func TestTabFileClicksReadRemoteFilesAndPreserveComposerAndPrompt(t *testing.T) {
	application := pickerTestApp(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer test" {
			t.Error("file request lost server authentication")
		}
		if r.Method != "GET" || r.URL.Path != "/api/fs/read/a # file.md" || r.URL.Query().Get("location[directory]") != "/remote/project/docs" {
			t.Errorf("incorrect remote file request: %s %s", r.Method, r.URL)
		}
		w.Header().Set("Content-Type", "text/markdown")
		io.WriteString(w, "# Remote file\n\nA formula $x^2$.")
	}, sessionInfo{ID: "ses_test", Location: locationRef{Directory: "/remote/project"}})
	application.replaceMessages([]apiMessage{{ID: "msg_p", Type: "user", Text: "question"}, answer("msg_a", "See [document](<docs/a%20%23%20file.md>)")})
	application.rows, application.cols = 20, 64
	application.input.Insert("unfinished question")
	application.draw()
	scroll := application.scroll
	application.handleInput(inputEvent{key: "<Tab>"})
	if !application.filesMode || !strings.Contains(application.input.Text(), "unfinished question") || application.selectedPrompt != "msg_p" {
		t.Fatal("Tab changed prompt/composer")
	}
	files := application.messageFiles()
	if len(files) != 1 {
		t.Fatalf("linked file list: %+v", files)
	}
	clickMessageFile(t, application, files[0].ID)
	oldSequence := application.fileSequence
	awaitOperation(t, application, "file")
	if !strings.Contains(application.selectedResponse(), "# Remote file") {
		t.Fatal("file preview missing")
	}
	application.handleInput(inputEvent{key: "<Tab>"})
	if application.filesMode || application.scroll != scroll || !strings.Contains(application.selectedResponse(), "See [document]") {
		t.Fatal("Tab did not restore prompt list/response/scroll")
	}
	application.applyOperation(operationResult{kind: "file", sessionID: "ses_test", epoch: application.sessionEpoch, file: &fileResult{sequence: oldSequence, text: "stale"}})
	if application.selectedResponse() == "stale" {
		t.Fatal("late file result replaced response")
	}
	application.handleInput(inputEvent{key: "<Tab>"})
	clickMessageFile(t, application, files[0].ID)
	awaitOperation(t, application, "file")
	application.handleInput(inputEvent{key: "<F3>"})
	awaitOperation(t, application, "file")
	if !strings.Contains(application.selectedResponse(), "Remote file") {
		t.Fatal("file reload failed")
	}
}

func TestInlineAttachmentAndFileErrorsAreIsolatedFromSessionChanges(t *testing.T) {
	application := pickerTestApp(t, func(w http.ResponseWriter, r *http.Request) { http.Error(w, "file removed", 404) }, sessionInfo{ID: "ses_test"})
	var attachment messageAttachment
	attachment.Name, attachment.MIME, attachment.Data = "note.txt", "text/plain", base64.StdEncoding.EncodeToString([]byte("message attachment"))
	attachment.Source.Type = "inline"
	application.replaceMessages([]apiMessage{{ID: "msg_p", Type: "user", Files: []messageAttachment{attachment}}, answer("msg_a", "[gone](deleted.go)")})
	application.toggleFiles()
	files := application.messageFiles()
	if len(files) != 2 {
		t.Fatal(files)
	}
	application.selectFile(files[0].ID)
	awaitOperation(t, application, "file")
	if !strings.Contains(application.selectedResponse(), "message attachment") {
		t.Fatal("inline attachment was not decoded")
	}
	application.selectFile(files[1].ID)
	awaitOperation(t, application, "file")
	if !strings.Contains(application.selectedResponse(), "404") || !strings.Contains(application.selectedResponse(), "file removed") {
		t.Fatal("file error not shown")
	}
	sequence := application.fileSequence
	application.resetFiles()
	application.session.ID = "ses_other"
	application.sessionEpoch++
	application.applyOperation(operationResult{kind: "file", sessionID: "ses_test", epoch: application.sessionEpoch - 1, file: &fileResult{sequence: sequence, text: "stale"}})
	if application.fileSelection != nil {
		t.Fatal("old-session file result leaked")
	}
}

func TestFilePreviewCodeFencesAndMedia(t *testing.T) {
	text, media := filePreview("code.go", "text/plain", []byte("\tpackage main\n// ``` $not_math$"))
	if media != nil || !strings.Contains(text, "````") || !strings.Contains(text, "\tpackage") {
		t.Fatalf("source not preserved: %q", text)
	}
	text, media = filePreview("file.pdf", "application/octet-stream", []byte("%PDF-1.7\n"))
	if media == nil || media.extension != ".pdf" || !strings.Contains(text, "file.pdf") {
		t.Fatal("PDF not detected")
	}
	application, _ := promptTestApp(t, []apiMessage{{ID: "msg_p", Type: "user"}}, false)
	toolEvent(t, application, "session.tool.input.started", map[string]any{"id": "image", "name": "read"})
	toolEvent(t, application, "session.tool.success", map[string]any{"id": "image", "content": []toolContent{{Type: "file", URI: "data:text/plain;base64,aGVsbG8=", Name: "result.txt", MIME: "text/plain"}}})
	application.toggleFiles()
	application.selectFile(application.messageFiles()[0].ID)
	awaitOperation(t, application, "file")
	if !strings.Contains(application.selectedResponse(), "hello") {
		t.Fatal("tool file data URI did not preview")
	}
}

func TestFileSelectionSurvivesHistoryRefreshAndRejectsStaleClicks(t *testing.T) {
	application, _ := promptTestApp(t, []apiMessage{{ID: "msg_p", Type: "user"}, answer("msg_a", "[one](one.md) [two](two.md)")}, false)
	application.toggleFiles()
	files := application.messageFiles()
	application.draw()
	application.cols++
	for row := range application.sidebarHits {
		application.handleInput(inputEvent{mouse: &mouseEvent{x: 2, y: row}})
	}
	if application.fileSelection != nil {
		t.Fatal("stale resized click loaded a file")
	}
	application.fileSelection = &fileSelection{ref: files[0], text: "pinned file"}
	application.replaceMessages([]apiMessage{{ID: "msg_p", Type: "user"}, answer("msg_a", "[one](one.md) [two](two.md)"), {ID: "msg_new", Type: "user"}, answer("msg_b", "latest")})
	if application.selectedPrompt != "msg_p" || application.selectedResponse() != "pinned file" {
		t.Fatal("resync stole pinned file")
	}
	application.applyEvent(apiEvent{Type: "session.inbox.delivered", Data: json.RawMessage(`{"sessionID":"ses_test","inboxID":"msg_new"}`)})
	if application.selectedResponse() != "pinned file" {
		t.Fatal("new delivery replaced file")
	}
}
