package main

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestSessionDetailsResolveDefaultAndStayVisibleInPickers(t *testing.T) {
	application := pickerTestApp(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("location[directory]") != "/remote" {
			t.Error("metadata lost remote location")
		}
		switch r.URL.Path {
		case "/api/model":
			io.WriteString(w, `{"data":[{"id":"chat","providerID":"p","limit":{"context":1000}}]}`)
		case "/api/model/default":
			io.WriteString(w, `{"data":{"id":"chat","providerID":"p","limit":{"context":1000}}}`)
		default:
			http.NotFound(w, r)
		}
	}, sessionInfo{ID: "ses_test", Agent: "root", Location: locationRef{Directory: "/remote"}})
	application.requestDetails()
	awaitOperation(t, application, "details")
	if application.modelLabel() != "p/chat" || application.variantLabel() != "default" || application.contextPercent() != "0%" {
		t.Fatal("default model/context not resolved")
	}
	application.applyEvent(apiEvent{Type: "session.model.selected", Data: json.RawMessage(`{"sessionID":"ses_test","model":{"id":"chat","providerID":"p","variant":"high"}}`)})
	application.applyEvent(apiEvent{Type: "session.step.ended", Data: json.RawMessage(`{"sessionID":"ses_test","assistantMessageID":"msg_a","tokens":{"input":100,"output":30,"reasoning":20,"cache":{"read":40,"write":10}}}`)})
	if application.contextPercent() != "20%" {
		t.Fatalf("context = %s", application.contextPercent())
	}
	enterCommand(t, application, "/models")
	awaitOperation(t, application, "picker-list")
	var output bytes.Buffer
	application.out, application.rows, application.cols = &output, 12, 60
	if err := application.draw(); err != nil {
		t.Fatal(err)
	}
	for _, wanted := range []string{"Model: p/chat", "Variant: high", "Context: 20%", "/models"} {
		if !strings.Contains(output.String(), wanted) {
			t.Fatalf("header missing %q", wanted)
		}
	}
	if strings.Contains(output.String(), "OpenCode") || strings.Contains(output.String(), "root") {
		t.Fatal("branding/agent prefix remained")
	}
}

func TestContextUsageUsesLatestStepAfterCompactionAndBeforeRevert(t *testing.T) {
	application, _ := promptTestApp(t, nil, false)
	model := modelRef{ID: "chat", ProviderID: "p"}
	info := modelInfo{ID: model.ID, ProviderID: model.ProviderID}
	info.Limit.Context = 1000
	application.session.Model = &model
	application.details.models = []modelInfo{info}
	history := []apiMessage{{ID: "msg_old", Type: "assistant", Model: &model, Tokens: &tokenUsage{Input: 900}}, {ID: "msg_compact", Type: "compaction", Status: "completed"}, {ID: "msg_new", Type: "assistant", Model: &model, Tokens: &tokenUsage{Input: 100}}, {ID: "msg_revert", Type: "user"}, {ID: "msg_last", Type: "assistant", Model: &model, Tokens: &tokenUsage{Input: 200}}}
	application.replaceMessages(history)
	if application.contextPercent() != "20%" {
		t.Fatal("usage summed steps instead of taking the latest")
	}
	application.session.Revert = &struct {
		MessageID string `json:"messageID"`
	}{"msg_revert"}
	application.replaceMessages(history)
	if application.contextPercent() != "10%" {
		t.Fatal("usage crossed the revert boundary")
	}
	application.session.Revert = nil
	application.replaceMessages(history[:2])
	if application.contextPercent() != "0%" {
		t.Fatal("compaction did not reset context")
	}
}
