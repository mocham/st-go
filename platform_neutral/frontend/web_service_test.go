package frontend

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"golang.org/x/net/websocket"
)

func TestWebServicePTYLifetimeAndZeroRecovery(t *testing.T) {
	binary, _ := filepath.Abs("BINARIES/st-web")
	if _, err := os.Stat(binary); err != nil {
		t.Skip("build with make st-web")
	}
	dir := t.TempDir()
	cmd := exec.Command(binary, "-service", "-port", "0", "-base-path", "/nested/terminal/", "-attachment-timeout", "1s", "-dir", dir, "-e", "/bin/true")
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	exited := make(chan error, 1)
	go func() { exited <- cmd.Wait() }()
	t.Cleanup(func() {
		cmd.Process.Kill()
		select {
		case <-exited:
		case <-time.After(5 * time.Second):
			t.Error("service did not exit")
		}
	})
	addresses := make(chan string, 1)
	go func() {
		scanner := bufio.NewScanner(stdout)
		for scanner.Scan() {
			if line := scanner.Text(); strings.HasPrefix(line, "Web terminal service: ") {
				addresses <- strings.TrimPrefix(line, "Web terminal service: ")
			}
		}
	}()
	var address string
	select {
	case address = <-addresses:
	case <-time.After(5 * time.Second):
		t.Fatal("service did not start")
	}
	u, _ := url.Parse(address)
	origin := u.Scheme + "://" + u.Host
	type pty struct {
		ID, Title string
		Occupied  bool
	}
	request := func(method, path string, body any, code int) []byte {
		t.Helper()
		var data []byte
		if body != nil {
			data, _ = json.Marshal(body)
		}
		r, _ := http.NewRequest(method, address+path, bytes.NewReader(data))
		r.Header.Set("Origin", origin)
		r.Header.Set("Content-Type", "application/json")
		client := http.Client{Timeout: 4 * time.Second}
		response, err := client.Do(r)
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()
		data, _ = io.ReadAll(response.Body)
		if response.StatusCode != code {
			t.Fatalf("%s %s: %d %s", method, path, response.StatusCode, data)
		}
		return data
	}
	await := func(description string, check func() bool) {
		t.Helper()
		end := time.Now().Add(5 * time.Second)
		for !check() {
			if time.Now().After(end) {
				t.Fatal("timed out:", description)
			}
			time.Sleep(20 * time.Millisecond)
		}
	}
	list := func() []pty {
		var result []pty
		if err := json.Unmarshal(request("GET", "api/ptys", nil, 200), &result); err != nil {
			t.Fatal(err)
		}
		return result
	}
	await("initial command exits while service remains healthy", func() bool { return len(list()) == 0 })
	request("GET", "healthz", nil, 200)
	var first pty
	json.Unmarshal(request("POST", "api/ptys", map[string]any{"command": []string{"/bin/sh"}}, 201), &first)
	attach := func(id string) *websocket.Conn {
		t.Helper()
		c, err := websocket.Dial("ws://"+u.Host+u.Path+"api/ptys/"+id+"/attach", "", origin)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { c.Close() })
		go func() {
			for {
				var data []byte
				if websocket.Message.Receive(c, &data) != nil {
					return
				}
			}
		}()
		return c
	}
	send := func(c *websocket.Conn, text string) {
		t.Helper()
		if err := websocket.JSON.Send(c, map[string]string{"type": "text", "text": text}); err != nil {
			t.Fatal(err)
		}
	}
	fileIs := func(name, want string) bool {
		data, _ := os.ReadFile(filepath.Join(dir, name))
		return string(data) == want
	}
	c := attach(first.ID)
	send(c, "printf '%s' \"$PWD\" > cwd; printf '%s' \"$$\" > pid\n")
	await("PTY input and directory", func() bool { return fileIs("cwd", dir) })
	pid, _ := os.ReadFile(filepath.Join(dir, "pid"))
	// No traffic: the browser lease expires; the original shell stays alive.
	await("lease expiry", func() bool { items := list(); return len(items) == 1 && !items[0].Occupied })
	c = attach(first.ID)
	send(c, "printf '%s' \"$$\" > reattached; (sleep 0.2; printf done > background) &\n")
	await("same process reattached", func() bool { return fileIs("reattached", string(pid)) })
	c.Close()
	await("detached child progresses", func() bool { return fileIs("background", "done") })
	await("clean release", func() bool { return !list()[0].Occupied })
	request("DELETE", "api/ptys/"+first.ID, nil, 204)
	await("last deletion", func() bool { return len(list()) == 0 })
	var fresh pty
	json.Unmarshal(request("POST", "api/ptys", map[string]any{}, 201), &fresh)
	c = attach(fresh.ID)
	send(c, "printf restarted > recovered; exit\n")
	await("create after zero PTYs", func() bool { return fileIs("recovered", "restarted") })
	await("natural last exit", func() bool { return len(list()) == 0 })
	request("GET", "healthz", nil, 200)
	// Failed exec does not poison later admission.
	request("POST", "api/ptys", map[string]any{"command": []string{"/no/such/executable"}}, 400)
	json.Unmarshal(request("POST", "api/ptys", map[string]any{"command": []string{"/bin/sh", "-c", "printf '%s' \"$$\" > shutdown-pid; exec sleep 60"}}, 201), &fresh)
	await("shutdown child", func() bool { data, _ := os.ReadFile(filepath.Join(dir, "shutdown-pid")); return len(data) > 0 })
	cmd.Process.Signal(syscall.SIGTERM)
	select {
	case err := <-exited:
		exited <- err
		if err != nil {
			t.Fatalf("shutdown: %v\n%s", err, stderr.String())
		}
	case <-time.After(5 * time.Second):
		t.Fatal("shutdown hung")
	}
	data, _ := os.ReadFile(filepath.Join(dir, "shutdown-pid"))
	var childPID int
	fmt.Sscan(string(data), &childPID)
	if childPID == 0 || syscall.Kill(childPID, 0) != syscall.ESRCH {
		t.Fatal("shutdown did not reap child", childPID)
	}
}
