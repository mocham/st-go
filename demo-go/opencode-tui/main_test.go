package main

import (
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

// Attachment failures must not wait for a locally managed server to start,
// create a session, or enter raw terminal mode.
func TestAttachFailureStopsBeforeSessionCreation(t *testing.T) {
	for _, status := range []int{http.StatusUnauthorized, http.StatusServiceUnavailable} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			var calls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				calls.Add(1)
				if request.Method != http.MethodGet || request.URL.Path != "/api/info" {
					t.Errorf("unexpected request after connection failure: %s %s", request.Method, request.URL.Path)
				}
				if request.Header.Get("Authorization") != "Bearer synthetic" {
					t.Error("attachment did not send configured authentication")
				}
				writer.WriteHeader(status)
				io.WriteString(writer, `{"message":"attachment failed"}`)
			}))
			defer server.Close()
			t.Setenv("OPENCODE_TUI_SERVER", server.URL)
			t.Setenv("OPENCODE_TUI_AUTHORIZATION", "Bearer synthetic")
			t.Setenv("PATH", t.TempDir())
			if code := run([]string{"--no-preview"}); code != 1 {
				t.Fatalf("exit code = %d, want connection failure", code)
			}
			if got := calls.Load(); got != 1 {
				t.Fatalf("made %d requests; attachment failure should be reported immediately", got)
			}
		})
	}
}

func TestWebPreviewOptionCanBeDisabled(t *testing.T) {
	for _, disable := range []bool{false, true} {
		args := []string{"--server", "127.0.0.1:4096", "--web"}
		if disable {
			args = append(args, "--no-preview")
		}
		options, err := parseOptions(args)
		if err != nil || !options.web || options.noPreview != disable {
			t.Fatalf("options = %+v, %v", options, err)
		}
		view, err := newRichPreview(io.Discard, options.previewWidth, !options.noPreview, options.web)
		if err != nil {
			t.Fatal(err)
		}
		defer view.close()
		if view.enabled == disable || view.directory != "" || view.pdfPath != "" {
			t.Fatal("web/disabled preview created PDF storage or ignored --no-preview")
		}
	}
}
