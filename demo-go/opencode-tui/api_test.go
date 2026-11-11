package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"syscall"
	"testing"
)

func TestAPIClientUsesV2SessionContract(t *testing.T) {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Header.Get("Authorization") != "Bearer synthetic" {
			http.Error(writer, `{"message":"missing auth"}`, http.StatusUnauthorized)
			return
		}
		writer.Header().Set("Content-Type", "application/json")
		switch request.Method + " " + request.URL.Path {
		case "POST /api/session":
			var body struct {
				Location locationRef `json:"location"`
			}
			if err := json.NewDecoder(request.Body).Decode(&body); err != nil || body.Location.Directory != "/workspace" {
				t.Fatalf("create body = %+v, %v", body, err)
			}
			io.WriteString(writer, `{"data":{"id":"ses_test","projectID":"project","cost":0,"tokens":{},"time":{"created":1,"updated":1},"location":{"directory":"/workspace"}}}`)
		case "GET /api/session/ses_test":
			io.WriteString(writer, `{"data":{"id":"ses_test","projectID":"project","cost":0,"tokens":{},"time":{"created":1,"updated":1},"title":"Test","location":{"directory":"/workspace"}}}`)
		case "GET /api/session/ses_test/message":
			if request.URL.Query().Get("limit") != "200" || request.URL.Query().Get("order") != "desc" {
				t.Fatalf("message query = %s", request.URL.RawQuery)
			}
			io.WriteString(writer, `{"data":[{"id":"msg_2","type":"user","text":"newer","time":{"created":2}},{"id":"msg_1","type":"user","text":"hello","time":{"created":1}}],"cursor":{"previous":null,"next":null}}`)
		case "POST /api/session/ses_test/prompt":
			var body map[string]string
			json.NewDecoder(request.Body).Decode(&body)
			if body["text"] != "show $x^2$" || body["id"] != "msg_stable" {
				t.Fatalf("prompt body = %#v", body)
			}
			io.WriteString(writer, `{"data":{"id":"inbox","sessionID":"ses_test","time":{"created":1},"type":"user","payload":{},"delivery":"queue"}}`)
		case "GET /api/session/ses_test/permission":
			io.WriteString(writer, `{"data":[{"id":"per_1","sessionID":"ses_test","action":"shell","resources":["make test"]}]}`)
		case "POST /api/session/ses_test/permission/per_1/reply":
			var body map[string]string
			json.NewDecoder(request.Body).Decode(&body)
			if body["decision"] != "once" {
				t.Fatalf("permission body = %#v", body)
			}
			writer.WriteHeader(http.StatusNoContent)
		case "POST /api/session/ses_test/interrupt":
			if request.Body != nil {
				data, _ := io.ReadAll(request.Body)
				if len(data) != 0 {
					t.Fatalf("interrupt unexpectedly had a body: %q", data)
				}
			}
			io.WriteString(writer, `{"interrupted":true}`)
		default:
			http.NotFound(writer, request)
		}
	}))
	defer server.Close()

	client, err := newAPIClient(server.URL, "Bearer synthetic")
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	created, err := client.createSession(ctx, "/workspace")
	if err != nil || created.ID != "ses_test" {
		t.Fatalf("create = %+v, %v", created, err)
	}
	loaded, err := client.getSession(ctx, created.ID)
	if err != nil || loaded.Title != "Test" {
		t.Fatalf("get = %+v, %v", loaded, err)
	}
	messages, err := client.messages(ctx, created.ID)
	if err != nil || len(messages) != 2 || messages[0].Text != "hello" || messages[1].Text != "newer" {
		t.Fatalf("messages = %+v, %v", messages, err)
	}
	if err := client.prompt(ctx, created.ID, "show $x^2$", "msg_stable"); err != nil {
		t.Fatal(err)
	}
	permissions, err := client.permissions(ctx, created.ID)
	if err != nil || len(permissions) != 1 {
		t.Fatalf("permissions = %+v, %v", permissions, err)
	}
	if err := client.replyPermission(ctx, created.ID, permissions[0].ID, "once"); err != nil {
		t.Fatal(err)
	}
	if err := client.interrupt(ctx, created.ID); err != nil {
		t.Fatal(err)
	}
}

func TestDecodeSSEIgnoresHeartbeats(t *testing.T) {
	stream := ": heartbeat\n\ndata: {\"type\":\"server.connected\",\"data\":{}}\n\ndata: {\"type\":\"session.text.delta\",\"data\":{\"delta\":\"x\"}}\n\n"
	var events []apiEvent
	err := decodeSSE(strings.NewReader(stream), func(event apiEvent) error {
		events = append(events, event)
		return nil
	})
	if err != nil && !errors.Is(err, io.EOF) || len(events) != 2 || events[0].Type != "server.connected" || events[1].Type != "session.text.delta" {
		t.Fatalf("events = %+v, err = %v", events, err)
	}
}

