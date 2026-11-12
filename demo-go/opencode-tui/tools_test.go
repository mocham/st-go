package main

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"
)

func toolEvent(t *testing.T, application *app, kind string, fields map[string]any) {
	t.Helper()
	fields["sessionID"], fields["assistantMessageID"] = "ses_test", "msg_a"
	data, err := json.Marshal(fields)
	if err != nil {
		t.Fatal(err)
	}
	application.applyEvent(apiEvent{Type: kind, Data: data})
}

func TestReasoningStreamsAndRestoresInOrder(t *testing.T) {
	application, _ := promptTestApp(t, []apiMessage{{ID: "msg_p", Type: "user", Text: "Write"}}, true)
	toolEvent(t, application, "session.reasoning.started", map[string]any{"ordinal": 0})
	toolEvent(t, application, "session.reasoning.delta", map[string]any{"ordinal": 0, "delta": "Exposed thought"})
	toolEvent(t, application, "session.reasoning.ended", map[string]any{"ordinal": 0, "text": "Exposed thought."})
	toolEvent(t, application, "session.text.delta", map[string]any{"ordinal": 1, "delta": "Final paragraph."})
	response := application.selectedResponse()
	if strings.Count(response, "Exposed thought.") != 1 || strings.Index(response, "Exposed thought.") > strings.Index(response, "Final paragraph.") {
		t.Fatalf("incorrect live reasoning: %s", response)
	}
	saved := convertMessages([]apiMessage{{ID: "msg_p", Type: "user", Text: "Write"}, {ID: "msg_a", Type: "assistant", Content: []messageContent{{Type: "reasoning", Text: "Exposed thought."}, {Type: "text", Text: "Final paragraph."}}}})
	if saved[1].Rich != application.messages[1].Rich {
		t.Fatalf("history differs: %q / %q", saved[1].Rich, application.messages[1].Rich)
	}
}

func TestToolsStreamCommandsFilesAndSavedOutputInMessageOrder(t *testing.T) {
	application, _ := promptTestApp(t, []apiMessage{{ID: "msg_p", Type: "user", Text: "Do work"}}, true)
	toolEvent(t, application, "session.text.delta", map[string]any{"ordinal": 0, "delta": "Before"})
	toolEvent(t, application, "session.tool.input.started", map[string]any{"id": "call_shell", "name": "shell"})
	toolEvent(t, application, "session.tool.input.delta", map[string]any{"id": "call_shell", "delta": `{"command":"go test`})
	if !strings.Contains(application.selectedResponse(), "streaming") {
		t.Fatal("tool preparation not visible")
	}
	toolEvent(t, application, "session.tool.input.ended", map[string]any{"id": "call_shell", "text": `{"command":"go test ./..."}`})
	toolEvent(t, application, "session.tool.called", map[string]any{"id": "call_shell", "input": map[string]any{"command": "go test ./..."}})
	if !strings.Contains(application.preview.pending.text, "go test ./...") || !strings.Contains(application.preview.pending.text, "running") {
		t.Fatal("running command missing from rich preview")
	}
	toolEvent(t, application, "session.tool.success", map[string]any{"id": "call_shell", "content": []toolContent{{Type: "text", Text: "ok package 0.1s"}}, "metadata": map[string]any{"outputPath": "/saved/test-output.txt"}})
	toolEvent(t, application, "session.tool.input.started", map[string]any{"id": "call_patch", "name": "patch"})
	toolEvent(t, application, "session.tool.called", map[string]any{"id": "call_patch", "input": map[string]any{"patchText": "*** Begin Patch\n*** Update File: src/a.go\n@@\n-old\n+new\n*** Add File: notes.md\n+hi\n*** End Patch"}})
	toolEvent(t, application, "session.tool.failed", map[string]any{"id": "call_patch", "error": map[string]any{"type": "Error", "message": "patch mismatch"}})
	toolEvent(t, application, "session.text.delta", map[string]any{"ordinal": 2, "delta": "After"})
	response := application.selectedResponse()
	previous := -1
	for _, text := range []string{"Before", "go test ./...", "shell-call_shell.txt", "src/a.go", "notes.md", "patch mismatch", "After"} {
		index := strings.Index(response, text)
		if index <= previous {
			t.Fatalf("missing/out-of-order %q in %s", text, response)
		}
		previous = index
	}
	if strings.Contains(response, "ok package") {
		t.Fatal("tool output flooded the response instead of becoming a message file")
	}
	files := application.messageFiles()
	if len(files) != 4 {
		t.Fatalf("files = %+v", files)
	}
	application.toggleFiles()
	var output messageFile
	for _, file := range files {
		if file.Text != nil {
			output = file
		}
	}
	application.selectFile(output.ID)
	awaitOperation(t, application, "file")
	if !strings.Contains(application.selectedResponse(), "ok package 0.1s") {
		t.Fatal("saved message output did not preview")
	}
	// Background tool updates must not replace a selected output file.
	toolEvent(t, application, "session.tool.progress", map[string]any{"id": "call_patch", "metadata": map[string]any{"title": "later"}})
	if !strings.Contains(application.preview.pending.text, "ok package 0.1s") {
		t.Fatal("tool activity stole file preview")
	}
	application.toggleFiles()
	if !strings.Contains(application.selectedResponse(), "go test ./...") {
		t.Fatal("Tab did not restore response")
	}
	for _, event := range []string{"session.tool.input.started", "session.tool.input.delta", "session.tool.input.ended", "session.tool.called", "session.tool.progress", "session.tool.success", "session.tool.failed"} {
		if !slices.Contains(streamTypes, event) {
			t.Fatalf("not subscribed to %s", event)
		}
	}
}

func TestHistoryToolsRestoreTheSamePreviewAndFileList(t *testing.T) {
	var messages []apiMessage
	err := json.Unmarshal([]byte(`[
{"id":"msg_p","type":"user","text":"Inspect"},
{"id":"msg_a","type":"assistant","content":[
{"type":"text","text":"Before"},
{"type":"tool","id":"read1","name":"read","state":{"status":"completed","input":{"filePath":"docs/a.md"},"content":[{"type":"text","text":"document"}]}},
{"type":"text","text":"After [file](docs/a.md#L5)"},
{"type":"tool","id":"prep","name":"shell","state":{"status":"streaming","input":"{\"command\":"}}
],"snapshot":{"files":["docs/a.md","other.txt"]}}
]`), &messages)
	if err != nil {
		t.Fatal(err)
	}
	application, _ := promptTestApp(t, messages, false)
	response := application.selectedResponse()
	if strings.Index(response, "Before") > strings.Index(response, "docs/a.md") || strings.Index(response, "After") < strings.Index(response, "docs/a.md") {
		t.Fatal("history reordered tool/text blocks")
	}
	if files := application.messageFiles(); len(files) != 3 {
		t.Fatalf("history failed to deduplicate path references: %+v", files)
	}
	// A call queued before this newer snapshot must not revert completed to running.
	toolEvent(t, application, "session.tool.called", map[string]any{"id": "read1", "input": map[string]any{"filePath": "docs/a.md"}})
	if application.messages[1].Tools[0].State.Status != "completed" {
		t.Fatal("late call regressed completed state")
	}
}
