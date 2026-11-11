package main

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

func pickerTestApp(t *testing.T, handler http.HandlerFunc, session sessionInfo) *app {
	t.Helper()
	server := httptest.NewServer(handler)
	client, err := newAPIClient(server.URL, "Bearer test")
	if err != nil {
		t.Fatal(err)
	}
	application := newApp(client, session, nil, nil, io.Discard, -1, &richPreview{})
	t.Cleanup(func() {
		application.closePicker()
		application.closeDeleteConfirmation()
		application.cancel()
		application.input.Close()
		server.Close()
	})
	return application
}

func enterCommand(t *testing.T, application *app, command string) {
	t.Helper()
	application.input.Reset(promptPrefix, len([]rune(promptPrefix)))
	if err := application.input.Insert(command); err != nil {
		t.Fatal(err)
	}
	application.handleInput(inputEvent{key: "<Enter>"})
}

func awaitOperation(t *testing.T, application *app, kind string) {
	t.Helper()
	select {
	case result := <-application.operations:
		if result.kind != kind {
			t.Fatalf("operation = %s, want %s", result.kind, kind)
		}
		application.applyOperation(result)
	case <-time.After(3 * time.Second):
		t.Fatalf("timed out waiting for %s", kind)
	}
}

func TestModelAndVariantCommandsPersistSelection(t *testing.T) {
	var mu sync.Mutex
	var selections []modelRef
	fail := false
	application := pickerTestApp(t, func(writer http.ResponseWriter, request *http.Request) {
		if request.Header.Get("Authorization") != "Bearer test" {
			t.Error("missing authentication")
		}
		switch request.Method + " " + request.URL.Path {
		case "GET /api/model":
			if request.URL.Query().Get("location[directory]") != "/remote/project with space" {
				t.Errorf("model lookup lost session location: %s", request.URL)
			}
			io.WriteString(writer, `{"data":[{"id":"disabled","providerID":"p","name":"Disabled","enabled":false,"variants":[]},{"id":"chat","providerID":"p","name":"Chat model","enabled":true,"variants":[{"id":"low"},{"id":"high"}]}]}`)
		case "POST /api/session/ses_one/model":
			var body struct {
				Model modelRef `json:"model"`
			}
			if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
				t.Error(err)
			}
			mu.Lock()
			defer mu.Unlock()
			if fail {
				http.Error(writer, `{"message":"model unavailable"}`, http.StatusServiceUnavailable)
				return
			}
			selections = append(selections, body.Model)
			writer.WriteHeader(http.StatusNoContent)
		default:
			t.Errorf("slash command triggered unexpected API call: %s %s", request.Method, request.URL)
			http.NotFound(writer, request)
		}
	}, sessionInfo{ID: "ses_one", Location: locationRef{Directory: "/remote/project with space"}})

	enterCommand(t, application, "/models chat")
	awaitOperation(t, application, "picker-list")
	if application.picker == nil || len(application.picker.visible()) != 1 {
		t.Fatal("enabled model filter failed")
	}
	application.handleInput(inputEvent{key: "<Enter>"})
	awaitOperation(t, application, "picker-model")
	if application.picker != nil || application.session.Model.ID != "chat" {
		t.Fatal("model selection was not applied")
	}

	enterCommand(t, application, "/variants")
	awaitOperation(t, application, "picker-list")
	application.handleInput(inputEvent{paste: "high"})
	application.handleInput(inputEvent{key: "<Enter>"})
	awaitOperation(t, application, "picker-model")
	if application.session.Model.Variant != "high" || !strings.Contains(application.header(), "p/chat") || !strings.Contains(application.detailsLine(), "high") {
		t.Fatal("selected variant missing from current session/header")
	}

	enterCommand(t, application, "/models")
	awaitOperation(t, application, "picker-list")
	application.handleInput(inputEvent{key: "<Enter>"})
	awaitOperation(t, application, "picker-model")
	if application.session.Model.Variant != "high" {
		t.Fatal("reselecting current model dropped the variant")
	}

	enterCommand(t, application, "/variants default")
	awaitOperation(t, application, "picker-list")
	application.handleInput(inputEvent{key: "<Enter>"})
	awaitOperation(t, application, "picker-model")
	if application.session.Model.Variant != "" {
		t.Fatal("default variant did not clear explicit variant")
	}
	mu.Lock()
	if len(selections) != 4 || selections[1].Variant != "high" || selections[3].Variant != "" {
		t.Errorf("persisted selections: %+v", selections)
	}
	fail = true
	mu.Unlock()

	enterCommand(t, application, "/variants high")
	awaitOperation(t, application, "picker-list")
	application.handleInput(inputEvent{key: "<Enter>"})
	awaitOperation(t, application, "picker-model")
	if application.picker == nil || !strings.Contains(application.picker.err, "model unavailable") || application.session.Model.Variant != "" {
		t.Fatal("failed model selection did not preserve previous model and display the error")
	}
	application.handleInput(inputEvent{key: "<Esc>"})
	if application.picker != nil || len(application.messages) != 0 || application.busy {
		t.Fatal("slash commands leaked into transcript/execution or Esc did not close picker")
	}
}

