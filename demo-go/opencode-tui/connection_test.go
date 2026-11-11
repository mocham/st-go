package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestPromptSurvivesLongAcknowledgementAndHistoryRecoversWithoutSwitch(t *testing.T) {
	if testing.Short() {
		t.Skip("exercises the old 20/30-second cutoffs")
	}
	started := make(chan string, 1)
	var promptID string
	var admitted atomic.Value
	application := pickerTestApp(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.Method + " " + r.URL.Path {
		case "POST /api/session/ses_slow/prompt":
			var body map[string]string
			json.NewDecoder(r.Body).Decode(&body)
			admitted.Store(body["id"])
			started <- body["id"]
			select {
			case <-time.After(31 * time.Second):
				io.WriteString(w, `{"data":{}}`)
			case <-r.Context().Done():
			}
		case "GET /api/session/ses_slow":
			io.WriteString(w, `{"data":{"id":"ses_slow"}}`)
		case "GET /api/session/ses_slow/message":
			json.NewEncoder(w).Encode(map[string]any{"data": []apiMessage{answer("msg_answer", "Completed during the delayed HTTP response"), {ID: admitted.Load().(string), Type: "user", Text: "slow"}}})
		case "GET /api/session/ses_slow/permission", "GET /api/session/ses_slow/inbox":
			io.WriteString(w, `{"data":[]}`)
		case "GET /api/session/active":
			io.WriteString(w, `{"data":{}}`)
		default:
			t.Errorf("unexpected request: %s", r.URL)
			http.NotFound(w, r)
		}
	}, sessionInfo{ID: "ses_slow"})
	enterCommand(t, application, "slow")
	select {
	case promptID = <-started:
	case <-time.After(time.Second):
		t.Fatal("prompt did not start")
	}
	application.tickConnection(time.Now().Add(6 * time.Second))
	awaitOperation(t, application, "sync")
	if application.selectedResponse() != "Completed during the delayed HTTP response" || application.messages[0].Failed {
		t.Fatal("periodic history refresh failed to recover the response")
	}
	select {
	case result := <-application.operations:
		if result.kind != "prompt" || result.err != nil {
			t.Fatalf("long prompt request failed: %v", result.err)
		}
		application.applyOperation(result)
	case <-time.After(35 * time.Second):
		t.Fatal("long request never completed")
	}
	if application.busy || application.messages[0].Failed || application.selectedPrompt != promptID {
		t.Fatal("late acknowledgement corrupted recovered state")
	}
}

func TestStreamWatchCountsKeepalivesAndReconnectsStalledConnections(t *testing.T) {
	for _, mode := range []string{"heartbeat", "stalled", "no-handshake"} {
		t.Run(mode, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "text/event-stream")
				if mode != "no-handshake" {
					fmt.Fprint(w, "data: {\"type\":\"server.connected\",\"data\":{}}\n\n")
				}
				w.(http.Flusher).Flush()
				if mode == "stalled" {
					<-r.Context().Done()
					return
				}
				for n := 0; n < 8; n++ {
					select {
					case <-time.After(20 * time.Millisecond):
					case <-r.Context().Done():
						return
					}
					fmt.Fprint(w, ": keepalive\n\n")
					w.(http.Flusher).Flush()
				}
				fmt.Fprint(w, "data: {\"type\":\"session.text.delta\",\"data\":{\"delta\":\"answer\"}}\n\n")
				w.(http.Flusher).Flush()
				<-r.Context().Done()
			}))
			defer server.Close()
			client, _ := newAPIClient(server.URL, "")
			client.streamConnectTimeout, client.streamIdleTimeout = 80*time.Millisecond, 80*time.Millisecond
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			done := errors.New("received answer")
			err := client.subscribe(ctx, func(event apiEvent) error {
				if event.Type == "session.text.delta" {
					return done
				}
				return nil
			})
			switch mode {
			case "heartbeat":
				if !errors.Is(err, done) {
					t.Fatalf("healthy quiet stream disconnected: %v", err)
				}
			case "stalled":
				if err == nil || !strings.Contains(err.Error(), "stalled") {
					t.Fatalf("half-open stream was not detected: %v", err)
				}
			case "no-handshake":
				if err == nil || !strings.Contains(err.Error(), "connecting") {
					t.Fatalf("missing handshake was not detected: %v", err)
				}
			}
		})
	}
}

func TestAmbiguousPromptErrorsKeepServerAcknowledgements(t *testing.T) {
	application, _ := promptTestApp(t, nil, false)
	application.messages = []chatMessage{{ID: "msg_p", Role: "user", Text: "question", Pending: true}}
	application.rebuildIndex()
	application.selectedPrompt, application.activePrompt = "msg_p", "msg_p"
	application.applyOperation(operationResult{kind: "prompt", requestID: "msg_p", err: context.DeadlineExceeded})
	if application.messages[0].Failed || !application.messages[0].Uncertain || application.nextSync.IsZero() {
		t.Fatal("timeout was treated as a definitive rejection")
	}
	application.applyEvent(apiEvent{Type: "session.inbox.enqueued", Data: json.RawMessage(`{"sessionID":"ses_test","inboxID":"msg_p","item":{"type":"user","payload":{"text":"question"}}}`)})
	application.applyOperation(operationResult{kind: "prompt", requestID: "msg_p", err: io.EOF})
	if !application.messages[0].Acknowledged || application.messages[0].Failed || application.messages[0].Uncertain {
		t.Fatal("late HTTP failure overrode server acknowledgement")
	}
	application.applyEvent(apiEvent{Type: "session.text.delta", Data: json.RawMessage(`{"sessionID":"ses_test","assistantMessageID":"msg_reply","ordinal":0,"delta":"answer"}`)})
	if application.selectedResponse() != "answer" {
		t.Fatal("response did not recover without a session switch")
	}
	application.applyEvent(apiEvent{Type: "session.inbox.cancelled", Data: json.RawMessage(`{"sessionID":"ses_test","inboxID":"msg_p"}`)})
	application.applyOperation(operationResult{kind: "prompt", requestID: "msg_p"})
	if !application.messages[0].Failed {
		t.Fatal("late HTTP success undid a server cancellation")
	}
}

func TestSSEFailureFrameIsReported(t *testing.T) {
	err := decodeSSE(strings.NewReader("event: effect/http-api/stream/failure\ndata: [{\"_tag\":\"Die\",\"defect\":\"overflow\"}]\n\n"), func(apiEvent) error { t.Fatal("failure emitted as a business event"); return nil })
	if err == nil || !strings.Contains(err.Error(), "overflow") {
		t.Fatalf("SSE failure lost its reason: %v", err)
	}
}
