package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"st-go/tui/appbus"
	"st-go/tui/writing"
)

func TestSubscribeBindsCurrentExistingSessionAndRebinds(t *testing.T) {
	t.Chdir(t.TempDir())
	t.Setenv("ST_GO_APPBUS_TOKEN", appbus.ID())
	application, _ := promptTestApp(t, nil, false)
	application.session.ID = "ses_existing"
	if !application.slashCommand("/subscribe paper") || application.writer == nil {
		t.Fatalf("subscribe not handled: %s", application.status)
	}
	first := application.writer
	root, err := writing.Workdir()
	if err != nil || first.project != root || first.session != "ses_existing" || first.bus.Channel != "paper" {
		t.Fatalf("wrong subscription: %+v %v", first, err)
	}
	application.slashCommand("/Subscribe paper")
	if application.writer != first {
		t.Fatal("idempotent subscription lost running history")
	}
	application.session.ID = "ses_other_existing"
	application.slashCommand("/subscribe paper")
	if application.writer == first || application.writer.session != "ses_other_existing" || application.writer.bus.ID != first.bus.ID {
		t.Fatal("same-channel subscription did not bind current session")
	}
	application.writer.tasks["active"] = &writingTask{cancel: func() {}}
	application.session.ID = "ses_third"
	application.slashCommand("/subscribe other")
	if application.writer.session != "ses_other_existing" || !strings.Contains(application.status, "finish or cancel") {
		t.Fatal("active work lost during rebind")
	}
	application.writer.close()
}

func TestWritingCancellationDoesNotInterruptAnotherNormalPrompt(t *testing.T) {
	for _, scenario := range []string{"queued", "current", "completed", "another"} {
		t.Run(scenario, func(t *testing.T) {
			var interrupts, deletes atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.Method + " " + r.URL.Path {
				case "GET /api/session/ses_test/inbox":
					if scenario == "queued" {
						io.WriteString(w, `{"data":[{"id":"msg_request","type":"user"}]}`)
					} else {
						io.WriteString(w, `{"data":[]}`)
					}
				case "DELETE /api/session/ses_test/inbox/msg_request":
					deletes.Add(1)
					w.WriteHeader(http.StatusNoContent)
				case "GET /api/session/ses_test/message":
					messages := []apiMessage{}
					if scenario == "completed" {
						messages = append(messages, apiMessage{ID: "idle", Type: "idle", Outcome: "succeeded"})
					}
					if scenario == "another" {
						messages = append(messages, apiMessage{ID: "msg_normal", Type: "user"})
					}
					messages = append(messages, apiMessage{ID: "msg_request", Type: "user"})
					json.NewEncoder(w).Encode(map[string]any{"data": messages})
				case "POST /api/session/ses_test/interrupt":
					interrupts.Add(1)
					io.WriteString(w, `{}`)
				default:
					http.NotFound(w, r)
				}
			}))
			defer server.Close()
			application, _ := promptTestApp(t, nil, false)
			var err error
			application.api, err = newAPIClient(server.URL, "")
			if err != nil {
				t.Fatal(err)
			}
			bridge, err := newWritingBridge(application, appbus.ID(), t.TempDir(), "paper")
			if err != nil {
				t.Fatal(err)
			}
			bridge.cancelRequest(application.ctx, "msg_request")
			if (interrupts.Load() == 1) != (scenario == "current") || (deletes.Load() == 1) != (scenario == "queued") {
				t.Fatalf("wrong cancellation: %d interrupts, %d deletes", interrupts.Load(), deletes.Load())
			}
		})
	}
}

