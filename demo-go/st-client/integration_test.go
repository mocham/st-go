package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"golang.org/x/net/websocket"
	"golang.org/x/sys/unix"
)

type capturedOutput struct {
	mu     sync.Mutex
	buffer bytes.Buffer
	wake   chan struct{}
}

func capture() *capturedOutput { return &capturedOutput{wake: make(chan struct{}, 1)} }
func (o *capturedOutput) Write(data []byte) (int, error) {
	o.mu.Lock()
	n, err := o.buffer.Write(data)
	o.mu.Unlock()
	select {
	case o.wake <- struct{}{}:
	default:
	}
	return n, err
}
func (o *capturedOutput) text() string { o.mu.Lock(); defer o.mu.Unlock(); return o.buffer.String() }
func (o *capturedOutput) await(t *testing.T, predicate func(string) bool) string {
	t.Helper()
	timer := time.NewTimer(10 * time.Second)
	defer timer.Stop()
	for {
		text := o.text()
		if predicate(text) {
			return text
		}
		select {
		case <-o.wake:
		case <-timer.C:
			t.Fatalf("output timeout: %q", text)
		}
	}
}

func openPTY(t *testing.T, cols, rows uint16) (*os.File, *os.File) {
	t.Helper()
	master, err := os.OpenFile("/dev/ptmx", os.O_RDWR|syscall.O_NOCTTY, 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { master.Close() })
	if err := unix.IoctlSetPointerInt(int(master.Fd()), unix.TIOCSPTLCK, 0); err != nil {
		t.Fatal(err)
	}
	n, err := unix.IoctlGetInt(int(master.Fd()), unix.TIOCGPTN)
	if err != nil {
		t.Fatal(err)
	}
	slave, err := os.OpenFile(fmt.Sprintf("/dev/pts/%d", n), os.O_RDWR|syscall.O_NOCTTY, 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { slave.Close() })
	if err := unix.IoctlSetWinsize(int(slave.Fd()), unix.TIOCSWINSZ, &unix.Winsize{Col: cols, Row: rows}); err != nil {
		t.Fatal(err)
	}
	return master, slave
}

func awaitInput(t *testing.T, messages <-chan inputMessage, predicate func(inputMessage) bool) inputMessage {
	t.Helper()
	timer := time.NewTimer(10 * time.Second)
	defer timer.Stop()
	for {
		select {
		case m := <-messages:
			if predicate(m) {
				return m
			}
		case <-timer.C:
			t.Fatal("timed out waiting for client wire input")
		}
	}
}

func TestPTYProtocolReconnectResizeAndCleanup(t *testing.T) {
	t.Setenv("ST_CLIENT_AUTHORIZATION", "Bearer test")
	t.Setenv("TMPDIR", t.TempDir())
	var attachments atomic.Int32
	messages := make(chan inputMessage, 256)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer test" {
			http.Error(w, "missing authorization", 401)
			return
		}
		switch r.URL.Path {
		case "/remote/api/ptys":
			json.NewEncoder(w).Encode([]ptyInfo{{ID: "one", Title: "Example", View: "/remote/api/ptys/one/view/"}})
		case "/remote/api/ptys/one/attach":
			websocket.Server{Handshake: func(_ *websocket.Config, req *http.Request) error {
				if req.Header.Get("Origin") != "http://"+req.Host {
					return fmt.Errorf("wrong origin")
				}
				return nil
			}, Handler: func(ws *websocket.Conn) {
				defer ws.Close()
				attachment := attachments.Add(1)
				_ = websocket.JSON.Send(ws, map[string]any{"type": "hello", "cellWidth": 8, "cellHeight": 16, "border": 2, "heartbeatMS": 250})
				for {
					var m inputMessage
					if err := websocket.JSON.Receive(ws, &m); err != nil {
						return
					}
					messages <- m
					switch m.Type {
					case "resize":
						_ = websocket.JSON.Send(ws, map[string]any{"type": "scene", "width": m.Width, "height": m.Height, "scenes": []any{map[string]any{"id": 1, "width": m.Width, "height": m.Height, "update": map[string]any{"reset": true, "rows": []any{map[string]any{"x": 2, "y": 2, "height": 16, "text": []any{map[string]any{"x": 2, "text": fmt.Sprintf("REMOTE_%d", attachment), "advance": 8, "fg": 4294967295}}}}}}}})
						_ = websocket.JSON.Send(ws, map[string]any{"type": "paint", "id": attachment})
					case "ping":
						_ = websocket.JSON.Send(ws, map[string]any{"type": "pong"})
					case "key":
						if m.Key == "x" {
							return
						}
					}
				}
			}}.ServeHTTP(w, r)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	master, slave := openPTY(t, 32, 10)
	before, err := unix.IoctlGetTermios(int(slave.Fd()), unix.TCGETS)
	if err != nil {
		t.Fatal(err)
	}
	out, stderr := capture(), capture()
	done := make(chan int, 1)
	go func() { done <- run([]string{server.URL + "/remote/"}, slave, out, stderr) }()
	t.Cleanup(func() { master.Write([]byte("\x1dq")) })
	awaitInput(t, messages, func(m inputMessage) bool { return m.Type == "paint_ack" && m.ID == 1 })
	out.await(t, func(s string) bool { return strings.Contains(s, "REMOTE_1") })
	master.Write([]byte("\x1b[1;5A\x1b[<0;4;3M\x1b[<0;4;3m\x1b[200~hello\nworld\x1b[201~"))
	awaitInput(t, messages, func(m inputMessage) bool { return m.Key == "ArrowUp" && m.Modifiers == 4 })
	awaitInput(t, messages, func(m inputMessage) bool { return m.Type == "press" && m.X == 30 && m.Y == 42 })
	awaitInput(t, messages, func(m inputMessage) bool { return m.Type == "paste" && m.Text == "hello\nworld" })
	awaitInput(t, messages, func(m inputMessage) bool { return m.Type == "ping" })
	master.Write([]byte("x"))
	awaitInput(t, messages, func(m inputMessage) bool { return m.Type == "paint_ack" && m.ID == 2 })
	out.await(t, func(s string) bool { return strings.Contains(s, "REMOTE_2") })
	if err := unix.IoctlSetWinsize(int(slave.Fd()), unix.TIOCSWINSZ, &unix.Winsize{Col: 40, Row: 12}); err != nil {
		t.Fatal(err)
	}
	if err := syscall.Kill(os.Getpid(), syscall.SIGWINCH); err != nil {
		t.Fatal(err)
	}
	awaitInput(t, messages, func(m inputMessage) bool { return m.Type == "resize" && m.Width == 324 && m.Height == 164 })
	master.Write([]byte("\x1dq"))
	select {
	case status := <-done:
		if status != 0 {
			t.Fatalf("client exit %d: %s", status, stderr.text())
		}
	case <-time.After(5 * time.Second):
		t.Fatal("detach blocked")
	}
	after, err := unix.IoctlGetTermios(int(slave.Fd()), unix.TCGETS)
	if err != nil || *before != *after {
		t.Fatalf("termios not restored: %v", err)
	}
	if !strings.Contains(out.text(), "\x1b[?1049l") {
		t.Fatal("alternate screen not restored")
	}
	entries, err := os.ReadDir(os.Getenv("TMPDIR"))
	if err != nil || len(entries) != 0 {
		t.Fatalf("cache files left after detach: %v %v", entries, err)
	}
}

