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