func TestVariantsResolveServerDefault(t *testing.T) {
	application := pickerTestApp(t, func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/api/model":
			io.WriteString(writer, `{"data":[]}`)
		case "/api/model/default":
			if request.URL.Query().Get("location[directory]") != "/project" {
				t.Error("default model query is not location scoped")
			}
			io.WriteString(writer, `{"data":{"id":"default-chat","providerID":"provider","enabled":true,"variants":[{"id":"deep"}]}}`)
		default:
			http.NotFound(writer, request)
		}
	}, sessionInfo{ID: "ses_default", Location: locationRef{Directory: "/project"}})
	enterCommand(t, application, "/variants")
	awaitOperation(t, application, "picker-list")
	options := application.picker.visible()
	if len(options) != 2 || options[1].model.ID != "default-chat" || options[1].model.Variant != "deep" || application.session.Model != nil {
		t.Fatalf("default-model variants = %+v", options)
	}
}

func TestSessionsPageSearchAndAttach(t *testing.T) {
	application := pickerTestApp(t, func(writer http.ResponseWriter, request *http.Request) {
		switch request.Method + " " + request.URL.Path {
		case "GET /api/session":
			query := request.URL.Query()
			if query.Get("cursor") == "" && query.Get("order") != "desc" || query.Get("limit") != "50" || query.Get("archived") != "false" {
				t.Errorf("session list query: %s", request.URL)
			}
			if query.Get("search") == "remote math" {
				io.WriteString(writer, `{"data":[{"id":"ses_two","title":"Remote math","location":{"directory":"/remote"}}],"cursor":{"next":null}}`)
			} else if query.Get("cursor") == "next+page/=" {
				io.WriteString(writer, `{"data":[{"id":"ses_two","title":"Remote math","location":{"directory":"/remote"}}],"cursor":{"next":null}}`)
			} else {
				io.WriteString(writer, `{"data":[{"id":"ses_one","title":"Original"}],"cursor":{"next":"next+page/="}}`)
			}
		case "GET /api/session/ses_two":
			io.WriteString(writer, `{"data":{"id":"ses_two","title":"Remote math","model":{"id":"chat","providerID":"p","variant":"high"},"location":{"directory":"/remote"}}}`)
		case "GET /api/session/ses_two/message":
			io.WriteString(writer, `{"data":[{"id":"msg_two","type":"assistant","content":[{"type":"text","text":"Use $x^2$"}]}]}`)
		case "GET /api/session/ses_two/permission":
			io.WriteString(writer, `{"data":[{"id":"per_two","sessionID":"ses_two","action":"shell"}]}`)
		case "GET /api/session/active":
			io.WriteString(writer, `{"data":{"ses_two":{"type":"running"}}}`)
		default:
			t.Errorf("unexpected request: %s %s", request.Method, request.URL)
			http.NotFound(writer, request)
		}
	}, sessionInfo{ID: "ses_one", Location: locationRef{Directory: "/original"}})
	application.preview.generation = 10
	application.preview.published = 10
	application.preview.pending = &richRenderRequest{generation: 10}
	enterCommand(t, application, "/sessions")
	awaitOperation(t, application, "picker-list")
	application.handleInput(inputEvent{key: "<Down>"}) // Load more, after current session.
	application.handleInput(inputEvent{key: "<Enter>"})
	awaitOperation(t, application, "picker-list")
	if application.picker.next != "" || application.picker.visible()[application.picker.selected].sessionID != "ses_two" {
		t.Fatal("session pagination did not replace Load more with the next page")
	}
	application.handleInput(inputEvent{paste: "remote math"})
	application.tickPicker(time.Now().Add(time.Second))
	awaitOperation(t, application, "picker-list")
	if len(application.picker.visible()) != 2 || application.picker.visible()[application.picker.selected].sessionID != "ses_two" {
		t.Fatal("search did not return/select server matches")
	}
	application.handleInput(inputEvent{key: "<Enter>"})
	awaitOperation(t, application, "picker-session")
	awaitOperation(t, application, "sync")
	if application.session.ID != "ses_two" || application.session.Location.Directory != "/remote" || !application.busy || len(application.permissions) != 1 || len(application.messages) != 1 || application.messages[0].ID != "msg_two" {
		t.Fatalf("attached session state: %+v, messages %+v", application.session, application.messages)
	}
	if application.picker != nil || application.preview.published != 0 || application.preview.pending != nil || application.preview.generation <= 10 || application.selectedResponse() != "Use $x^2$" {
		t.Fatal("old session preview was not invalidated")
	}
	old := operationResult{kind: "sync", sessionID: "ses_one", sessionSnapshot: sessionSnapshot{messages: []apiMessage{{ID: "stale", Type: "user"}}}}
	application.applyOperation(old)
	application.applyOperation(operationResult{kind: "prompt", sessionID: "ses_one", err: io.EOF})
	application.applyEvent(apiEvent{Type: "session.text.delta", Data: json.RawMessage(`{"sessionID":"ses_one","assistantMessageID":"stale","ordinal":0,"delta":"old"}`)})
	if application.session.ID != "ses_two" || len(application.messages) != 1 || application.messages[0].ID != "msg_two" || strings.Contains(application.status, "EOF") {
		t.Fatal("old session response contaminated newly attached session")
	}
}

