package aiharness

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"st-go/sshtunnel/sshtest"
)

func TestSSHTunnelProcess(t *testing.T) {
	sshtest.Process()
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
			recordPath := sshtest.Install(t, "")
			var discoveries, generations atomic.Int32
			var host string
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				sshtest.AssertForwarded(t, recordPath, r.RemoteAddr)
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
				record := sshtest.ReadProcess(t, recordPath)
				for _, arg := range []string{"-N", "-L", "BatchMode=yes", "ExitOnForwardFailure=yes", "ControlPath=none", "ForkAfterAuthentication=no"} {
					if !slices.Contains(record.Args, arg) {
						t.Fatalf("SSH option %s missing: %v", arg, record.Args)
					}
				}
				if record.Args[len(record.Args)-1] != "writer@remote-alias" || !strings.HasPrefix(record.Address, "127.0.0.1:") {
					t.Fatalf("wrong SSH destination/listener: %+v", record)
				}
				sshtest.AssertStopped(t, record)
			}
		})
	}
}

func TestOpenCodeSSHTunnelFailureAndCancellation(t *testing.T) {
	for _, mode := range []string{"fail", "stall", "generating"} {
		t.Run(mode, func(t *testing.T) {
			recordPath := sshtest.Install(t, mode)
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
			record := sshtest.ReadProcess(t, recordPath)
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
			sshtest.AssertStopped(t, record)
			if mode != "generating" && requests.Load() != 0 {
				t.Fatal("failed tunnel fell back to direct HTTP")
			}
		})
	}
}

func TestOpenCodeSSHConfiguration(t *testing.T) {
	client, err := NewOpenCodeSSH("", "writer@alias", "Bearer test")
	if err != nil || client.target.ServerURL() != "http://127.0.0.1:4096" {
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
