package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"st-go/ptyutil"
	"st-go/sshtunnel/sshtest"
)

func TestSSHTunnelProcess(t *testing.T) { sshtest.Process() }

func TestSSHOptions(t *testing.T) {
	for _, test := range []struct {
		name, envServer, envSSH string
		args                    []string
		server, ssh             string
		invalid                 bool
	}{
		{name: "default-port", args: []string{"--server-ssh", "user@host"}, server: "http://127.0.0.1:4096", ssh: "user@host"},
		{name: "environment", envServer: "localhost:5000", envSSH: "alias", server: "http://localhost:5000", ssh: "alias"},
		{name: "flag-overrides", envServer: "localhost:5000", envSSH: "env-host", args: []string{"--server-ssh", "flag-host", "--server", "http://127.0.0.1:6000/base"}, server: "http://127.0.0.1:6000/base", ssh: "flag-host"},
		{name: "disable-env", envServer: "https://remote.example", envSSH: "env-host", args: []string{"--server-ssh="}, server: "https://remote.example"},
		{name: "direct-needs-url", invalid: true},
		{name: "not-loopback", args: []string{"--server-ssh", "host", "--server", "http://remote.example:4096"}, invalid: true},
		{name: "invalid-destination", args: []string{"--server-ssh", "host extra"}, invalid: true},
		{name: "invalid-port", args: []string{"--server-ssh", "host", "--server", "localhost:65536"}, invalid: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Setenv("OPENCODE_TUI_SERVER", test.envServer)
			t.Setenv("OPENCODE_TUI_SERVER_SSH", test.envSSH)
			configuration, err := parseOptions(test.args)
			if test.invalid {
				if err == nil {
					t.Fatal("accepted invalid options")
				}
				return
			}
			if err != nil || configuration.server != test.server || configuration.serverSSH != test.ssh {
				t.Fatalf("options: %+v %v", configuration, err)
			}
		})
	}
}

func TestSSHRequestsAndLiveEventsShareTunnel(t *testing.T) {
	recordPath := sshtest.Install(t, "")
	var posts atomic.Int32
	var host string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sshtest.AssertForwarded(t, recordPath, r.RemoteAddr)
		if r.Host != host || r.Header.Get("Authorization") != "Bearer synthetic" {
			t.Error("tunnel changed remote host or lost server authentication")
		}
		switch r.Method + " " + r.URL.Path {
		case "GET /base/api/event":
			w.Header().Set("Content-Type", "text/event-stream")
			io.WriteString(w, "data: {\"type\":\"server.connected\",\"data\":{}}\n\n")
			w.(http.Flusher).Flush()
			<-r.Context().Done()
		case "GET /base/api/info":
			io.WriteString(w, `{"version":"2.test"}`)
		case "POST /base/api/session/ses_test/prompt":
			posts.Add(1)
			var body map[string]string
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body["text"] != "Prompt through SSH" || body["id"] != "msg_stable" {
				t.Errorf("prompt changed: %v %v", body, err)
			}
			http.Error(w, "ambiguous failure", http.StatusServiceUnavailable)
		case "GET /base/redirect":
			http.Redirect(w, r, "/unexpected", http.StatusTemporaryRedirect)
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	host = strings.TrimPrefix(server.URL, "http://")
	client, err := newAPIClient(server.URL+"/base", "Bearer synthetic")
	if err != nil {
		t.Fatal(err)
	}
	defer client.close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := client.openSSH(ctx, "user@remote-alias"); err != nil {
		t.Fatal(err)
	}
	record := sshtest.ReadProcess(t, recordPath)
	connected := make(chan struct{})
	streamDone := make(chan error, 1)
	go func() {
		streamDone <- client.subscribe(ctx, func(event apiEvent) error {
			if event.Type == "server.connected" {
				close(connected)
			}
			return nil
		})
	}()
	select {
	case <-connected:
	case err := <-streamDone:
		t.Fatalf("event stream failed: %v", err)
	case <-ctx.Done():
		t.Fatal("event stream did not connect")
	}
	if err := client.info(ctx); err != nil {
		t.Fatal(err)
	}
	if err := client.prompt(ctx, "ses_test", "Prompt through SSH", "msg_stable"); err == nil || posts.Load() != 1 {
		t.Fatalf("failed prompt was replayed or lost: posts=%d error=%v", posts.Load(), err)
	}
	if err := client.request(ctx, http.MethodGet, "/redirect", nil, nil); err == nil {
		t.Fatal("followed an SSH endpoint redirect")
	}
	if later := sshtest.ReadProcess(t, recordPath); later.PID != record.PID {
		t.Fatal("API requests restarted the live tunnel")
	}
	client.close()
	select {
	case err := <-streamDone:
		if err == nil {
			t.Fatal("closed SSH stream reported success")
		}
	case <-ctx.Done():
		t.Fatal("SSH cleanup did not unblock event stream")
	}
	sshtest.AssertStopped(t, record)
	if err := client.info(ctx); err == nil {
		t.Fatal("closed SSH client fell back to direct HTTP")
	}
}

func TestSSHStartupFailureReapsTunnel(t *testing.T) {
	for _, mode := range []string{"fail", "unauthorized"} {
		t.Run(mode, func(t *testing.T) {
			recordPath := sshtest.Install(t, mode)
			var calls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				sshtest.AssertForwarded(t, recordPath, r.RemoteAddr)
				calls.Add(1)
				if r.URL.Path != "/api/info" || r.Header.Get("Authorization") != "Bearer synthetic" {
					t.Error("startup continued after failure or lost authorization")
				}
				w.WriteHeader(http.StatusUnauthorized)
			}))
			defer server.Close()
			t.Setenv("OPENCODE_TUI_SERVER", server.URL)
			t.Setenv("OPENCODE_TUI_SERVER_SSH", "remote-alias")
			t.Setenv("OPENCODE_TUI_AUTHORIZATION", "Bearer synthetic")
			if code := run([]string{"--no-preview"}); code != 1 {
				t.Fatalf("startup exit code: %d", code)
			}
			sshtest.AssertStopped(t, sshtest.ReadProcess(t, recordPath))
			want := int32(1)
			if mode == "fail" {
				want = 0
			}
			if calls.Load() != want {
				t.Fatalf("startup made %d HTTP calls, want %d", calls.Load(), want)
			}
		})
	}
}