// Set ST_CLIENT_TEST_SERVER to an independently built st-web to exercise the
// real service/terminal/scene stack without importing its graphical backend.
func TestLiveWebService(t *testing.T) {
	binary := os.Getenv("ST_CLIENT_TEST_SERVER")
	if binary == "" {
		t.Skip("set ST_CLIENT_TEST_SERVER to run the real st-web smoke test")
	}
	binary, err := filepath.Abs(binary)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	log := capture()
	cmd := exec.CommandContext(ctx, binary, "-service", "-port", "0", "-base-path", "/nested/remote/", "-dir", t.TempDir(), "-e", "/bin/sh")
	cmd.Stdout, cmd.Stderr = log, log
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { cancel(); _ = cmd.Wait() }()
	re := regexp.MustCompile(`Web terminal service: (http://[^\s]+)`)
	text := log.await(t, func(s string) bool { return re.MatchString(s) })
	address := re.FindStringSubmatch(text)[1]
	var list, stderr bytes.Buffer
	if status := run([]string{"-list", address}, os.Stdin, &list, &stderr); status != 0 || list.Len() == 0 {
		t.Fatalf("list: %d %s", status, stderr.String())
	}
	master, slave := openPTY(t, 80, 24)
	out, errors := capture(), capture()
	done := make(chan int, 1)
	go func() { done <- run([]string{address}, slave, out, errors) }()
	t.Cleanup(func() { master.Write([]byte("\x1dq")) })
	out.await(t, func(s string) bool { return strings.Contains(s, "Connected") })
	master.Write([]byte("printf 'LIVE_%s\\n' 'ROUNDTRIP'\r"))
	out.await(t, func(s string) bool { return strings.Contains(s, "LIVE_ROUNDTRIP") })
	// PTY lifetime is independent from the service UI, both when the shell
	// exits itself and when the user closes it through the attachment token.
	master.Write([]byte("exit\r"))
	text = out.await(t, func(s string) bool { return strings.Contains(s, "No available terminal") })
	for _, command := range []string{"printf 'AFTER_%s\\n' 'EXIT'\r", "printf 'AFTER_%s\\n' 'DELETE'\r"} {
		offset := len(text)
		master.Write([]byte("\x1dn"))
		out.await(t, func(s string) bool { return len(s) >= offset && strings.Contains(s[offset:], "Connected") })
		master.Write([]byte(command))
		marker := "AFTER_EXIT"
		if strings.Contains(command, "DELETE") {
			marker = "AFTER_DELETE"
		}
		out.await(t, func(s string) bool { return strings.Contains(s, marker) })
		if marker == "AFTER_EXIT" {
			offset = len(out.text())
			master.Write([]byte("\x1dx"))
			text = out.await(t, func(s string) bool { return len(s) >= offset && strings.Contains(s[offset:], "No available terminal") })
		}
	}
	master.Write([]byte("\x1dq"))
	select {
	case status := <-done:
		if status != 0 {
			t.Fatalf("client exit %d: %s", status, errors.text())
		}
	case <-time.After(5 * time.Second):
		t.Fatal("detach blocked")
	}
	// Detach keeps the managed PTY alive and attachable.
	r, _ := newRemote(address, "")
	sessions, err := r.sessions(ctx)
	if err != nil || len(sessions) != 1 {
		t.Fatalf("detach lost session: %+v %v", sessions, err)
	}
}
