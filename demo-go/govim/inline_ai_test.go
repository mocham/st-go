package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"st-go/aiharness"
)

func isolateInlineAI(t *testing.T) {
	t.Helper()
	// Never read the developer's real OpenCode registration or server password.
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	t.Setenv("OPENCODE_PASSWORD", "")
	t.Setenv("OPENCODE_TUI_AUTHORIZATION", "")
	t.Setenv("OPENCODE_TUI_PASSWORD", "")
	t.Setenv("OPENCODE_TUI_USERNAME", "")
}

func inlineAPIDiscovery(w http.ResponseWriter, r *http.Request) bool {
	if r.URL.Path != "/openapi.json" || r.Method != "GET" {
		return false
	}
	w.Write([]byte(`{"paths":{"/api/experimental/generate/text":{"post":{}}}}`))
	return true
}

func inlineTargetIDs(t *testing.T, prompt string) []string {
	t.Helper()
	_, tail, ok := strings.Cut(prompt, "Target placeholder UUIDs (document order):\n")
	if !ok {
		t.Error("missing batch target list")
		return nil
	}
	var ids []string
	if err := json.NewDecoder(strings.NewReader(tail)).Decode(&ids); err != nil {
		t.Error(err)
	}
	return ids
}

func inlineTestReply(t *testing.T, w http.ResponseWriter, prompt, paragraph string) {
	t.Helper()
	values := make(map[string]string)
	for _, id := range inlineTargetIDs(t, prompt) {
		values[id] = paragraph
	}
	text, _ := json.Marshal(values)
	json.NewEncoder(w).Encode(map[string]any{"data": map[string]string{"text": string(text)}})
}

