package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"golang.org/x/net/websocket"
	"golang.org/x/sys/unix"
)

// An isolated stand-in exercises process ownership and Unix forwarding without
// an SSH account, key material, known_hosts edits, or a running SSH daemon.
func TestSSHProcessHelper(t *testing.T) {
	if os.Getenv("ST_CLIENT_SSH_HELPER") != "1" {
		return
	}
	var forward, control string
	for i, arg := range os.Args {
		if arg == "-L" && i+1 < len(os.Args) {
			forward = strings.SplitN(os.Args[i+1], ":", 2)[0]
		}
		if arg == "-S" && i+1 < len(os.Args) {
			control = os.Args[i+1]
		}
	}
	if forward == "" || control == "" {
		os.Exit(2)
	}
	master, err := net.Listen("unix", control)
	if err != nil {
		os.Exit(3)
	}
	defer master.Close()
	listener, err := net.Listen("unix", forward)
	if err != nil {
		os.Exit(4)
	}
	for {
		local, err := listener.Accept()
		if err != nil {
			os.Exit(5)
		}
		go func() {
			defer local.Close()
			remote, err := net.Dial("tcp", os.Getenv("ST_CLIENT_SSH_ENDPOINT"))
			if err != nil {
				return
			}
			defer remote.Close()
			go func() { io.Copy(remote, local); remote.Close() }()
			io.Copy(local, remote)
		}()
	}
}

func fakeSSH(t *testing.T, endpoint string) {
	t.Helper()
	dir := t.TempDir()
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	script := "#!/bin/sh\nexec \"$ST_CLIENT_TEST_BINARY\" -test.run=^TestSSHProcessHelper$ -- \"$@\"\n"
	if err := os.WriteFile(filepath.Join(dir, "ssh"), []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+":"+os.Getenv("PATH"))
	t.Setenv("ST_CLIENT_TEST_BINARY", executable)
	t.Setenv("ST_CLIENT_SSH_HELPER", "1")
	t.Setenv("ST_CLIENT_SSH_ENDPOINT", endpoint)
}

