package frontend

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"st-go/ptyutil"
	"st-go/tui/appbus"
	"st-go/tui/writing"
)

func TestAIWritingRealClientsPTY(t *testing.T) {
	root := t.TempDir()
	document := filepath.Join(root, "paper.md")
	if err := os.WriteFile(document, []byte("# Excluded earlier heading\n\nOpening paragraph.\n\nFollowing paragraph.\n"), 0600); err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	messageID, prediction, promptText := "", "", ""
	prompts, forks, created := 0, 0, 0
	completed := false
	model := map[string]string{"id": "model-a", "providerID": "test"}
	events := make(chan map[string]any, 32)
	session := func(id string) map[string]any {
		title := "Existing session"
		if id == "ses_new" {
			title = "New session"
		}
		return map[string]any{"id": id, "title": title, "model": model, "location": map[string]string{"directory": root}}
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/event" {
			w.Header().Set("Content-Type", "text/event-stream")
			io.WriteString(w, "data: {\"type\":\"server.connected\",\"data\":{}}\n\n")
			w.(http.Flusher).Flush()
			for {
				select {
				case event := <-events:
					data, _ := json.Marshal(event)
					fmt.Fprintf(w, "data: %s\n\n", data)
					w.(http.Flusher).Flush()
				case <-r.Context().Done():
					return
				}
			}
		}
		mu.Lock()
		defer mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		switch r.Method + " " + r.URL.Path {
		case "GET /api/info":
			io.WriteString(w, `{"version":"2.test"}`)
		case "POST /api/session":
			created++
			json.NewEncoder(w).Encode(map[string]any{"data": session("ses_new")})
		case "GET /api/session/ses_parent", "GET /api/session/ses_new":
			json.NewEncoder(w).Encode(map[string]any{"data": session(strings.TrimPrefix(r.URL.Path, "/api/session/"))})
		case "POST /api/session/ses_parent/fork":
			forks++
			http.Error(w, "must use the subscribed existing session", 400)
		case "GET /api/session":
			json.NewEncoder(w).Encode(map[string]any{"data": []any{session("ses_new"), session("ses_parent")}})
		case "GET /api/session/active":
			if messageID != "" && !completed {
				io.WriteString(w, `{"data":{"ses_parent":{}}}`)
			} else {
				io.WriteString(w, `{"data":{}}`)
			}
		case "GET /api/model":
			io.WriteString(w, `{"data":[{"id":"model-a","providerID":"test","name":"Model A","enabled":true},{"id":"model-b","providerID":"test","name":"Model B","enabled":true,"variants":[{"id":"deep"}]}]}`)
		case "GET /api/model/default":
			io.WriteString(w, `{"data":null}`)
		case "POST /api/session/ses_parent/model":
			var body struct {
				Model map[string]string `json:"model"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				http.Error(w, err.Error(), 400)
				return
			}
			model = body.Model
			io.WriteString(w, `{}`)
		case "POST /api/session/ses_parent/prompt":
			var body map[string]string
			if json.NewDecoder(r.Body).Decode(&body) != nil {
				http.Error(w, "body", 400)
				return
			}
			messageID = body["id"]
			promptText = body["text"]
			prompts++
			begin, end := writing.Markers(strings.TrimPrefix(messageID, "msg_"))
			prediction = "Editorial commentary.\n" + begin + "\nA generated next paragraph.\n" + end
			if !strings.Contains(body["text"], "Opening paragraph.") || strings.Contains(body["text"], "Following paragraph.") || strings.Contains(body["text"], "Excluded earlier heading") || strings.Contains(body["text"], writing.Checkpoint("paper.md")) {
				http.Error(w, "incorrect checkpoint-to-cursor context", 400)
				return
			}
			emit := func(kind string, fields map[string]any) {
				fields["sessionID"] = "ses_parent"
				events <- map[string]any{"type": kind, "data": fields}
			}
			emit("session.inbox.enqueued", map[string]any{"inboxID": messageID, "item": map[string]any{"type": "user", "payload": map[string]string{"text": body["text"]}}})
			emit("session.inbox.delivered", map[string]any{"inboxID": messageID})
			emit("session.execution.started", map[string]any{})
			emit("session.reasoning.delta", map[string]any{"assistantMessageID": "answer", "ordinal": 0, "delta": "Visible provider reasoning."})
			emit("session.text.delta", map[string]any{"assistantMessageID": "answer", "ordinal": 1, "delta": "Generating the next paragraph..."})
			io.WriteString(w, `{"data":{"id":"queued"}}`)
		case "POST /api/session/ses_new/prompt":
			t.Error("writing prompt sent to startup session instead of clicked session")
			http.Error(w, "wrong session", 400)
		case "GET /api/session/ses_parent/message":
			var messages []any
			if messageID != "" {
				text := "Generating the next paragraph..."
				if completed {
					text = prediction
					messages = append(messages, map[string]any{"id": "idle", "type": "idle", "outcome": "succeeded"})
				}
				messages = append(messages, map[string]any{"id": "answer", "type": "assistant", "content": []any{map[string]string{"type": "reasoning", "text": "Visible provider reasoning."}, map[string]string{"type": "text", "text": text}}}, map[string]string{"id": messageID, "type": "user", "text": promptText})
			}
			json.NewEncoder(w).Encode(map[string]any{"data": messages})
		default:
			io.WriteString(w, `{"data":[]}`)
		}
	}))
	t.Cleanup(server.Close)
	binaries := make(map[string]string)
	for _, name := range []string{"govim", "opencode-tui"} {
		binary := filepath.Join(t.TempDir(), name)
		command := exec.Command("go", "-C", "demo-go/"+name, "build", "-o", binary, ".")
		command.Env = append(os.Environ(), "CGO_ENABLED=0")
		if output, err := command.CombinedOutput(); err != nil {
			t.Fatalf("build %s: %v %s", name, err, output)
		}
		binaries[name] = binary
	}
	broker := newAppBroker()
	start := func(command []string) (*os.File, func() string, func()) {
		terminal := regionTestTerminal(t)
		terminal.appBroker = broker
		terminal.cfg.AppBusToken = appbus.ID()
		terminal.cols, terminal.rows = 80, 24
		terminal.termCore.Tresize(80, 24)
		master, slave, err := ptyutil.Open()
		if err != nil {
			t.Fatal(err)
		}
		if err := ptyutil.SetWinSizePixels(master, 24, 80, 320, 192); err != nil {
			t.Fatal(err)
		}
		env := append(envWithout(os.Environ(), "ST_GO_APPBUS_TOKEN", "OPENCODE_TUI_AUTHORIZATION", "OPENCODE_TUI_PASSWORD", "OPENCODE_TUI_USERNAME"), "ST_GO_APPBUS_TOKEN="+terminal.cfg.AppBusToken)
		terminal.termCore.SetWriter(func(data []byte) { master.Write(data) })
		child, err := ptyutil.StartDir(slave, command, env, root)
		slave.Close()
		if err != nil {
			t.Fatal(err)
		}
		done := make(chan struct{})
		go func() {
			defer close(done)
			var pending []byte
			buffer := make([]byte, 8192)
			for {
				n, err := master.Read(buffer)
				if n > 0 {
					pending = append(pending, buffer[:n]...)
					consumed := terminal.writeTerminalOutput(terminal.termCore, pending)
					pending = pending[consumed:]
				}
				if err != nil {
					return
				}
			}
		}()
		var once sync.Once
		stop := func() { once.Do(func() { child.Process.Kill(); master.Close(); <-done; child.Wait() }) }
		t.Cleanup(stop)
		return master, func() string {
			terminal.mu.Lock()
			defer terminal.mu.Unlock()
			var lines []string
			for row := 0; row < terminal.termCore.Rows(); row++ {
				lines = append(lines, terminal.termCore.LineText(row))
			}
			return strings.Join(lines, "\n")
		}, stop
	}
	providerCommand := []string{binaries["opencode-tui"], "--server", server.URL, "--no-preview"}
	provider, providerScreen, stopProvider := start(append(providerCommand, root))
	editor, editorScreen, _ := start([]string{binaries["govim"], "--no-preview", document})
	await := func(label string, predicate func() bool) {
		t.Helper()
		deadline := time.Now().Add(15 * time.Second)
		for time.Now().Before(deadline) {
			if predicate() {
				return
			}
			time.Sleep(20 * time.Millisecond)
		}
		t.Fatalf("%s\neditor=%s\nprovider=%s", label, editorScreen(), providerScreen())
	}
	await("editor ready", func() bool { return strings.Contains(editorScreen(), "Opening paragraph.") })
	await("provider ready", func() bool { return strings.Contains(providerScreen(), "New session") })
	fmt.Fprint(editor, ":Subscribe paper\r")
	await("waiting for explicit subscription", func() bool { return strings.Contains(editorScreen(), "waiting for /Subscribe paper") })
	fmt.Fprint(editor, ":AINext\r")
	await("unconnected request refused", func() bool { return strings.Contains(editorScreen(), "Error: AI [paper] waiting") })
	if strings.Contains(editorScreen(), "govim-ai:") {
		t.Fatal("unconnected request inserted a placeholder")
	}
	fmt.Fprint(provider, "\x1b[6~") // Reveal the existing session below the startup session.
	await("existing sidebar session", func() bool { return strings.Contains(providerScreen(), "Existing session") })
	selectedRow := 0
	for index, line := range strings.Split(providerScreen(), "\n") {
		if strings.Contains(line, "Existing session") {
			selectedRow = index + 1
			break
		}
	}
	if selectedRow == 0 {
		t.Fatal("existing session header disappeared")
	}
	// Send both in the same read: /Subscribe must wait for the clicked session
	// to load instead of being discarded or binding to the startup session.
	fmt.Fprintf(provider, "\x1b[<0;3;%dM\x1b[<0;3;%dm/Subscribe paper\r", selectedRow, selectedRow)
	await("channel connected", func() bool { return strings.Contains(editorScreen(), "AI [paper] connected") })
	await("subscription bound to sidebar selection", func() bool { return strings.Contains(providerScreen(), "Subscribed session: ses_parent") })
	if _, err := os.Stat(filepath.Join(root, ".govim", "ai.sqlite3")); err != nil {
		t.Fatal(err)
	}
	fmt.Fprint(editor, "jj:AICheckpoint\rj:AINext Continue in the same style\r")
	await("live progress in subscribed session", func() bool {
		text := providerScreen()
		return strings.Contains(text, "Visible provider reasoning.") && strings.Contains(text, "Generating the next paragraph")
	})
	fmt.Fprint(provider, "\x1bOS")
	await("long writing prompt expanded", func() bool { return strings.Contains(providerScreen(), "Fold prompt") })
	fmt.Fprint(provider, "\x1b[14~")
	await("long writing prompt folded", func() bool {
		text := providerScreen()
		return strings.Contains(text, "Show more") && strings.Contains(text, "Visible provider reasoning.")
	})
	fmt.Fprint(provider, "/models Model B\r")
	await("model picker", func() bool {
		return strings.Contains(providerScreen(), "Model B") && strings.Contains(providerScreen(), "1/1")
	})
	fmt.Fprint(provider, "\r")
	await("model changed on existing session", func() bool {
		mu.Lock()
		defer mu.Unlock()
		return model["id"] == "model-b"
	})
	await("model picker applied", func() bool { return strings.Contains(providerScreen(), "Model selected: test/model-b") })
	fmt.Fprint(provider, "/variants deep\r")
	await("variant picker", func() bool {
		return strings.Contains(providerScreen(), "deep") && strings.Contains(providerScreen(), "1/1")
	})
	fmt.Fprint(provider, "\r")
	await("variant changed on existing session", func() bool {
		mu.Lock()
		defer mu.Unlock()
		return model["variant"] == "deep"
	})
	stopProvider() // Simulate a client crash; the ordinary server session survives.
	provider, providerScreen, _ = start(append(providerCommand, "--session", "ses_parent", root))
	await("reopened existing session", func() bool { return strings.Contains(providerScreen(), "Existing session") })
	fmt.Fprint(provider, "/subscribe paper\r")
	await("resubscribed existing session", func() bool { return strings.Contains(providerScreen(), "paper.md") })
	mu.Lock()
	completed = true
	mu.Unlock()
	await("prediction ready", func() bool { return strings.Contains(editorScreen(), "AI paragraph ready") })
	fmt.Fprint(editor, ":AIIns\r:w\r")
	await("saved generated paragraph", func() bool {
		data, err := os.ReadFile(document)
		return err == nil && strings.Contains(string(data), "A generated next paragraph.")
	})
	data, err := os.ReadFile(document)
	if err != nil {
		t.Fatal(err)
	}
	withoutCheckpoint := strings.ReplaceAll(string(data), writing.Checkpoint("paper.md"), "")
	if strings.Contains(withoutCheckpoint, "govim-ai:") || strings.Contains(string(data), "commentary") || strings.Contains(string(data), "reasoning") {
		t.Fatalf("nonprediction inserted: %s", data)
	}
	mu.Lock()
	count, forkCount, createCount := prompts, forks, created
	mu.Unlock()
	if count != 1 || forkCount != 0 || createCount != 1 {
		t.Fatalf("generation submitted %d times; forked %d sessions; created %d sessions (only startup should create one)", count, forkCount, createCount)
	}
}
