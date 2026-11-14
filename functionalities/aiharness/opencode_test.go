package aiharness

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
)

// All authentication values in these tests are synthetic mock-server fixtures.
const testServicePassword = "test-only-service-password"

func registerTestService(t *testing.T, server string) {
	t.Helper()
	state := t.TempDir()
	t.Setenv("XDG_STATE_HOME", state)
	t.Setenv("OPENCODE_PASSWORD", "")
	t.Setenv("OPENCODE_TUI_AUTHORIZATION", "")
	t.Setenv("OPENCODE_TUI_USERNAME", "")
	t.Setenv("OPENCODE_TUI_PASSWORD", "")
	if err := os.MkdirAll(filepath.Join(state, "opencode"), 0700); err != nil {
		t.Fatal(err)
	}
	data, _ := json.Marshal(serviceRegistration{URL: server, Password: testServicePassword})
	if err := os.WriteFile(filepath.Join(state, "opencode", "service.json"), data, 0600); err != nil {
		t.Fatal(err)
	}
}

func TestOpenCodeLocalServiceAuthenticationAndV2Routes(t *testing.T) {
	for _, path := range []string{generateTextPath, legacyV2GeneratePath} {
		for _, explicit := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/explicit=%t", path, explicit), func(t *testing.T) {
				var schemaCalls, generations atomic.Int32
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					user, password, ok := r.BasicAuth()
					if !ok || user != "opencode" || password != testServicePassword {
						t.Error("local service authentication missing")
						w.WriteHeader(http.StatusUnauthorized)
						return
					}
					if r.Method == "GET" && r.URL.Path == "/openapi.json" {
						schemaCalls.Add(1)
						fmt.Fprintf(w, `{"paths":{%q:{"post":{}}}}`, path)
						return
					}
					if r.Method != "POST" || r.URL.Path != path {
						t.Errorf("unexpected route %s %s", r.Method, r.URL.Path)
						http.NotFound(w, r)
						return
					}
					generations.Add(1)
					var body struct {
						Prompt string
						Model  *Model
					}
					decoder := json.NewDecoder(r.Body)
					decoder.DisallowUnknownFields()
					if err := decoder.Decode(&body); err != nil {
						t.Error(err)
						return
					}
					if body.Prompt != "Explicit prompt." || body.Model == nil || body.Model.ProviderID != "test" || body.Model.ID != "paragraph" || body.Model.Variant != "deep" {
						t.Errorf("changed prompt/model: %+v", body)
					}
					w.Write([]byte(`{"data":{"text":"Subscription paragraph."}}`))
				}))
				defer server.Close()
				registerTestService(t, server.URL)
				address := ""
				if explicit {
					address = strings.Replace(server.URL, "127.0.0.1", "localhost", 1) + "/"
				}
				client, err := NewOpenCode(address, "")
				if err != nil {
					t.Fatal(err)
				}
				for range 2 {
					text, err := client.Generate(context.Background(), "Explicit prompt.", &Model{"test", "paragraph", "deep"})
					if err != nil || text != "Subscription paragraph." {
						t.Fatalf("generate: %q %v", text, err)
					}
				}
				if schemaCalls.Load() != 1 || generations.Load() != 2 {
					t.Fatal("API discovery repeated or a generation was retried")
				}
			})
		}
	}
}

