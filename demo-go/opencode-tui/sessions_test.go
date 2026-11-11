package main

import (
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
)

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
	awaitOperation(t, application, "delete")
	awaitOperation(t, application, "attach")
	awaitOperation(t, application, "sync")
	application.applyOperation(stale)
	if application.session.ID != "ses_b" || len(application.sessions) != 1 || !application.deleted["ses_child"] {
		t.Fatal("delete did not switch to survivor/remove descendants, or stale catalog resurrected them")
	}
	fail.Store(true)
	clickDelete(t, application, "ses_b")
	awaitOperation(t, application, "delete")
	if application.session.ID != "ses_b" || application.deleted["ses_b"] || !strings.Contains(application.status, "cannot delete") {
		t.Fatal("failed delete removed the session")
	}
	fail.Store(false)
	clickDelete(t, application, "ses_b")
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
