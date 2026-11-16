package aiharness

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
)

type sshProcessRecord struct {
	PID     int
	Args    []string
	Address string
}

// The fake ssh is a real child process and TCP forward, so tests exercise HTTP
// routing, subprocess failures, cancellation and reaping without an SSH account.
func TestSSHTunnelProcess(t *testing.T) {
	if os.Getenv("AIHARNESS_TEST_SSH_PROCESS") != "1" {
		return
	}
	args := os.Args[slices.Index(os.Args, "--")+1:]
	index := slices.Index(args, "-L")
	if index < 0 || index+1 >= len(args) {
		os.Exit(2)
	}
	parts := strings.SplitN(args[index+1], ":", 3)
	if len(parts) != 3 {
		os.Exit(2)
	}
	address := parts[0] + ":" + parts[1]
	record, _ := json.Marshal(sshProcessRecord{PID: os.Getpid(), Args: args, Address: address})
	if err := os.WriteFile(os.Getenv("AIHARNESS_TEST_SSH_RECORD"), record, 0600); err != nil {
		os.Exit(2)
	}
	switch os.Getenv("AIHARNESS_TEST_SSH_MODE") {
	case "fail":
		fmt.Fprintln(os.Stderr, "Permission denied (publickey).")
		os.Exit(255)
	case "stall":
		time.Sleep(time.Hour)
		os.Exit(2)
	}
	listener, err := net.Listen("tcp4", address)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	for {
		local, err := listener.Accept()
		if err != nil {
			os.Exit(2)
		}
		go func() {
			defer local.Close()
			remote, err := net.Dial("tcp", parts[2])
			if err != nil {
				return
			}
			defer remote.Close()
			connections, err := os.OpenFile(os.Getenv("AIHARNESS_TEST_SSH_RECORD")+".connections", os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0600)
			if err != nil {
				os.Exit(2)
			}
			fmt.Fprintln(connections, remote.LocalAddr().String())
			connections.Close()
			go func() {
				io.Copy(remote, local)
				remote.(*net.TCPConn).CloseWrite()
			}()
			io.Copy(local, remote)
		}()
	}
}

func fakeSSH(t *testing.T, mode string) string {
	t.Helper()
	dir := t.TempDir()
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("AIHARNESS_TEST_SSH_EXECUTABLE", executable)
	t.Setenv("AIHARNESS_TEST_SSH_PROCESS", "1")
	t.Setenv("AIHARNESS_TEST_SSH_MODE", mode)
	record := filepath.Join(dir, "record.json")
	t.Setenv("AIHARNESS_TEST_SSH_RECORD", record)
	script := "#!/bin/sh\nexec \"$AIHARNESS_TEST_SSH_EXECUTABLE\" -test.run=^TestSSHTunnelProcess$ -- \"$@\"\n"
	if err := os.WriteFile(filepath.Join(dir, "ssh"), []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return record
}

func readSSHProcess(t *testing.T, path string) sshProcessRecord {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		var record sshProcessRecord
		data, err := os.ReadFile(path)
		if err == nil && json.Unmarshal(data, &record) == nil {
			return record
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("SSH process did not record its arguments")
	return sshProcessRecord{}
}

func assertSSHStopped(t *testing.T, record sshProcessRecord) {
	t.Helper()
	if err := syscall.Kill(record.PID, 0); !errors.Is(err, syscall.ESRCH) {
		t.Fatalf("SSH process %d was not reaped: %v", record.PID, err)
	}
	if connection, err := net.DialTimeout("tcp", record.Address, 100*time.Millisecond); err == nil {
		connection.Close()
		t.Fatal("SSH listener survived generation")
	}
}

func TestOpenCodeSSHTunnelGeneration(t *testing.T) {
	for _, test := range []struct {
		name, password, explicit, wantAuth string
		status                             int
	}{
		{name: "no-local-credentials", status: 200},
		{name: "remote-password", password: "remote-test-password", wantAuth: "Basic " + base64.StdEncoding.EncodeToString([]byte("opencode:remote-test-password")), status: 200},
		{name: "explicit-header", password: "ignored", explicit: "Bearer remote-test-token", wantAuth: "Bearer remote-test-token", status: 200},
		{name: "no-generation-retry", status: 503},
	} {
		t.Run(test.name, func(t *testing.T) {
			recordPath := fakeSSH(t, "")
			var discoveries, generations atomic.Int32
			var host string
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				connections, err := os.ReadFile(recordPath + ".connections")
				if err != nil || !strings.Contains("\n"+string(connections), "\n"+r.RemoteAddr+"\n") {
					t.Error("HTTP request bypassed the SSH forward")
				}
				if r.Host != host || r.Header.Get("Authorization") != test.wantAuth {
					t.Error("SSH request changed host or used the wrong server credentials")
				}
				if r.Method == "GET" && r.URL.Path == "/base/openapi.json" {
					discoveries.Add(1)
					fmt.Fprintf(w, `{"paths":{%q:{"post":{}}}}`, generateTextPath)
					return
				}
				if r.Method != "POST" || r.URL.Path != "/base"+generateTextPath {
					t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
				}
				generations.Add(1)
				var body struct {
					Prompt string
					Model  *Model
				}
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Prompt != "Frozen prompt." || body.Model == nil || body.Model.ID != "paragraph" {
					t.Errorf("changed request: %+v %v", body, err)
				}
				w.WriteHeader(test.status)
				io.WriteString(w, `{"data":{"text":"Remote paragraph."}}`)
			}))
			defer server.Close()
			host = strings.TrimPrefix(server.URL, "http://")
			// The remote URL deliberately matches a local registration. That
			// registration's password must never be sent through the tunnel.
			registerTestService(t, server.URL+"/base")
			os.Unsetenv("OPENCODE_TUI_PASSWORD")
			t.Setenv("OPENCODE_PASSWORD", test.password)
			client, err := NewOpenCodeSSH(server.URL+"/base", "writer@remote-alias", test.explicit)
			if err != nil {
				t.Fatal(err)
			}
			for n := 1; n <= 2; n++ {
				text, err := client.Generate(context.Background(), "Frozen prompt.", &Model{ProviderID: "test", ID: "paragraph"})
				if test.status == 200 && (err != nil || text != "Remote paragraph.") || test.status != 200 && err == nil {
					t.Fatalf("generation: %q %v", text, err)
				}
				if discoveries.Load() != int32(n) || generations.Load() != int32(n) {
					t.Fatal("unexpected discovery/generation count")
				}
				record := readSSHProcess(t, recordPath)
				for _, arg := range []string{"-N", "-L", "BatchMode=yes", "ExitOnForwardFailure=yes", "ControlPath=none", "ForkAfterAuthentication=no"} {
					if !slices.Contains(record.Args, arg) {
						t.Fatalf("SSH option %s missing: %v", arg, record.Args)
					}
				}
				if record.Args[len(record.Args)-1] != "writer@remote-alias" || !strings.HasPrefix(record.Address, "127.0.0.1:") {
					t.Fatalf("wrong SSH destination/listener: %+v", record)
				}
				assertSSHStopped(t, record)
			}
		})
	}
}