func TestAPIErrorUsesServerMessage(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.WriteHeader(http.StatusConflict)
		io.WriteString(writer, `{"message":"session is busy"}`)
	}))
	defer server.Close()
	client, _ := newAPIClient(server.URL, "")
	err := client.prompt(context.Background(), "ses", "hello", "msg_error")
	if err == nil || !strings.Contains(err.Error(), "session is busy") {
		t.Fatalf("error = %v", err)
	}
}

func TestBareAddressWithPasswordAuthentication(t *testing.T) {
	t.Setenv("OPENCODE_TUI_AUTHORIZATION", "")
	t.Setenv("OPENCODE_TUI_USERNAME", "")
	t.Setenv("OPENCODE_TUI_PASSWORD", "synthetic:password")
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		username, password, ok := request.BasicAuth()
		if !ok || username != "opencode" || password != "synthetic:password" {
			t.Error("request did not authenticate using the configured password")
			writer.WriteHeader(http.StatusUnauthorized)
			return
		}
		if request.URL.Path != "/api/info" {
			t.Errorf("unexpected API path: %s", request.URL.Path)
		}
		io.WriteString(writer, `{"version":"2.0.32"}`)
	}))
	defer server.Close()
	client, err := newAPIClient(strings.TrimPrefix(server.URL, "http://"), serverAuthorization())
	if err != nil {
		t.Fatal(err)
	}
	if err := client.info(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestConnectionEOFAndTruncatedBodyAreDistinguished(t *testing.T) {
	for _, bodyStarted := range []bool{false, true} {
		name := "before headers"
		if bodyStarted {
			name = "during body"
		}
		t.Run(name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				connection, buffer, err := writer.(http.Hijacker).Hijack()
				if err != nil {
					t.Error(err)
					return
				}
				defer connection.Close()
				if bodyStarted {
					buffer.WriteString("HTTP/1.1 200 OK\r\nContent-Length: 100\r\n\r\n{\"version\":")
					buffer.Flush()
				}
			}))
			defer server.Close()
			client, err := newAPIClient(server.URL, "")
			if err != nil {
				t.Fatal(err)
			}
			err = client.info(context.Background())
			if bodyStarted {
				if !errors.Is(err, io.ErrUnexpectedEOF) || !strings.Contains(err.Error(), "response (200 OK)") {
					t.Fatalf("truncated response diagnostic: %v", err)
				}
			} else if !errors.Is(err, io.EOF) || !strings.Contains(err.Error(), "before an HTTP response") || !strings.Contains(err.Error(), "Docker") {
				t.Fatalf("connection EOF diagnostic: %v", err)
			}
		})
	}
}

func TestInfoRequiresServerInformation(t *testing.T) {
	for _, body := range []string{"", "{}"} {
		server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
			io.WriteString(writer, body)
		}))
		client, err := newAPIClient(server.URL, "")
		if err != nil {
			t.Fatal(err)
		}
		err = client.info(context.Background())
		server.Close()
		if err == nil || !strings.Contains(err.Error(), "/api/info") {
			t.Fatalf("non-OpenCode response %q accepted: %v", body, err)
		}
	}
}

func TestBasicAuthenticationChallengeIsActionable(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("WWW-Authenticate", `Basic realm="Secure Area"`)
		writer.WriteHeader(http.StatusUnauthorized)
		io.WriteString(writer, `{"message":"Authentication required"}`)
	}))
	defer server.Close()
	client, err := newAPIClient(server.URL, "")
	if err != nil {
		t.Fatal(err)
	}
	err = client.info(context.Background())
	if err == nil || !strings.Contains(err.Error(), "401") || !strings.Contains(err.Error(), "OPENCODE_TUI_PASSWORD") {
		t.Fatalf("authentication diagnostic: %v", err)
	}
}

func TestConnectionResetIncludesListenerDiagnostic(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		connection, _, err := writer.(http.Hijacker).Hijack()
		if err != nil {
			t.Error(err)
			return
		}
		defer connection.Close()
		// Reproduce a published container port resetting before HTTP headers.
		if err := connection.(*net.TCPConn).SetLinger(0); err != nil {
			t.Error(err)
		}
	}))
	defer server.Close()
	client, err := newAPIClient(server.URL, "")
	if err != nil {
		t.Fatal(err)
	}
	err = client.info(context.Background())
	if !errors.Is(err, syscall.ECONNRESET) || !strings.Contains(err.Error(), "listener/port mapping") {
		t.Fatalf("connection reset diagnostic: %v", err)
	}
}