func waitInline(t *testing.T, e *Editor, id string) aiharness.Record {
	t.Helper()
	h, _, err := e.inlineProject(e.buffer())
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		e.inlineTick()
		r, err := h.Latest(id)
		if err == nil && r.Status == "ready" {
			return r
		}
		if err == nil && (r.Status == "failed" || r.Status == "unknown") {
			t.Fatalf("generation failed: %+v", r)
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("AI did not become ready: %s", e.message)
	return aiharness.Record{}
}

func TestInlineAIIMAPAcceptUninsertAndVersions(t *testing.T) {
	isolateInlineAI(t)
	t.Setenv("ST_GO_APPBUS_TOKEN", "")
	initial := "%project=history/project.db\n%context=context.txt\n\nOpening paragraph.\n\n"
	e, _, _ := newPreviewTestEditor(t, "paper.md", initial)
	e.preview.options.web = true
	e.preview.render = renderPreview
	settlePreview(t, e)
	path := e.buffer().meta.Path
	if err := os.WriteFile(filepath.Join(filepath.Dir(path), "context.txt"), []byte("EXPLICIT_NOTES"), 0600); err != nil {
		t.Fatal(err)
	}
	var calls atomic.Int32
	release := make(chan struct{})
	defer func() {
		select {
		case <-release:
		default:
			close(release)
		}
	}()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if inlineAPIDiscovery(w, r) {
			return
		}
		if r.URL.Path != "/api/experimental/generate/text" {
			t.Errorf("session route used: %s", r.URL.Path)
			http.NotFound(w, r)
			return
		}
		var body struct {
			Prompt string `json:"prompt"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
			return
		}
		if !strings.Contains(body.Prompt, "EXPLICIT_NOTES") || !strings.Contains(body.Prompt, "Opening paragraph.") {
			t.Errorf("missing explicit context: %s", body.Prompt)
		}
		n := calls.Add(1)
		if n == 1 {
			select {
			case <-release:
			case <-r.Context().Done():
				return
			}
		}
		text := "Generated paragraph with 文."
		if n > 1 {
			text = "Revised paragraph."
		}
		inlineTestReply(t, w, body.Prompt, text)
	}))
	t.Cleanup(server.Close)
	e.inlineOptions.server = server.URL
	editKeys(e, "GA{{{")
	blocks := aiharness.Blocks(string(e.buffer().text))
	if len(blocks) != 1 {
		t.Fatalf("IMAP didn't create a UUID block: %q · %s", e.buffer().text, e.message)
	}
	id := blocks[0].UUID
	if calls.Load() != 0 || e.inlineAI != nil {
		t.Fatal("slot creation requested generation")
	}
	e.setPos(0)
	editKeys(e, "<C-j>")
	if calls.Load() != 0 || !strings.Contains(e.message, "Initial") {
		t.Fatal("Ctrl-J requested an Initial slot")
	}
	e.execute("AI")
	if e.inlineAI == nil {
		t.Fatal("AI command did not initialize the paragraph harness")
	}
	e.setPos(0)
	editKeys(e, "<C-j>")
	if !strings.Contains(string(e.buffer().text), aiharness.Slot) || !strings.Contains(e.message, "generating") {
		t.Fatalf("pending slot consumed: %s", e.message)
	}
	// Edits made while the request runs must neither change its snapshot nor its anchor.
	e.replace(0, 0, "Unrelated Unicode 文字.\n", 0)
	close(release)
	first := waitInline(t, e, id)
	if strings.Contains(first.CurrentDocument, "Unrelated Unicode") {
		t.Fatal("request followed a live buffer")
	}
	e.setPos(0)
	editKeys(e, "<C-j>")
	if !strings.Contains(string(e.buffer().text), first.Paragraph) || strings.Contains(string(e.buffer().text), aiharness.Slot) {
		t.Fatalf("acceptance failed: %s", e.message)
	}
	if e.preview.pending == nil || !e.preview.pending.immediate || !e.preview.pending.cursorOnly {
		t.Fatal("Ctrl-J did not trigger toggle!")
	}
	settlePreview(t, e)
	for _, hidden := range []string{id, "{{{", "}}}", "%project=", "%context="} {
		if strings.Contains(e.preview.source.Source, hidden) {
			t.Fatalf("bookkeeping leaked into preview: %q", e.preview.source.Source)
		}
	}
	if data, err := os.ReadFile(path); err != nil || string(data) != initial {
		t.Fatal("acceptance/preview saved the document")
	}
	editKeys(e, "<Esc>u")
	if !strings.Contains(string(e.buffer().text), aiharness.Slot) || !strings.Contains(string(e.buffer().text), "Unrelated Unicode") {
		t.Fatal("acceptance wasn't one undo operation")
	}
	e.setPos(0)
	editKeys(e, "<C-j><Esc>")
	if calls.Load() != 1 {
		t.Fatal("undo caused generation to be replayed")
	}
	e.execute("UnIns")
	if !strings.Contains(string(e.buffer().text), aiharness.Slot) || strings.Contains(string(e.buffer().text), first.Paragraph) {
		t.Fatalf("UnIns failed: %s", e.message)
	}
	editKeys(e, "<C-j>")
	if calls.Load() != 1 {
		t.Fatal("discarded slot was regenerated by Ctrl-J")
	}
	e.execute("AI")
	second := waitInline(t, e, id)
	if second.Version != 2 || second.Paragraph != "Revised paragraph." {
		t.Fatalf("new dictionary version missing: %+v", second)
	}
	// Reload the dictionary without any configured generator or terminal channel.
	if err := e.closeInlineAI(); err != nil {
		t.Fatal(err)
	}
	e.inlineOptions.server = ""
	e.setPos(0)
	editKeys(e, "<C-j><Esc>")
	if !strings.Contains(string(e.buffer().text), second.Paragraph) || calls.Load() != 2 {
		t.Fatalf("saved paragraph not recovered: %s", e.message)
	}
}

func TestInlineAIBypassesLiteralInputAndProtectsDuplicateAnchors(t *testing.T) {
	for _, mode := range []string{"paste", "register", "quoted", "other-filetype"} {
		t.Run(mode, func(t *testing.T) {
			e := memoryEditor("")
			e.buffer().meta.Path = filepath.Join(t.TempDir(), "paper.tex")
			defer e.close(false)
			switch mode {
			case "paste":
				e.Feed(keyEvent{Key: "<Paste>", Paste: "{{{"})
			case "register":
				e.registers['a'] = register{Text: "{{{"}
				editKeys(e, "i<C-r>a")
			case "quoted":
				editKeys(e, "i{{<C-v>{")
			case "other-filetype":
				e.buffer().meta.Path += ".go"
				editKeys(e, "i{{{")
			}
			if string(e.buffer().text) != "{{{" || e.inlineAI != nil {
				t.Fatal("literal input dispatched AI")
			}
		})
	}
	id := aiharness.UUID()
	e := memoryEditor(aiharness.BlockText(id, aiharness.Slot) + "\n" + aiharness.BlockText(id, aiharness.Slot))
	e.buffer().meta.Path = filepath.Join(t.TempDir(), "paper.md")
	defer e.close(false)
	before := string(e.buffer().text)
	editKeys(e, "<C-j>")
	if string(e.buffer().text) != before || !strings.Contains(e.message, "duplicated") {
		t.Fatal("duplicate anchor was consumed")
	}
	e.execute("UnIns " + id)
	if string(e.buffer().text) != before {
		t.Fatal("UnIns changed duplicate blocks")
	}
}

func TestPTYInlineAIWithoutTerminalBus(t *testing.T) {
	testPTYInlineAIAuthentication(t, false)
}

func TestPTYInlineAIInheritedServerAuthentication(t *testing.T) {
	testPTYInlineAIAuthentication(t, true)
}

func testPTYInlineAIAuthentication(t *testing.T, inherited bool) {
	t.Helper()
	isolateInlineAI(t)
	// Synthetic local service; this test never uses a provider account.
	const servicePassword = "test-only-pty-password"
	t.Setenv("ST_GO_APPBUS_TOKEN", "")
	t.Setenv("GOVIM_AI_SERVER", "")
	t.Setenv("GOVIM_AI_MODEL", "")
	t.Setenv("GOVIM_AI_AUTHORIZATION", "")
	t.Setenv("OPENCODE_PASSWORD", "")
	root := t.TempDir()
	t.Setenv("XDG_STATE_HOME", root)
	path := filepath.Join(root, "paper.md")
	if err := os.WriteFile(path, []byte("An opening paragraph.\n"), 0600); err != nil {
		t.Fatal(err)
	}
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user, password, ok := r.BasicAuth()
		if !ok || user != "opencode" || password != servicePassword {
			t.Error("editor did not authenticate to the local service")
			w.WriteHeader(401)
			return
		}
		if r.Method == "GET" && r.URL.Path == "/openapi.json" {
			w.Write([]byte(`{"paths":{"/api/experimental/generate":{"post":{}}}}`))
			return
		}
		if r.URL.Path != "/api/experimental/generate" {
			http.NotFound(w, r)
			return
		}
		calls.Add(1)
		var body struct{ Prompt string }
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
			return
		}
		inlineTestReply(t, w, body.Prompt, "PTY generated paragraph.")
	}))
	defer server.Close()
	args := []string{"--no-preview"}
	if inherited {
		t.Setenv("OPENCODE_TUI_PASSWORD", servicePassword)
		args = append(args, "--ai-server", server.URL)
	} else {
		if err := os.MkdirAll(filepath.Join(root, "opencode"), 0700); err != nil {
			t.Fatal(err)
		}
		registration, _ := json.Marshal(map[string]string{"url": server.URL, "password": servicePassword})
		if err := os.WriteFile(filepath.Join(root, "opencode", "service.json"), registration, 0600); err != nil {
			t.Fatal(err)
		}
	}
	p := startPTYEditor(t, append(args, path)...)
	p.await(t, "editor", func() bool { return strings.Contains(p.screenText(), "An opening paragraph.") })
	p.send(t, "A {{{\x1b")
	p.await(t, "Initial slot", func() bool { return strings.Contains(p.screenText(), "Initial") })
	if calls.Load() != 0 {
		t.Fatal("IMAP sent a request")
	}
	p.send(t, ":AI\r")
	p.await(t, "paragraph ready", func() bool { return strings.Contains(p.screenText(), "v1 ready") })
	p.send(t, "\n\x1b")
	p.await(t, "inserted paragraph", func() bool { return strings.Contains(p.screenText(), "PTY generated paragraph.") })
	p.send(t, ":UnIns\r")
	p.await(t, "restored slot", func() bool {
		return strings.Contains(p.screenText(), "discarded") && strings.Contains(p.screenText(), "<++>")
	})
	p.send(t, ":wq\r")
	select {
	case <-p.done:
	case <-time.After(5 * time.Second):
		t.Fatal("editor did not exit")
	}
	if p.err != nil {
		t.Fatal(p.err)
	}
	data, err := os.ReadFile(path)
	if err != nil || len(aiharness.Blocks(string(data))) != 1 || strings.Contains(string(data), "PTY generated") || calls.Load() != 1 {
		t.Fatalf("PTY result: %q %v", data, err)
	}
	if _, err := os.Stat(path + ".db"); err != nil {
		t.Fatal("default project DB missing:", err)
	}
}