func TestSSHTUILifetime(t *testing.T) {
	for _, mode := range []string{"quit", "disconnect", "cancel"} {
		t.Run(mode, func(t *testing.T) {
			recordPath := sshtest.Install(t, "")
			connected := make(chan struct{}, 1)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				sshtest.AssertForwarded(t, recordPath, r.RemoteAddr)
				if r.URL.Path == "/api/event" {
					w.Header().Set("Content-Type", "text/event-stream")
					io.WriteString(w, "data: {\"type\":\"server.connected\",\"data\":{}}\n\n")
					w.(http.Flusher).Flush()
					select {
					case connected <- struct{}{}:
					default:
					}
					<-r.Context().Done()
					return
				}
				io.WriteString(w, `{"data":[]}`)
			}))
			defer server.Close()
			client, err := newAPIClient(server.URL, "Bearer synthetic")
			if err != nil {
				t.Fatal(err)
			}
			defer client.close()
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			if err := client.openSSH(ctx, "remote-alias"); err != nil {
				t.Fatal(err)
			}
			record := sshtest.ReadProcess(t, recordPath)
			master, slave, err := ptyutil.Open()
			if err != nil {
				t.Fatal(err)
			}
			defer master.Close()
			defer slave.Close()
			rich, err := newRichPreview(io.Discard, 58, false, false)
			if err != nil {
				t.Fatal(err)
			}
			application := newApp(client, sessionInfo{ID: "ses_test"}, nil, nil, io.Discard, int(slave.Fd()), rich)
			done := make(chan error, 1)
			go func() {
				err := application.run()
				client.close()
				done <- err
			}()
			select {
			case <-connected:
			case err := <-done:
				t.Fatalf("TUI stopped before connecting: %v", err)
			case <-ctx.Done():
				t.Fatal("TUI did not start")
			}
			switch mode {
			case "quit":
				if _, err := master.Write([]byte{0x11}); err != nil {
					t.Fatal(err)
				}
			case "disconnect":
				if err := syscall.Kill(record.PID, syscall.SIGKILL); err != nil {
					t.Fatal(err)
				}
			case "cancel":
				cancel()
			}
			select {
			case err := <-done:
				if mode == "quit" && err != nil || mode != "quit" && (err == nil || !strings.Contains(err.Error(), "SSH tunnel exited")) {
					t.Fatalf("TUI exit: %v", err)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("TUI did not exit")
			}
			sshtest.AssertStopped(t, record)
		})
	}
}
