package aiharness

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
	"unicode/utf8"
)

type generateFunc func(context.Context, string, *Model) (string, error)

func (f generateFunc) Generate(ctx context.Context, prompt string, model *Model) (string, error) {
	return f(ctx, prompt, model)
}

func awaitResult(t *testing.T, h *Harness) Record {
	t.Helper()
	select {
	case r := <-h.Updates():
		return r
	case <-time.After(5 * time.Second):
		t.Fatal("generation did not finish")
		return Record{}
	}
}

func TestProjectDirectivesAndBlockProjection(t *testing.T) {
	root := t.TempDir()
	document := filepath.Join(root, "paper.tex")
	t.Chdir(t.TempDir())
	p, err := Resolve(document, "%project=history/shared.db\n%context=notes.txt\nDocument")
	if err != nil || p.Database != filepath.Join(root, "history/shared.db") || p.ContextFile != filepath.Join(root, "notes.txt") {
		t.Fatalf("resolution: %+v %v", p, err)
	}
	p, err = Resolve(document, "Document")
	if err != nil || p.Database != document+".db" {
		t.Fatalf("default: %+v %v", p, err)
	}
	for _, text := range []string{"%project=\n", "%context=a\n%context=b", "%project=paper.tex", "%project=x\n%context=x"} {
		if _, err := Resolve(document, text); err == nil {
			t.Fatalf("accepted %q", text)
		}
	}
	id := UUID()
	body := `Unicode 文 and \textbf{\emph{nested}} plus \{literal\}.`
	text := "%project=x.db\n%context=notes.txt\n前文 " + BlockText(id, body) + "\nAfter"
	b, err := FindBlock(text, id)
	if err != nil || string([]rune(text)[b.Content:b.Close]) != body {
		t.Fatalf("block: %+v %v", b, err)
	}
	projected := ProjectText(text)
	if !strings.Contains(projected, body) || strings.Contains(projected, id) || strings.Contains(projected, "%project") || strings.Count(projected, "\n") != strings.Count(text, "\n") {
		t.Fatalf("projection: %s", projected)
	}
	if _, err := FindBlock(text+BlockText(id, Slot), id); err == nil {
		t.Fatal("duplicate UUID accepted")
	}
	if b.End > utf8.RuneCountInString(text) {
		t.Fatal("byte offsets returned as runes")
	}
}

