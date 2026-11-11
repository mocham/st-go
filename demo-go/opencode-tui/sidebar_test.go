package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"
)

func sidebarClick(t *testing.T, application *app, kind, id string) {
	t.Helper()
	application.sidebarFocus = ""
	rows := application.sidebarRows(application.bodyLayout().prompts.width)
	for index, row := range rows {
		match := kind == "session" && row.sessionID == id || kind == "prompt" && row.promptID == id || kind == row.action
		if !match {
			continue
		}
		application.scroll = index
		if err := application.draw(); err != nil {
			t.Fatal(err)
		}
		for y, hit := range application.sidebarHits {
			if hit.kind == kind && (id == "" || hit.id == id) {
				application.handleInput(inputEvent{mouse: &mouseEvent{x: 2, y: y}})
				return
			}
		}
	}
	t.Fatalf("sidebar target missing: %s %s", kind, id)
}

func sidebarPromptCount(application *app) int {
	ids := make(map[string]bool)
	for _, row := range application.sidebarRows(80) {
		if row.promptID != "" {
			ids[row.promptID] = true
		}
	}
	return len(ids)
}

func TestSidebarCatalogColorsActivationAndTenPromptExpansion(t *testing.T) {
	var messages []apiMessage
	for n := 1; n <= 25; n++ {
		messages = append(messages, apiMessage{ID: fmt.Sprintf("msg_b%d", n), Type: "user", Text: fmt.Sprintf("Question B%d", n)}, answer(fmt.Sprintf("msg_reply%d", n), fmt.Sprintf("Response B%d", n)))
	}
	application := pickerTestApp(t, func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/api/session":
			query := request.URL.Query()
			if query.Has("archived") {
				t.Error("all-session catalog unexpectedly filtered archived sessions")
			}
			if query.Get("cursor") == "" {
				io.WriteString(writer, `{"data":[{"id":"ses_a","title":"A","location":{"directory":"/work/a"}}],"cursor":{"next":"page+2="}}`)
			} else {
				if query.Get("cursor") != "page+2=" || query.Has("order") {
					t.Errorf("bad catalog cursor query: %s", request.URL)
				}
				io.WriteString(writer, `{"data":[{"id":"ses_b","title":"B","location":{"directory":"/work/b"}},{"id":"ses_c","title":"C","location":{"directory":"/work/c"}}],"cursor":{"next":null}}`)
			}
		case "/api/session/ses_b":
			io.WriteString(writer, `{"data":{"id":"ses_b","title":"B","location":{"directory":"/work/b"}}}`)
		case "/api/session/ses_b/message":
			data := append([]apiMessage(nil), messages...)
			for a, b := 0, len(data)-1; a < b; a, b = a+1, b-1 {
				data[a], data[b] = data[b], data[a]
			}
			json.NewEncoder(writer).Encode(map[string]any{"data": data, "cursor": map[string]any{"next": nil}})
		case "/api/session/ses_b/permission":
			io.WriteString(writer, `{"data":[]}`)
		case "/api/session/active":
			io.WriteString(writer, `{"data":{}}`)
		default:
			t.Errorf("unexpected request %s", request.URL)
			http.NotFound(writer, request)
		}
	}, sessionInfo{ID: "ses_a", Title: "A", Location: locationRef{Directory: "/work/a"}})
	application.replaceMessages([]apiMessage{{ID: "msg_a", Type: "user", Text: "Question A"}, answer("msg_answer_a", "Response A")})
	application.requestCatalog()
	awaitOperation(t, application, "")
	awaitOperation(t, application, "")
	if application.catalogLoading || len(application.sessions) != 3 {
		t.Fatalf("catalog = %+v", application.sessions)
	}
	var output bytes.Buffer
	application.out = &output
	if err := application.draw(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), workdirStyle) || !strings.Contains(output.String(), sessionNameStyle) {
		t.Fatal("workdir/session pastel colors missing")
	}
	sidebarClick(t, application, "session", "ses_b")
	if application.attachTarget != "ses_b" || sidebarPromptCount(application) != 0 {
		t.Fatal("other session prompts were not folded during attachment")
	}
	awaitOperation(t, application, "attach")
	awaitOperation(t, application, "sync")
	if application.session.ID != "ses_b" || application.promptLimit != 10 || sidebarPromptCount(application) != 10 {
		t.Fatal("session click did not activate latest ten prompts")
	}
	if got := application.selectedResponse(); got != "Response B25" {
		t.Fatalf("initial response = %q", got)
	}
	for _, row := range application.sidebarRows(80) {
		if row.promptID == "msg_a" || row.promptID == "msg_b15" {
			t.Fatal("collapsed/older prompts leaked into initial list")
		}
	}
	sidebarClick(t, application, "more", "")
	if sidebarPromptCount(application) != 20 || application.selectedResponse() != "Response B25" {
		t.Fatal("More did not reveal exactly ten older prompts while preserving selection")
	}
	sidebarClick(t, application, "more", "")
	if sidebarPromptCount(application) != 25 {
		t.Fatal("final partial batch missing")
	}
	for _, row := range application.sidebarRows(80) {
		if row.action == "more" {
			t.Fatal("More remained after the oldest prompt")
		}
	}
	sidebarClick(t, application, "prompt", "msg_b1")
	if application.selectedResponse() != "Response B1" {
		t.Fatal("expanded prompt selected the wrong response")
	}
}