func TestSSHForwardHTTPWebSocketAndRecovery(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Host != "remote.invalid:8080" {
			http.Error(w, "wrong Host", 403)
			return
		}
		switch r.URL.Path {
		case "/api/ptys":
			json.NewEncoder(w).Encode([]ptyInfo{{ID: "one", View: "/api/ptys/one/view/"}})
		case "/api/ptys/one/view/media/1":
			io.WriteString(w, "resource through SSH")
		case "/api/ptys/one/attach":
			websocket.Server{Handshake: func(_ *websocket.Config, request *http.Request) error {
				if request.Header.Get("Origin") != "http://remote.invalid:8080" {
					return fmt.Errorf("wrong Origin")
				}
				return nil
			}, Handler: func(ws *websocket.Conn) {
				defer ws.Close()
				websocket.JSON.Send(ws, serverMessage{Type: "hello"})
				var input inputMessage
				if websocket.JSON.Receive(ws, &input) == nil {
					websocket.JSON.Send(ws, serverMessage{Type: input.Type})
				}
			}}.ServeHTTP(w, r)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	fakeSSH(t, server.Listener.Addr().String())
	r, _ := newRemote("http://remote.invalid:8080/", "")
	tunnel, err := newSSHTunnel("fixture", r)
	if err != nil {
		t.Fatal(err)
	}
	defer tunnel.close()
	defer r.http.CloseIdleConnections()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	list, err := r.sessions(ctx)
	if err != nil || len(list) != 1 {
		t.Fatalf("SSH service list: %v %v", list, err)
	}
	view := *r
	if err := view.selectView(list[0]); err != nil {
		t.Fatal(err)
	}
	c, err := view.connect(ctx, make(chan wireEvent, 8))
	if err != nil {
		t.Fatal(err)
	}
	defer c.close()
	var hello serverMessage
	c.ws.SetReadDeadline(time.Now().Add(time.Second))
	if err := websocket.JSON.Receive(c.ws, &hello); err != nil || hello.Type != "hello" {
		t.Fatalf("SSH WebSocket: %+v %v", hello, err)
	}
	c.send(inputMessage{Type: "ping"})
	if err := websocket.JSON.Receive(c.ws, &hello); err != nil || hello.Type != "ping" {
		t.Fatalf("SSH input: %+v %v", hello, err)
	}
	data, err := view.request(ctx, "GET", view.resource("media/1"), nil, 1024)
	if err != nil || string(data) != "resource through SSH" {
		t.Fatalf("SSH resource: %q %v", data, err)
	}
	// A new dial replaces a dead master while retaining the service URL.
	tunnel.mu.Lock()
	pid := tunnel.cmd.Process.Pid
	tunnel.cmd.Process.Kill()
	<-tunnel.done
	tunnel.mu.Unlock()
	r.http.CloseIdleConnections()
	if _, err := r.sessions(ctx); err != nil {
		t.Fatal(err)
	}
	if tunnel.cmd.Process.Pid == pid {
		t.Fatal("dead SSH master was reused")
	}
	tunnel.close()
	if _, err := os.Stat(tunnel.dir); !os.IsNotExist(err) {
		t.Fatalf("SSH sockets leaked: %v", err)
	}
}

func localSFTP(t *testing.T) (string, string) {
	t.Helper()
	client, err := exec.LookPath("sftp")
	if err != nil {
		t.Skip("OpenSSH sftp unavailable")
	}
	for _, server := range []string{"/usr/lib/ssh/sftp-server", "/usr/lib/openssh/sftp-server", "/usr/libexec/sftp-server"} {
		if _, err := os.Stat(server); err == nil {
			return client, server
		}
	}
	t.Skip("local sftp-server unavailable")
	return "", ""
}

func TestSFTPLiteralPathsAndDirectories(t *testing.T) {
	client, server := localSFTP(t)
	dir := t.TempDir()
	for _, name := range []string{"space and 界.txt", "quote'\".txt", "glob[*?].txt", "back\\slash.txt", "-option.txt"} {
		source := filepath.Join(dir, "source "+name)
		remote := filepath.Join(dir, "remote "+name)
		destination := filepath.Join(dir, "download "+name)
		if err := os.WriteFile(source, []byte(name), 0600); err != nil {
			t.Fatal(err)
		}
		for _, transfer := range []struct {
			upload   bool
			from, to string
		}{{true, source, remote}, {false, remote, destination}} {
			batch, err := transferBatch(transfer.upload, transfer.from, transfer.to)
			if err != nil {
				t.Fatal(err)
			}
			cmd := exec.Command(client, "-D", server, "-b", "-")
			cmd.Stdin = strings.NewReader(batch)
			if output, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("SFTP %q: %v %s", batch, err, output)
			}
		}
		if data, err := os.ReadFile(destination); err != nil || string(data) != name {
			t.Fatalf("literal filename round trip: %q %v", data, err)
		}
	}
	for _, path := range []string{"", "a\nb", "a\x00b"} {
		if _, err := transferBatch(true, path, "."); err == nil {
			t.Fatalf("accepted malformed transfer path %q", path)
		}
	}
	// Recursive transfer is useful for project folders as well as single files.
	source := filepath.Join(dir, "folder")
	remote := filepath.Join(dir, "uploaded folder")
	destination := filepath.Join(dir, "downloaded folder")
	if err := os.MkdirAll(filepath.Join(source, "nested"), 0700); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(source, "nested", "file"), []byte("directory data"), 0600)
	put, _ := transferBatch(true, source, remote)
	get, _ := transferBatch(false, remote, destination)
	cmd := exec.Command(client, "-D", server, "-b", "-")
	cmd.Stdin = strings.NewReader(put + get)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("directory transfer: %v %s", err, output)
	}
	if data, err := os.ReadFile(filepath.Join(destination, "nested", "file")); err != nil || string(data) != "directory data" {
		t.Fatalf("directory round trip: %q %v", data, err)
	}
}