func TestStatelessRequestAndVersionedDictionary(t *testing.T) {
	root := t.TempDir()
	// Ambient files and prior generations must never be read as model context.
	if err := os.WriteFile(filepath.Join(root, "AGENTS.md"), []byte("AMBIENT_SENTINEL"), 0600); err != nil {
		t.Fatal(err)
	}
	var calls atomic.Int32
	requests := make(chan string, 4)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/openapi.json" && r.Method == "GET" {
			if r.Header.Get("Authorization") != "Bearer test" {
				t.Error("API discovery lost authorization")
			}
			w.Write([]byte(`{"paths":{"/api/experimental/generate/text":{"post":{}}}}`))
			return
		}
		if r.Method != "POST" || r.URL.Path != "/api/experimental/generate/text" || r.Header.Get("Authorization") != "Bearer test" {
			t.Errorf("unexpected request: %s %s", r.Method, r.URL)
			http.Error(w, "bad route/auth", 400)
			return
		}
		var body struct {
			Prompt string `json:"prompt"`
			Model  *Model `json:"model"`
		}
		decoder := json.NewDecoder(r.Body)
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&body); err != nil {
			t.Error(err)
			return
		}
		if body.Model == nil || body.Model.ProviderID != "test" || body.Model.ID != "paragraph" || body.Model.Variant != "small" {
			t.Errorf("model: %+v", body.Model)
		}
		requests <- body.Prompt
		n := calls.Add(1)
		paragraph := "First paragraph."
		if n > 1 {
			paragraph = "Second paragraph."
		}
		blocks := Blocks(body.Prompt)
		if len(blocks) != 1 {
			t.Error("expected one target document block")
			return
		}
		reply, _ := json.Marshal(map[string]string{blocks[0].UUID: paragraph})
		json.NewEncoder(w).Encode(map[string]any{"data": map[string]string{"text": string(reply)}})
	}))
	defer server.Close()
	client, err := NewOpenCode(server.URL, "Bearer test")
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "new", "project.db")
	h, err := Open(path, client)
	if err != nil {
		t.Fatal(err)
	}
	defer h.Close()
	id := UUID()
	request := Request{UUID: id, Document: filepath.Join(root, "paper.md"), Instruction: "Continue the argument.", Context: "EXPLICIT_CONTEXT", CurrentDocument: "Before\n" + BlockText(id, Slot) + "\nAfter", Model: &Model{"test", "paragraph", "small"}}
	first, err := h.Submit(request)
	if err != nil {
		t.Fatal(err)
	}
	request.Model.ID = "mutated-after-submit"
	first = awaitResult(t, h)
	if first.Status != "ready" || first.Version != 1 || first.Paragraph != "First paragraph." {
		t.Fatalf("first result: %+v", first)
	}
	sent := <-requests
	if sent != first.Prompt || !strings.Contains(sent, "EXPLICIT_CONTEXT") || !strings.Contains(sent, "After") || strings.Contains(sent, "AMBIENT_SENTINEL") {
		t.Fatalf("unexpected prompt: %q", sent)
	}
	if err := h.RecordAction(first, "inserted", first.Paragraph); err != nil {
		t.Fatal(err)
	}
	if err := h.RecordAction(first, "discarded", "Author edited paragraph."); err != nil {
		t.Fatal(err)
	}
	request.Model.ID = "paragraph"
	if _, err := h.Submit(request); err != nil {
		t.Fatal(err)
	}
	second := awaitResult(t, h)
	if second.Version != 2 || second.Paragraph != "Second paragraph." || strings.Contains(<-requests, "First paragraph.") {
		t.Fatalf("version/context leak: %+v", second)
	}
	if err := h.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(path, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	latest, err := reopened.Latest(id)
	if err != nil || latest.Version != 2 || latest.Paragraph != second.Paragraph {
		t.Fatalf("dictionary recovery: %+v %v", latest, err)
	}
	var original, discarded string
	if err := reopened.db.QueryRow("SELECT paragraph FROM govim_ai_versions WHERE uuid=? AND version=1", id).Scan(&original); err != nil {
		t.Fatal(err)
	}
	if err := reopened.db.QueryRow("SELECT text FROM govim_ai_events WHERE uuid=? AND kind='discarded'", id).Scan(&discarded); err != nil {
		t.Fatal(err)
	}
	if original != "First paragraph." || discarded != "Author edited paragraph." || calls.Load() != 2 {
		t.Fatal("history was overwritten or generation was replayed")
	}
}

func TestDiscardSuppressesLateCompletionAndCloseRecordsUnknown(t *testing.T) {
	for _, discard := range []bool{false, true} {
		name := "close"
		if discard {
			name = "discard"
		}
		t.Run(name, func(t *testing.T) {
			started := make(chan struct{})
			generator := generateFunc(func(ctx context.Context, _ string, _ *Model) (string, error) {
				close(started)
				<-ctx.Done()
				return "Late answer.", nil
			})
			path := filepath.Join(t.TempDir(), "project.db")
			h, err := Open(path, generator)
			if err != nil {
				t.Fatal(err)
			}
			defer h.Close()
			id := UUID()
			r, err := h.Submit(Request{UUID: id, Document: "paper.md", CurrentDocument: BlockText(id, Slot)})
			if err != nil {
				t.Fatal(err)
			}
			select {
			case <-started:
			case <-time.After(5 * time.Second):
				t.Fatal("request didn't start")
			}
			if discard {
				if err := h.RecordAction(r, "discarded", Slot); err != nil {
					t.Fatal(err)
				}
			}
			if err := h.Close(); err != nil {
				t.Fatal(err)
			}
			h, err = Open(path, nil)
			if err != nil {
				t.Fatal(err)
			}
			defer h.Close()
			r, err = h.Latest(id)
			want := "unknown"
			if discard {
				want = "discarded"
			}
			if err != nil || r.Status != want || r.Paragraph != "" {
				t.Fatalf("late result survived cancellation: %+v %v", r, err)
			}
		})
	}
}