func TestOpenCodeServerCredentialsAreScopedAndExplicitOverridesWin(t *testing.T) {
	for _, test := range []struct{ name, password, header, want string }{
		{name: "unrelated-server"},
		{name: "server-password", password: "test-only-password", want: "Basic " + base64.StdEncoding.EncodeToString([]byte("opencode:test-only-password"))},
		{name: "explicit-header", password: "test-only-password", header: "Bearer test-only-token", want: "Bearer test-only-token"},
	} {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Header.Get("Authorization") != test.want {
					t.Error("wrong credentials sent to selected server")
				}
				if r.URL.Path == "/openapi.json" {
					w.Write([]byte(`{"paths":{"/api/experimental/generate/text":{"post":{}}}}`))
					return
				}
				w.Write([]byte(`{"data":{"text":"OK"}}`))
			}))
			defer server.Close()
			registerTestService(t, "http://127.0.0.1:1")
			// An absent setting differs from an explicitly configured empty
			// Basic password, which the existing TUI client supports.
			os.Unsetenv("OPENCODE_TUI_PASSWORD")
			t.Setenv("OPENCODE_PASSWORD", test.password)
			client, err := NewOpenCode(server.URL, test.header)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := client.Generate(context.Background(), "test", nil); err != nil {
				t.Fatal(err)
			}
		})
	}
	for _, address := range []string{"http://example.com:4096", "https://localhost:4096", "http://localhost:4097", "http://localhost:4096/other"} {
		u, _ := serverURL(address)
		registered, _ := serverURL("http://127.0.0.1:4096")
		if sameLocalServer(u, registered) {
			t.Fatalf("local credentials matched %s", address)
		}
	}
}

func TestOpenCodeInheritsTUIAuthentication(t *testing.T) {
	for _, test := range []struct{ name, password, username, header, explicit, want string }{
		{name: "password", password: "test-only-password", username: "test-user", want: "Basic " + base64.StdEncoding.EncodeToString([]byte("test-user:test-only-password"))},
		{name: "default-user", password: "test-only-password", want: "Basic " + base64.StdEncoding.EncodeToString([]byte("opencode:test-only-password"))},
		{name: "header", password: "test-only-password", header: "Bearer test-only-inherited", want: "Bearer test-only-inherited"},
		{name: "explicit-override", header: "Bearer test-only-inherited", explicit: "Bearer test-only-override", want: "Bearer test-only-override"},
	} {
		t.Run(test.name, func(t *testing.T) {
			registerTestService(t, "http://127.0.0.1:1")
			t.Setenv("OPENCODE_TUI_PASSWORD", test.password)
			t.Setenv("OPENCODE_TUI_USERNAME", test.username)
			t.Setenv("OPENCODE_TUI_AUTHORIZATION", test.header)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Header.Get("Authorization") != test.want {
					t.Error("inherited server login was not forwarded")
					w.WriteHeader(401)
					return
				}
				if r.URL.Path == "/openapi.json" {
					w.Write([]byte(`{"paths":{"/api/experimental/generate/text":{"post":{}}}}`))
					return
				}
				w.Write([]byte(`{"data":{"text":"OK"}}`))
			}))
			defer server.Close()
			client, err := NewOpenCode(server.URL, test.explicit)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := client.Generate(context.Background(), "test", nil); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestOpenCodeAuthenticationAndGenerationErrorsDoNotRetry(t *testing.T) {
	for _, test := range []struct {
		name                         string
		discovery, generation, posts int
	}{
		{"unauthorized", 401, 200, 0},
		{"provider-error", 200, 503, 1},
		{"generation-not-found", 200, 404, 1},
	} {
		t.Run(test.name, func(t *testing.T) {
			var posts atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/openapi.json" {
					w.WriteHeader(test.discovery)
					w.Write([]byte(`{"paths":{"/api/experimental/generate/text":{"post":{}},"/api/experimental/generate":{"post":{}}}}`))
					return
				}
				posts.Add(1)
				w.WriteHeader(test.generation)
			}))
			defer server.Close()
			client, err := NewOpenCode(server.URL, "Bearer explicit")
			if err != nil {
				t.Fatal(err)
			}
			_, err = client.Generate(context.Background(), "test", nil)
			if err == nil || int(posts.Load()) != test.posts {
				t.Fatalf("error=%v posts=%d", err, posts.Load())
			}
			if test.discovery == 401 && !strings.Contains(err.Error(), "server") {
				t.Fatal("401 did not identify server authentication")
			}
		})
	}
}