func TestMoreFetchesHistoryBeyondTwoHundredMessages(t *testing.T) {
	var history []apiMessage
	for n := 1; n <= 25; n++ {
		history = append(history, apiMessage{ID: fmt.Sprintf("msg_p%d", n), Type: "user", Text: fmt.Sprintf("Question %d", n)})
		for step := 0; step < 20; step++ {
			history = append(history, answer(fmt.Sprintf("msg_a%d_%d", n, step), fmt.Sprintf("Response %d step %d", n, step)))
		}
	}
	application := pickerTestApp(t, func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/api/session/ses_history":
			io.WriteString(writer, `{"data":{"id":"ses_history","location":{"directory":"/history"}}}`)
		case "/api/session/ses_history/message":
			query := request.URL.Query()
			offset := 0
			if cursor := query.Get("cursor"); cursor != "" {
				if query.Has("order") {
					t.Error("cursor must not be combined with order")
				}
				var err error
				offset, err = strconv.Atoi(cursor)
				if err != nil {
					t.Error(err)
				}
			}
			var data []apiMessage
			end := min(len(history), offset+200)
			for i := offset; i < end; i++ {
				data = append(data, history[len(history)-i-1])
			}
			var next any
			if end < len(history) {
				next = strconv.Itoa(end)
			}
			json.NewEncoder(writer).Encode(map[string]any{"data": data, "cursor": map[string]any{"next": next}})
		case "/api/session/ses_history/permission":
			io.WriteString(writer, `{"data":[]}`)
		case "/api/session/active":
			io.WriteString(writer, `{"data":{}}`)
		default:
			http.NotFound(writer, request)
		}
	}, sessionInfo{ID: "ses_history"})
	snapshot, err := application.api.snapshot(context.Background(), "ses_history", 10)
	if err != nil {
		t.Fatal(err)
	}
	application.replaceMessages(snapshot.messages)
	application.historyCursor = snapshot.messageCursor
	if len(application.messages) <= 200 || sidebarPromptCount(application) != 10 || application.historyCursor == "" {
		t.Fatal("initial ten prompts were truncated at the message-page boundary")
	}
	application.morePrompts()
	awaitOperation(t, application, "sync")
	if sidebarPromptCount(application) != 20 || len(application.messages) != len(history) {
		t.Fatal("More failed to load history beyond the initial pages")
	}
	application.morePrompts()
	application.selectPrompt("msg_p1")
	if sidebarPromptCount(application) != 25 || !strings.Contains(application.selectedResponse(), "Response 1 step 19") || strings.Contains(application.selectedResponse(), "Response 2 step") {
		t.Fatal("expanded history lost a prompt/response boundary")
	}
}

func TestRapidSessionClicksUseLastSelectionAndFailuresRestoreCurrent(t *testing.T) {
	started := make(chan struct{})
	application := pickerTestApp(t, func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/api/session/ses_slow":
			close(started)
			<-request.Context().Done()
		case "/api/session/ses_bad":
			http.Error(writer, `{"message":"unavailable"}`, 503)
		case "/api/session/ses_fast":
			io.WriteString(writer, `{"data":{"id":"ses_fast","location":{"directory":"/fast"}}}`)
		case "/api/session/ses_fast/message":
			io.WriteString(writer, `{"data":[{"id":"msg_fast","type":"user","text":"Fast prompt"}],"cursor":{"next":null}}`)
		case "/api/session/ses_fast/permission":
			io.WriteString(writer, `{"data":[]}`)
		case "/api/session/active":
			io.WriteString(writer, `{"data":{}}`)
		default:
			http.NotFound(writer, request)
		}
	}, sessionInfo{ID: "ses_old"})
	application.attachSession("ses_slow")
	select {
	case <-started:
	case <-time.After(3 * time.Second):
		t.Fatal("slow request did not start")
	}
	application.attachSession("ses_fast")
	for i := 0; i < 3; i++ {
		select {
		case result := <-application.operations:
			application.applyOperation(result)
		case <-time.After(3 * time.Second):
			t.Fatal("attachment timed out")
		}
	}
	if application.session.ID != "ses_fast" || application.attachTarget != "" {
		t.Fatal("late response replaced most recently clicked session")
	}
	application.attachSession("ses_bad")
	awaitOperation(t, application, "attach")
	if application.session.ID != "ses_fast" || application.attachTarget != "" || application.selectedPrompt != "msg_fast" || !strings.Contains(application.status, "unavailable") {
		t.Fatal("failed attachment corrupted active session")
	}
}