func TestCanceledPickerIgnoresLateResponseAndPreservesDraft(t *testing.T) {
	application := pickerTestApp(t, func(writer http.ResponseWriter, request *http.Request) {
		io.WriteString(writer, `{"data":[{"id":"chat","providerID":"p","enabled":true}]}`)
	}, sessionInfo{ID: "ses_one"})
	_ = application.input.Insert("unsent draft")
	application.openPicker("models", "")
	var result operationResult
	select {
	case result = <-application.operations:
	case <-time.After(3 * time.Second):
		t.Fatal("picker request timed out")
	}
	application.handleInput(inputEvent{key: "<Esc>"})
	application.applyOperation(result)
	if application.picker != nil || application.input.Text() != promptPrefix+"unsent draft" {
		t.Fatal("late response reopened picker or changed composer draft")
	}
	application.openPicker("models", "")
	application.applyOperation(result)
	if !application.picker.loading || len(application.picker.options) != 0 {
		t.Fatal("old request replaced the new picker")
	}
	awaitOperation(t, application, "picker-list")
	var screen bytes.Buffer
	application.out = &screen
	application.rows, application.cols = 5, 8
	if err := application.draw(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(screen.String(), "/models") || !strings.Contains(screen.String(), "Filter:") {
		t.Fatal("narrow picker did not render header/filter")
	}
}