func TestSSHTransferButtonsAndModalInput(t *testing.T) {
	a := testTabs(t)
	var output strings.Builder
	a.out = &output
	if err := a.drawStatus(); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(output.String(), "[Upload]") {
		t.Fatal("transfer buttons shown without SSH")
	}
	a.remote.ssh = &sshTunnel{}
	output.Reset()
	a.drawStatus()
	if !strings.Contains(output.String(), "[Upload] [Download]") {
		t.Fatal("missing SSH transfer buttons")
	}
	// Clicking the status row must open a local prompt, not change service tabs.
	a.input(localEvent{input: inputMessage{Type: "press", Button: 1, X: 10, Y: a.screen.rows + 2}})
	if a.prompt == nil || a.prompt.upload {
		t.Fatal("download button missed")
	}
	a.input(localEvent{input: inputMessage{Type: "paste", Text: "remote 界.txt"}})
	a.input(localEvent{input: inputMessage{Type: "key", Key: "Enter"}})
	if a.prompt.source != "remote 界.txt" || a.prompt.text != "." {
		t.Fatal("source prompt did not advance to destination")
	}
	a.input(localEvent{input: keyRune(21, 0)}) // Ctrl-U
	a.input(localEvent{input: inputMessage{Type: "paste", Text: "bad\ncommand"}})
	if a.prompt.text != "" {
		t.Fatal("multiline paste accepted as path")
	}
	a.input(localEvent{input: inputMessage{Type: "key", Key: "Escape"}})
	if a.prompt != nil {
		t.Fatal("Escape did not dismiss prompt")
	}
	for _, cols := range []int{8, 16, 24, 80} {
		a.screen.size(cols, 6)
		bar, split, end := a.transferBar()
		if _, width := tabText(bar, cols); width != len(bar) || end > cols+1 {
			t.Fatalf("buttons exceed %d columns", cols)
		}
		if !a.transferInput(inputMessage{Type: "press", Button: 1, X: split, Y: 8}) || a.prompt == nil || a.prompt.upload {
			t.Fatalf("download target at %d columns", cols)
		}
		a.prompt = nil
	}
	a.input(localEvent{escape: true})
	a.input(localEvent{input: inputMessage{Type: "key", Key: "u"}})
	if a.prompt == nil || !a.prompt.upload {
		t.Fatal("Ctrl-] u did not open upload")
	}
}