func TestSessionBoxesHighlightActivityAndRetainBoundedHitRows(t *testing.T) {
	application, output := promptTestApp(t, []apiMessage{{ID: "msg_prompt", Type: "user", Text: "Question with 世界 and long wrapping text"}}, true)
	application.session.Title = "Current 世界"
	application.session.Location.Directory = "/work/current"
	application.sessions = []sessionInfo{application.session, {ID: "ses_other", Title: "Other", Location: locationRef{Directory: "/work/other"}}}
	application.streamConnected = true
	findTop := func(id string, width int) displayRow {
		t.Helper()
		for _, row := range application.sidebarRows(width) {
			if row.sessionID == id {
				return row
			}
		}
		t.Fatalf("missing session %s", id)
		return displayRow{}
	}
	if row := findTop("ses_test", 60); !strings.Contains(row.text, "Active · Ready") || !strings.HasPrefix(row.text, "┏") || row.style != activeSessionStyle {
		t.Fatalf("active session not boxed/highlighted: %+v", row)
	}
	if row := findTop("ses_other", 60); !strings.Contains(row.text, "Inactive") || !strings.HasPrefix(row.text, "┌") || row.style != dimStyle {
		t.Fatalf("inactive session not boxed: %+v", row)
	}
	application.applyEvent(apiEvent{Type: "session.execution.started", Data: json.RawMessage(`{"sessionID":"ses_other"}`)})
	if row := findTop("ses_other", 60); !strings.Contains(row.text, "Awaiting response") || row.style != waitingSessionStyle {
		t.Fatalf("folded running session lost activity: %+v", row)
	}
	if application.busy || application.selectedPrompt != "msg_prompt" || len(application.messages) != 1 {
		t.Fatal("background activity changed current conversation")
	}
	application.applyEvent(apiEvent{Type: "session.status", Data: json.RawMessage(`{"sessionID":"ses_other","status":{"type":"idle"}}`)})
	if row := findTop("ses_other", 60); !strings.Contains(row.text, "Inactive") {
		t.Fatal("idle event left stale waiting title")
	}
	application.busy = true
	if row := findTop("ses_test", 60); !strings.Contains(row.text, "Awaiting response") {
		t.Fatal("current busy title missing")
	}
	application.busy = false
	application.messages[0].Pending = true
	if row := findTop("ses_test", 60); !strings.Contains(row.text, "Awaiting response") {
		t.Fatal("queued prompt not shown as waiting")
	}
	application.messages[0].Pending = false
	application.attachTarget = "ses_other"
	if row := findTop("ses_other", 60); !strings.Contains(row.text, "Connecting") || row.style != activeSessionStyle {
		t.Fatal("connecting session not highlighted")
	}
	application.attachTarget = ""
	for _, width := range []int{3, 8, 14, 28, 60, 80} {
		for _, row := range application.sidebarRows(width) {
			if row.sessionID == "" && row.promptID == "" && len(row.spans) == 0 {
				continue
			}
			if textWidth(row.text) != width {
				t.Fatalf("box row width %d != %d: %q", textWidth(row.text), width, row.text)
			}
			if len(row.spans) > 0 {
				var text strings.Builder
				for _, span := range row.spans {
					text.WriteString(span.text)
				}
				if text.String() != row.text {
					t.Fatalf("styled box differs from hit row: %q vs %q", text.String(), row.text)
				}
			}
		}
	}
	application.rows, application.cols = 40, 60
	if err := application.draw(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), activeSessionBackground) {
		t.Fatal("active box background missing")
	}
	for y, id := range application.deleteHits {
		if id != "ses_test" {
			continue
		}
		// The border is outside the delete button; its leftmost cell is inside.
		application.handleInput(inputEvent{mouse: &mouseEvent{x: 60, y: y}})
		if application.deleteConfirmation != nil {
			t.Fatal("right border triggered deletion")
		}
		application.handleInput(inputEvent{mouse: &mouseEvent{x: 52, y: y}})
		if application.deleteConfirmation == nil || application.deleteConfirmation.session.ID != id {
			t.Fatal("delete button hit bounds do not match drawing")
		}
		application.closeDeleteConfirmation()
		return
	}
	t.Fatal("no visible delete target")
}

func TestSessionActivitySnapshotsDoNotOverwriteNewerEvents(t *testing.T) {
	application, _ := promptTestApp(t, nil, false)
	application.applyOperation(operationResult{sessionSnapshot: sessionSnapshot{active: map[string]bool{"ses_other": true}}, activityRevision: application.activityRevision})
	if !application.sessionActivity["ses_other"] {
		t.Fatal("snapshot did not restore background activity")
	}
	stale := operationResult{sessionSnapshot: sessionSnapshot{active: map[string]bool{"ses_other": true}}, activityRevision: application.activityRevision}
	application.applyEvent(apiEvent{Type: "session.execution.succeeded", Data: json.RawMessage(`{"sessionID":"ses_other"}`)})
	application.applyOperation(stale)
	if application.sessionActivity["ses_other"] {
		t.Fatal("stale snapshot resurrected completed activity")
	}
	application.applyOperation(operationResult{sessionSnapshot: sessionSnapshot{active: map[string]bool{"ses_other": true}}, activityRevision: application.activityRevision})
	application.applyOperation(operationResult{sessionSnapshot: sessionSnapshot{active: map[string]bool{}}, activityRevision: application.activityRevision})
	if application.sessionActivity["ses_other"] {
		t.Fatal("refresh did not repair missed idle event")
	}
}
