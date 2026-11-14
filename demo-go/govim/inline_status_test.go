package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"st-go/aiharness"
)

func TestInlineBatchDirectivesStatusAndRemoval(t *testing.T) {
	isolateInlineAI(t)
	var calls atomic.Int32
	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if inlineAPIDiscovery(w, r) {
			return
		}
		var body struct {
			Prompt string           `json:"prompt"`
			Model  *aiharness.Model `json:"model"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
			return
		}
		if body.Model == nil || body.Model.ProviderID != "test" || body.Model.ID != "paragraph" || body.Model.Variant != "deep" {
			t.Errorf("directive model: %+v", body.Model)
		}
		if len(aiharness.Blocks(body.Prompt)) != 2 {
			t.Error("batch did not freeze both slots")
		}
		calls.Add(1)
		select {
		case <-release:
		case <-r.Context().Done():
			return
		}
		json.NewEncoder(w).Encode(map[string]any{"data": map[string]string{"text": "A paragraph."}})
	}))
	defer server.Close()
	defer func() {
		select {
		case <-release:
		default:
			close(release)
		}
	}()
	text := "%server=" + server.URL + "\n%model=test/paragraph\n%variant=deep\nOpening.\n"
	e, out, _ := newPreviewTestEditor(t, "paper.md", text)
	e.inlineOptions.envServer = "http://unused.invalid"
	e.inlineOptions.envModel = "wrong/model"
	settlePreview(t, e)
	editKeys(e, "GA{{{<Enter>{{{<Esc>")
	blocks := aiharness.Blocks(string(e.buffer().text))
	if len(blocks) != 2 || calls.Load() != 0 {
		t.Fatalf("slots: %+v calls:%d", blocks, calls.Load())
	}
	check := func(want uint32, count int) {
		t.Helper()
		status := e.inlineSlotStatus(e.buffer())
		if len(status.Items) != count {
			t.Fatalf("status count=%d want %d", len(status.Items), count)
		}
		for _, item := range status.Items {
			if item.Color != want {
				t.Fatalf("unexpected state: %+v", item)
			}
		}
	}
	check(slotInitial, 2)
	e.execute("AI")
	check(slotProcessing, 2)
	e.execute("AI") // no duplicate dispatch, including queued work
	close(release)
	for _, block := range blocks {
		waitInline(t, e, block.UUID)
	}
	if calls.Load() != 2 {
		t.Fatal("batch was resubmitted")
	}
	check(slotReady, 2)
	out.Reset()
	if err := e.preview.syncSlotStatus(e, time.Now()); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "region secondary status ") {
		t.Fatal("status bar not published")
	}
	e.setPos(0)
	editKeys(e, "<C-j><Esc>")
	status := e.inlineSlotStatus(e.buffer())
	if status.Items[0].Color != slotInserted || status.Items[1].Color != slotReady {
		t.Fatalf("acceptance status: %+v", status)
	}
	editKeys(e, "u")
	check(slotReady, 2)
	current := aiharness.Blocks(string(e.buffer().text))
	e.replace(current[0].Start, current[0].End, "", 0)
	status = e.inlineSlotStatus(e.buffer())
	if len(status.Items) != 1 || !strings.HasPrefix(status.Items[0].Text, blocks[1].UUID[:8]) {
		t.Fatal("deleted UUID remained in status")
	}
	current = aiharness.Blocks(string(e.buffer().text))
	e.replace(current[0].Start, current[0].End, "", 0)
	out.Reset()
	if err := e.preview.syncSlotStatus(e, time.Now()); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "region secondary status clear") {
		t.Fatal("last deleted UUID left stale bar")
	}
	h, _, err := e.inlineProject(e.buffer())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.Latest(blocks[0].UUID); err != nil {
		t.Fatal("removing slot deleted history")
	}
	if strings.Contains(aiharness.ProjectText(text), "%server") || strings.Contains(aiharness.ProjectText(text), "%variant") {
		t.Fatal("directives leaked into preview")
	}
}

func TestInlineExplicitModelAndServerOverrideDirectives(t *testing.T) {
	isolateInlineAI(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if inlineAPIDiscovery(w, r) {
			return
		}
		var body struct {
			Model *aiharness.Model `json:"model"`
		}
		json.NewDecoder(r.Body).Decode(&body)
		if body.Model == nil || body.Model.ID != "override" || body.Model.Variant != "small" {
			t.Errorf("override lost: %+v", body.Model)
		}
		json.NewEncoder(w).Encode(map[string]any{"data": map[string]string{"text": "Override paragraph."}})
	}))
	defer server.Close()
	e := memoryEditor("%server=http://unused.invalid\n%model=other/model\n%variant=large\n")
	e.buffer().meta.Path = filepath.Join(t.TempDir(), "paper.md")
	defer e.close(false)
	e.inlineOptions = inlineAIOptions{server: server.URL, model: &aiharness.Model{ProviderID: "test", ID: "override", Variant: "small"}}
	editKeys(e, "GA{{{<Esc>")
	e.execute("AI")
	id := aiharness.Blocks(string(e.buffer().text))[0].UUID
	r := waitInline(t, e, id)
	if r.Model.ID != "override" || r.Model.Variant != "small" {
		t.Fatal("request did not freeze effective model")
	}
}