func TestOpenCodeSSHTunnelFailureAndCancellation(t *testing.T) {
	for _, mode := range []string{"fail", "stall", "generating"} {
		t.Run(mode, func(t *testing.T) {
			recordPath := fakeSSH(t, mode)
			started := make(chan struct{})
			var requests atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests.Add(1)
				if r.URL.Path == "/openapi.json" {
					fmt.Fprintf(w, `{"paths":{%q:{"post":{}}}}`, generateTextPath)
					return
				}
				io.Copy(io.Discard, r.Body)
				close(started)
				<-r.Context().Done()
			}))
			defer server.Close()
			client, err := NewOpenCodeSSH(server.URL, "remote-alias", "Bearer synthetic")
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			done := make(chan error, 1)
			go func() {
				_, err := client.Generate(ctx, "test", nil)
				done <- err
			}()
			record := readSSHProcess(t, recordPath)
			if mode == "generating" {
				select {
				case <-started:
				case <-ctx.Done():
					t.Fatal("generation never started")
				}
			}
			if mode != "fail" {
				cancel()
			}
			select {
			case err := <-done:
				if err == nil || mode == "fail" && !strings.Contains(err.Error(), "Permission denied (publickey)") || mode != "fail" && !errors.Is(err, context.Canceled) {
					t.Fatalf("SSH failure: %v", err)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("SSH generation did not stop")
			}
			assertSSHStopped(t, record)
			if mode != "generating" && requests.Load() != 0 {
				t.Fatal("failed tunnel fell back to direct HTTP")
			}
		})
	}
}

func TestOpenCodeSSHConfiguration(t *testing.T) {
	client, err := NewOpenCodeSSH("", "writer@alias", "Bearer test")
	if err != nil || client.server.String() != "http://127.0.0.1:4096" {
		t.Fatalf("default remote URL: %+v %v", client, err)
	}
	for _, test := range []struct{ server, destination string }{
		{"", ""}, {"", "-oProxyCommand=bad"}, {"", "user@host extra"}, {"", "host;command"},
		{"http://public.example:4096", "host"}, {"http://127.0.0.1:0", "host"},
		{"http://127.0.0.1:65536", "host"}, {"http://user:password@localhost:4096", "host"},
	} {
		if _, err := NewOpenCodeSSH(test.server, test.destination, "Bearer test"); err == nil {
			t.Fatalf("accepted invalid SSH configuration: %+v", test)
		}
	}
}