func TestSSHPTYUploadDownloadAndCancel(t *testing.T) {
	client, sftpServer := localSFTP(t)
	service := &tabTestService{entries: []ptyInfo{tabFixture("one", "Shell")}, sockets: map[string]*websocket.Conn{}, events: make(chan string, 128)}
	server := httptest.NewServer(http.HandlerFunc(service.serve))
	defer server.Close()
	fakeSSH(t, server.Listener.Addr().String())
	dir := t.TempDir()
	// Use the actual SFTP protocol on local fixture files. A FIFO can stall the
	// subprocess to check that cancellation and terminal input remain live.
	script := "#!/bin/sh\nif [ -n \"$ST_CLIENT_SFTP_GATE\" ]; then\n  read signal <\"$ST_CLIENT_SFTP_GATE\"\nfi\nexec \"$ST_CLIENT_REAL_SFTP\" -D \"$ST_CLIENT_SFTP_SERVER\" -b -\n"
	os.WriteFile(filepath.Join(dir, "sftp"), []byte(script), 0700)
	t.Setenv("PATH", dir+":"+os.Getenv("PATH"))
	t.Setenv("ST_CLIENT_REAL_SFTP", client)
	t.Setenv("ST_CLIENT_SFTP_SERVER", sftpServer)
	source, remote, local := filepath.Join(dir, "source.txt"), filepath.Join(dir, "remote.txt"), filepath.Join(dir, "local.txt")
	os.WriteFile(source, []byte("SSH transfer round trip"), 0600)
	master, slave := openPTY(t, 80, 12)
	out, stderr := capture(), capture()
	done := make(chan int, 1)
	go func() {
		done <- run([]string{"-ssh", "fixture", "http://remote.invalid:8080/service/"}, slave, out, stderr)
	}()
	defer func() {
		master.Write([]byte("\x03\x1dq")) // also dismiss a prompt on test failure
		select {
		case status := <-done:
			if status != 0 {
				t.Errorf("client failed: %s", stderr.text())
			}
		case <-time.After(5 * time.Second):
			t.Error("SSH client cleanup blocked")
		}
	}()
	waitTabEvent(t, service, "attached:one")
	out.await(t, func(s string) bool { return strings.Contains(s, "[Upload] [Download]") })
	paths := func(from, to string) string {
		return "\x1b[200~" + from + "\x1b[201~\r\x15\x1b[200~" + to + "\x1b[201~\r"
	}
	master.Write([]byte("\x1b[<0;2;12M\x1b[<0;2;12m" + paths(source, remote)))
	out.await(t, func(s string) bool { return strings.Contains(s, "Upload complete") })
	if data, err := os.ReadFile(remote); err != nil || string(data) != "SSH transfer round trip" {
		t.Fatalf("upload button: %q %v", data, err)
	}
	master.Write([]byte("\x1b[<0;10;12M\x1b[<0;10;12m" + paths(remote, local)))
	out.await(t, func(s string) bool { return strings.Contains(s, "Download complete") })
	if data, err := os.ReadFile(local); err != nil || string(data) != "SSH transfer round trip" {
		t.Fatalf("download button: %q %v", data, err)
	}
	gate := filepath.Join(dir, "gate")
	if err := unix.Mkfifo(gate, 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("ST_CLIENT_SFTP_GATE", gate)
	mark := len(out.text())
	master.Write([]byte("\x1du" + paths(source, remote)))
	out.await(t, func(s string) bool { return len(s) > mark && strings.Contains(s[mark:], "Uploading") })
	// Tab actions still work during an external transfer.
	master.Write([]byte("\x1dn"))
	waitTabEvent(t, service, "attached:pty1")
	master.Write([]byte("\x1dk"))
	out.await(t, func(s string) bool { return strings.Contains(s, "context canceled") })
	service.mu.Lock()
	remaining := len(service.entries)
	service.mu.Unlock()
	if remaining != 2 {
		t.Fatal("transfer prompt input leaked into the remote PTY")
	}
}

func TestLiveWebDaemon(t *testing.T) {
	binary := os.Getenv("ST_CLIENT_TEST_SERVER")
	if binary == "" {
		t.Skip("set ST_CLIENT_TEST_SERVER for daemon lifecycle checks")
	}
	binary, err := filepath.Abs(binary)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	marker := filepath.Join(dir, "command-started")
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, binary, "--daemon", "-port", "0", "-dir", dir,
		"-e", "/bin/sh", "-c", `printf ready > "$1"; exec /bin/sh`, "sh", marker)
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("daemon launch: %v %s", err, output)
	}
	match := regexp.MustCompile(`Web terminal daemon: PID ([0-9]+) (http://[^\s]+)`).FindStringSubmatch(string(output))
	if match == nil {
		t.Fatalf("missing daemon readiness: %s", output)
	}
	pid, _ := strconv.Atoi(match[1])
	defer syscall.Kill(pid, syscall.SIGTERM)
	if sid, err := unix.Getsid(pid); err != nil || sid != pid {
		t.Fatalf("daemon is not detached: SID %d PID %d %v", sid, pid, err)
	}
	r, _ := newRemote(match[2], "")
	list, err := r.sessions(ctx)
	if err != nil || len(list) != 1 {
		t.Fatalf("daemon service: %v %v", list, err)
	}
	view := *r
	view.selectView(list[0])
	c, err := view.connect(ctx, make(chan wireEvent, 8))
	if err != nil {
		t.Fatal(err)
	}
	defer c.close()
	var hello serverMessage
	c.ws.SetReadDeadline(time.Now().Add(3 * time.Second))
	if err := websocket.JSON.Receive(c.ws, &hello); err != nil {
		t.Fatal(err)
	}
	// Verify -e arguments survived the frontend's argument splitting.
	for {
		if data, _ := os.ReadFile(marker); string(data) == "ready" {
			break
		}
		select {
		case <-ctx.Done():
			t.Fatal("daemon lost -e command arguments")
		case <-time.After(10 * time.Millisecond):
		}
	}
	_, err = r.requestAttached(ctx, "DELETE", r.resource("api/ptys/"+list[0].ID), nil, 1024, hello.Attachment)
	if err != nil {
		t.Fatal(err)
	}
	if list, err := r.sessions(ctx); err != nil || len(list) != 0 {
		t.Fatalf("daemon must survive its last PTY: %v %v", list, err)
	}
	// A bind failure must reach the launcher rather than reporting success.
	failure := exec.CommandContext(ctx, binary, "--daemon", "-port", r.base.Port())
	if out, err := failure.CombinedOutput(); err == nil || !strings.Contains(string(out), "address already in use") {
		t.Fatalf("daemon bind failure: %v %s", err, out)
	}
}
