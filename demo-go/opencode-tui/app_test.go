package main

import (
	"encoding/json"
	"io"
	"testing"
)

func TestStreamingTextBuildsAssistantResponse(t *testing.T) {
	application := newApp(nil, sessionInfo{ID: "ses_test"}, []apiMessage{{ID: "msg_prompt", Type: "user", Text: "Math?"}}, nil, io.Discard, -1, &richPreview{})
	defer application.cancel()
	defer application.input.Close()
	apply := func(kind string, data any) {
		encoded, err := json.Marshal(data)
		if err != nil {
			t.Fatal(err)
		}
		application.applyEvent(apiEvent{Type: kind, Data: encoded})
	}
	apply("session.text.started", map[string]any{"sessionID": "ses_test", "assistantMessageID": "msg_a", "ordinal": 0})
	apply("session.text.delta", map[string]any{"sessionID": "ses_test", "assistantMessageID": "msg_a", "ordinal": 0, "delta": "Use $x"})
	apply("session.text.delta", map[string]any{"sessionID": "ses_test", "assistantMessageID": "msg_a", "ordinal": 0, "delta": "^2$."})
	apply("session.text.ended", map[string]any{"sessionID": "ses_test", "assistantMessageID": "msg_a", "ordinal": 0, "text": "Use $x^2$."})
	if len(application.messages) != 2 || application.messages[1].Rich != "Use $x^2$." || application.selectedPrompt != "msg_prompt" || application.messages[1].PromptID != "msg_prompt" || !application.busy {
		t.Fatalf("streamed application = %+v", application)
	}
}

func TestFailedPromptRestoresIdleState(t *testing.T) {
	application := newApp(nil, sessionInfo{ID: "ses_test"}, nil, nil, io.Discard, -1, &richPreview{})
	defer application.cancel()
	defer application.input.Close()
	application.busy = true
	application.messages = []chatMessage{{ID: "local-1", Role: "user", Text: "hello"}}
	application.rebuildIndex()
	application.applyOperation(operationResult{kind: "prompt", requestID: "local-1", wasBusy: false, err: &apiError{400, "rejected"}})
	if application.busy || !application.messages[0].Failed || application.messages[0].Text != "hello" || application.status != "rejected" {
		t.Fatalf("failed prompt state = busy %v, message %q, status %q", application.busy, application.messages[0].Text, application.status)
	}
}

func TestPermissionEventsAreSessionScoped(t *testing.T) {
	application := newApp(nil, sessionInfo{ID: "ses_test"}, nil, nil, io.Discard, -1, &richPreview{})
	defer application.cancel()
	defer application.input.Close()
	request, _ := json.Marshal(permissionRequest{ID: "per_1", SessionID: "ses_other", Action: "shell", Resources: []string{"rm"}})
	application.applyEvent(apiEvent{Type: "permission.asked", Data: request})
	if len(application.permissions) != 0 {
		t.Fatal("accepted permission for another session")
	}
	request, _ = json.Marshal(permissionRequest{ID: "per_2", SessionID: "ses_test", Action: "shell", Resources: []string{"make test"}})
	application.applyEvent(apiEvent{Type: "permission.asked", Data: request})
	application.applyEvent(apiEvent{Type: "permission.asked", Data: request})
	if len(application.permissions) != 1 || application.permissions[0].ID != "per_2" {
		t.Fatalf("permissions = %+v", application.permissions)
	}
}
