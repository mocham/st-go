package main

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
)

func confirmDelete(application *app) {
	application.handleInput(inputEvent{paste: "DELETE"})
	application.handleInput(inputEvent{key: "<Enter>"})
}

func clickDelete(t *testing.T, application *app, id string) {
	t.Helper()
	application.rows, application.cols = 40, 120
	application.sidebarFocus = ""
	application.scroll = 0
	if err := application.draw(); err != nil {
		t.Fatal(err)
	}
	for y, target := range application.deleteHits {
		if target == id {
			application.handleInput(inputEvent{mouse: &mouseEvent{x: application.paintedLayout.prompts.width - 2, y: y}})
			return
		}
	}
	t.Fatalf("no visible delete button for %s", id)
}

func TestDeleteSessionSwitchesSurvivorRemovesChildrenAndCanCreateAfterLast(t *testing.T) {
	var fail atomic.Bool
	application := pickerTestApp(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "DELETE" {
			if fail.Load() {
				http.Error(w, `{"message":"cannot delete"}`, 500)
				return
			}
			w.WriteHeader(204)
			return
		}
		switch r.URL.Path {
		case "/api/session":
			io.WriteString(w, `{"data":{"id":"ses_new","location":{"directory":"/project"}}}`)
		case "/api/session/ses_b":
			io.WriteString(w, `{"data":{"id":"ses_b","location":{"directory":"/project"}}}`)
		case "/api/session/ses_new":
			io.WriteString(w, `{"data":{"id":"ses_new","location":{"directory":"/project"}}}`)
		case "/api/session/active":
			io.WriteString(w, `{"data":{}}`)
		default:
			io.WriteString(w, `{"data":[]}`)
		}
	}, sessionInfo{ID: "ses_a", Location: locationRef{Directory: "/project"}})
	application.sessions = []sessionInfo{application.session, {ID: "ses_child", ParentID: "ses_a"}, {ID: "ses_b"}}
	stale := operationResult{catalog: &catalogResult{sequence: application.catalogSequence, first: true, done: true, sessions: append([]sessionInfo(nil), application.sessions...)}}
	clickDelete(t, application, "ses_a")
	confirmDelete(application)
	awaitOperation(t, application, "delete")
	awaitOperation(t, application, "attach")
	awaitOperation(t, application, "sync")
	application.applyOperation(stale)
	if application.session.ID != "ses_b" || len(application.sessions) != 1 || !application.deleted["ses_child"] {
		t.Fatal("delete did not switch to survivor/remove descendants, or stale catalog resurrected them")
	}
	fail.Store(true)
	clickDelete(t, application, "ses_b")
	confirmDelete(application)
	awaitOperation(t, application, "delete")
	if application.session.ID != "ses_b" || application.deleted["ses_b"] || !strings.Contains(application.status, "cannot delete") {
		t.Fatal("failed delete removed the session")
	}
	fail.Store(false)
	clickDelete(t, application, "ses_b")
	confirmDelete(application)
	awaitOperation(t, application, "delete")
	if application.session.ID != "" || len(application.sessions) != 0 {
		t.Fatal("last deleted session remained active")
	}
	sidebarClick(t, application, "new", "")
	awaitOperation(t, application, "attach")
	awaitOperation(t, application, "sync")
	if application.session.ID != "ses_new" {
		t.Fatal("could not create a session after deleting the last one")
	}
}

func TestDeleteRequiresExactConfirmationAndPreservesDraft(t *testing.T) {
	var calls atomic.Int32
	application := pickerTestApp(t, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.Method != "DELETE" || r.URL.Path != "/api/session/ses_b" {
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
		w.WriteHeader(204)
	}, sessionInfo{ID: "ses_a", Title: "Current"})
	application.sessions = []sessionInfo{application.session, {ID: "ses_b", Title: "Delete this", Location: locationRef{Directory: "/work/b"}}}
	application.input.Insert("unfinished draft")
	draft := application.input.Text()
	for _, cancel := range []string{"<Esc>", "\x03"} {
		clickDelete(t, application, "ses_b")
		if application.deleteConfirmation == nil || len(application.deleting) != 0 {
			t.Fatal("delete click bypassed confirmation")
		}
		application.handleInput(inputEvent{paste: "DELETE"})
		application.handleInput(inputEvent{key: cancel})
		if application.deleteConfirmation != nil || application.input.Text() != draft || !application.running {
			t.Fatal("cancellation lost draft or quit client")
		}
	}
	clickDelete(t, application, "ses_b")
	var output bytes.Buffer
	application.out = &output
	if err := application.draw(); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"Delete this", "ses_b", "/work/b", "Type DELETE"} {
		if !strings.Contains(output.String(), want) {
			t.Fatalf("confirmation omitted %q", want)
		}
	}
	// Background permissions and clicks cannot steal confirmation input or
	// retarget the pending deletion to the current session.
	application.permissions = []permissionRequest{{ID: "permission", SessionID: "ses_a"}}
	application.handleInput(inputEvent{mouse: &mouseEvent{x: 2, y: 6}})
	application.handleInput(inputEvent{key: "<Tab>"})
	application.deleteSession("ses_a")
	for _, text := range []string{"", "delete", "DELETE ", " DELETE", "DELETE\n"} {
		application.deleteConfirmation.input.Reset("Confirm: ", len("Confirm: "))
		application.handleInput(inputEvent{paste: text})
		application.handleInput(inputEvent{key: "<Enter>"})
		if application.deleteConfirmation == nil || application.deleteConfirmation.err == "" || len(application.deleting) != 0 {
			t.Fatalf("invalid confirmation %q was accepted", text)
		}
	}
	application.deleteConfirmation.input.Reset("Confirm: ", len("Confirm: "))
	for _, r := range "DELETE" {
		application.handleInput(inputEvent{key: string(r)})
	}
	application.handleInput(inputEvent{key: "<Enter>"})
	awaitOperation(t, application, "delete")
	if calls.Load() != 1 || !application.deleted["ses_b"] || application.deleted["ses_a"] || application.input.Text() != draft || application.deleteConfirmation != nil {
		t.Fatal("confirmation sent the wrong request or altered the composer")
	}
}

func TestExternalDeleteCancelsConfirmationForDescendant(t *testing.T) {
	application := pickerTestApp(t, func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
	}, sessionInfo{ID: "ses_current"})
	application.sessions = []sessionInfo{application.session, {ID: "ses_parent"}, {ID: "ses_child", ParentID: "ses_parent"}}
	application.deleteSession("ses_child")
	application.handleInput(inputEvent{paste: "DELETE"})
	application.applyEvent(apiEvent{Type: "session.deleted", Data: json.RawMessage(`{"sessionID":"ses_parent"}`)})
	if application.deleteConfirmation != nil || !application.deleted["ses_child"] || len(application.deleting) != 0 {
		t.Fatal("external deletion left stale confirmation")
	}
}