func TestWritingAmbiguousPromptAndRestartRecoverFromDurableHistory(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, ".govim", "ai-snapshots")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	id, placeholder := appbus.ID(), writing.UUID()
	source := "Previous paragraph.\n\n" + writing.Marker("paper.md", placeholder) + "\nFollowing paragraph."
	object := filepath.Join(dir, "source")
	if err := os.WriteFile(object, []byte(source), 0600); err != nil {
		t.Fatal(err)
	}
	manifest := writing.Manifest{Version: writing.Version, Project: root, Document: "paper.md", Placeholder: placeholder, Files: []writing.File{{Path: "paper.md", Snapshot: object, Hash: writing.Hash(source)}}}
	data, _ := json.Marshal(manifest)
	path := filepath.Join(dir, "manifest.json")
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	begin, end := writing.Markers(id)
	answerText := "Explanation\n" + begin + "\nPredicted paragraph.\n" + end
	var prompts, forks atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer synthetic" {
			http.Error(w, "auth", 401)
			return
		}
		switch r.Method + " " + r.URL.Path {
		case "GET /api/event":
			w.Header().Set("Content-Type", "text/event-stream")
			io.WriteString(w, "event: server.connected\ndata: {}\n\n")
			w.(http.Flusher).Flush()
			<-r.Context().Done()
		case "POST /api/session/ses_test/fork":
			forks.Add(1)
			io.WriteString(w, `{"data":{"id":"ses_writing"}}`)
		case "GET /api/session/ses_test":
			io.WriteString(w, `{"data":{"id":"ses_test"}}`)
		case "POST /api/session/ses_test/prompt":
			prompts.Add(1)
			http.Error(w, "ambiguous gateway failure", 502)
		case "GET /api/session/ses_test/message":
			json.NewEncoder(w).Encode(map[string]any{"data": []apiMessage{{ID: "msg_idle", Type: "idle", Outcome: "succeeded"}, {ID: "msg_answer", Type: "assistant", Content: []messageContent{{Type: "reasoning", Text: "Exposed reasoning recovered from history."}, {Type: "text", Text: answerText}}}, {ID: "msg_" + id, Type: "user", Text: "Writing prompt"}}})
		case "GET /api/session/ses_test/permission":
			io.WriteString(w, `{"data":[]}`)
		case "GET /api/session/active":
			io.WriteString(w, `{"data":{}}`)
		default:
			http.Error(w, "unexpected "+r.Method+" "+r.URL.Path, 404)
		}
	}))
	defer server.Close()
	application, _ := promptTestApp(t, nil, false)
	var err error
	application.api, err = newAPIClient(server.URL, "Bearer synthetic")
	if err != nil {
		t.Fatal(err)
	}
	bridge, err := newWritingBridge(application, appbus.ID(), root, "test-writing")
	if err != nil {
		t.Fatal(err)
	}
	application.session.ID = "ses_unrelated"
	request := writing.Message{Version: writing.Version, Type: "suggest", RequestID: id, Placeholder: placeholder, Project: root, Document: "paper.md", Manifest: path}
	run := func(request writing.Message) writing.Message {
		ctx, cancel := context.WithTimeout(application.ctx, 10*time.Second)
		defer cancel()
		done := make(chan struct{})
		go func() { defer close(done); bridge.generate(ctx, request) }()
		var result writing.Message
		for {
			select {
			case update := <-bridge.updates:
				if update.Type == "result" || update.Type == "failed" || update.Type == "cancelled" {
					result = update
				}
			case <-done:
				for len(bridge.updates) > 0 {
					result = <-bridge.updates
				}
				return result
			case <-ctx.Done():
				t.Fatal("writing worker timed out")
				return result
			}
		}
	}
	result := run(request)
	if result.Type != "result" || result.Prediction != "Predicted paragraph." || result.Reasoning != "Exposed reasoning recovered from history." {
		t.Fatalf("bad recovered result: %+v", result)
	}
	if prompts.Load() != 1 || forks.Load() != 0 || result.Session != "ses_test" {
		t.Fatal("ambiguous request was resent or binding changed")
	}
	request.Type = "status"
	request.Session = result.Session
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	application.session.ID = "ses_test"
	bridge, err = newWritingBridge(application, appbus.ID(), root, "test-writing")
	if err != nil {
		t.Fatal(err)
	}
	result = run(request)
	if result.Type != "result" || prompts.Load() != 1 || forks.Load() != 0 {
		t.Fatalf("restart retriggered generation: %+v", result)
	}
}

func TestWritingCancellationKeepsSessionBusyUntilCleanupSettles(t *testing.T) {
	application, _ := promptTestApp(t, nil, false)
	bridge, err := newWritingBridge(application, appbus.ID(), t.TempDir(), "test-writing")
	if err != nil {
		t.Fatal(err)
	}
	bridge.bus.Out = io.Discard
	first := &writingTask{started: true, document: "paper.md", cancel: func() {}}
	second := &writingTask{document: "another.md", ctx: application.ctx, cancel: func() {}, request: writing.Message{RequestID: "second"}}
	bridge.tasks["first"] = first
	bridge.tasks["second"] = second
	bridge.updates <- writing.Message{RequestID: "first", Type: "cancelled"}
	bridge.tick(time.Now())
	bridge.startPending()
	if first.finished || second.started {
		t.Fatal("next request started before cancellation cleanup")
	}
	bridge.updates <- writing.Message{RequestID: "first", Type: "settled"}
	bridge.tick(time.Now())
	bridge.startPending()
	if !first.finished || !second.started {
		t.Fatal("queue did not resume after cleanup")
	}
}
