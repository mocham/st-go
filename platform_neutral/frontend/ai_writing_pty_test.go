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
	if err := os.WriteFile(document, []byte("Opening paragraph.\n\nFollowing paragraph.\n"), 0600); err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	messageID, prediction := "", ""
	prompts := 0
	session := func(id string) map[string]any {
		return map[string]any{"id": id, "title": "Writing test", "location": map[string]string{"directory": root}}
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/event" {
			w.Header().Set("Content-Type", "text/event-stream")
			io.WriteString(w, "event: server.connected\ndata: {}\n\n")
			w.(http.Flusher).Flush()
			<-r.Context().Done()
			return
		}
		mu.Lock()
		defer mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		switch r.Method + " " + r.URL.Path {
		case "GET /api/info":
			io.WriteString(w, `{"version":"2.test"}`)
		case "GET /api/session/ses_parent":
			json.NewEncoder(w).Encode(map[string]any{"data": session("ses_parent")})
		case "GET /api/session/ses_writing", "POST /api/session/ses_parent/fork":
			json.NewEncoder(w).Encode(map[string]any{"data": session("ses_writing")})
		case "GET /api/session":
			json.NewEncoder(w).Encode(map[string]any{"data": []any{session("ses_parent"), session("ses_writing")}})
		case "GET /api/session/active":
			io.WriteString(w, `{"data":{}}`)
		case "GET /api/model/default":
			io.WriteString(w, `{"data":null}`)
		case "POST /api/session/ses_writing/prompt":
			var body map[string]string
			if json.NewDecoder(r.Body).Decode(&body) != nil {
				http.Error(w, "body", 400)
				return
			}
			messageID = body["id"]
			prompts++
			begin, end := writing.Markers(strings.TrimPrefix(messageID, "msg_"))
			prediction = "Editorial commentary.\n" + begin + "\nA generated next paragraph.\n" + end
			if !strings.Contains(body["text"], "Opening paragraph.") || !strings.Contains(body["text"], "Following paragraph.") {
				http.Error(w, "missing source context", 400)
				return
			}
			io.WriteString(w, `{"data":{"id":"queued"}}`)
		case "GET /api/session/ses_writing/message":
			var messages []any
			if messageID != "" {
				messages = []any{map[string]any{"id": "idle", "type": "idle", "outcome": "succeeded"}, map[string]any{"id": "answer", "type": "assistant", "content": []any{map[string]string{"type": "reasoning", "text": "Visible provider reasoning."}, map[string]string{"type": "text", "text": prediction}}}, map[string]string{"id": messageID, "type": "user", "text": "Writing request"}}
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
	start := func(command []string) (*os.File, func() string) {
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
		child, err := ptyutil.Start(slave, command, env)
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
		t.Cleanup(func() { child.Process.Kill(); master.Close(); <-done; child.Wait() })
		return master, func() string {
			terminal.mu.Lock()
			defer terminal.mu.Unlock()
			var lines []string
			for row := 0; row < terminal.termCore.Rows(); row++ {
				lines = append(lines, terminal.termCore.LineText(row))
			}
			return strings.Join(lines, "\n")
		}
	}
	_, providerScreen := start([]string{binaries["opencode-tui"], "--server", server.URL, "--session", "ses_parent", "--ai-project", root, "--no-preview", root})
	editor, editorScreen := start([]string{binaries["govim"], "--no-preview", "--ai-project", root, document})
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
	fmt.Fprint(editor, ":AINext Continue in the same style\r")
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
	if strings.Contains(string(data), "govim-ai:") || strings.Contains(string(data), "commentary") || strings.Contains(string(data), "reasoning") {
		t.Fatalf("nonprediction inserted: %s", data)
	}
	mu.Lock()
	count := prompts
	mu.Unlock()
	if count != 1 {
		t.Fatalf("generation submitted %d times", count)
	}
}
