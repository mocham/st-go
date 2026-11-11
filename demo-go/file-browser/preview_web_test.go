package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"st-go/platform_neutral/api"
	"st-go/ptyutil"
)

func TestWebHTMLDocuments(t *testing.T) {
	dir := t.TempDir()
	for _, test := range []struct{ name, source, want string }{
		{"note.md", "# Web markdown\n\n$x^2$\n", "Web markdown"},
		{"note.tex", "\\documentclass{article}\n\\begin{document}Web latex $x^2$\\end{document}", "Web latex"},
		{"note.html", "<h1 style='color:red'>Web HTML</h1>", "style='color:red'"},
		{"note.xhtml", "<h1>Web XHTML</h1>", "Web XHTML"},
	} {
		t.Run(test.name, func(t *testing.T) {
			path := filepath.Join(dir, test.name)
			if err := os.WriteFile(path, []byte(test.source), 0600); err != nil {
				t.Fatal(err)
			}
			data, err := renderWebHTML(context.Background(), path)
			var document api.HTMLDocument
			if err != nil || json.Unmarshal(data, &document) != nil || !strings.Contains(document.HTML, test.want) {
				t.Fatalf("HTML render: %s, %v", data, err)
			}
			if strings.HasSuffix(test.name, ".tex") || strings.HasSuffix(test.name, ".md") {
				if !strings.Contains(document.HTML, "<svg") {
					t.Fatal("math did not render to SVG")
				}
			}
		})
	}
}

func TestWebPreviewWorkerCancellation(t *testing.T) {
	ctx, stop := context.WithCancel(context.Background())
	defer stop()
	jobs := make(chan webPreviewJob, 1)
	results := make(chan webPreviewResult, 1)
	started, release, finished := make(chan struct{}), make(chan struct{}), make(chan struct{})
	go func() {
		defer close(finished)
		webPreviewWorker(ctx, jobs, results, func(_ context.Context, path string) ([]byte, error) {
			if path == "old" {
				close(started)
				<-release
			}
			return []byte(path), nil
		})
	}()
	old, cancel := context.WithCancel(ctx)
	jobs <- webPreviewJob{old, 1, "old"}
	<-started
	cancel()
	jobs <- webPreviewJob{ctx, 2, "latest"}
	close(release)
	select {
	case result := <-results:
		if result.generation != 2 || string(result.data) != "latest" {
			t.Fatal("stale conversion was published", result)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("replacement conversion did not finish")
	}
	stop()
	<-finished

	var output bytes.Buffer
	b := &Browser{out: bufio.NewWriter(&output), webPreview: webPreviewState{generation: 4, results: results}}
	results <- webPreviewResult{generation: 3, data: []byte(`{"html":"stale"}`)}
	b.pollWebPreview()
	b.flush()
	if output.Len() != 0 {
		t.Fatal("already queued stale result reached the terminal")
	}
}

func TestWebBrowserPTYPreviewAndPinnedPanel(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "a.html"), []byte("<h1>Browser HTML</h1>"), 0600); err != nil {
		t.Fatal(err)
	}
	f := startBrowser(t, append(os.Environ(), "ST_GO_BACKEND=web"), "--web", dir)
	f.send("\x1b[B") // parent -> HTML
	waitContains(t, f, "region overlay 1 3 45 21", "pinned HTML rectangle")
	waitContains(t, f, "region secondary html ", "HTML publication")
	before := len(f.output())
	f.send("\x1b[Z\t") // Shift+Tab must not unpin; Tab hides the panel.
	waitFor(t, f, func() bool { return strings.Contains(f.output()[before:], "region overlay 1 3 80 21") }, "hidden panel expands preview")
	before = len(f.output())
	f.send("\t")
	waitFor(t, f, func() bool { return strings.Contains(f.output()[before:], "region overlay 1 3 45 21") }, "shown panel remains pinned")
	before = len(f.output())
	if err := ptyutil.SetWinSize(f.master, 30, 100); err != nil {
		t.Fatal(err)
	}
	waitFor(t, f, func() bool { return strings.Contains(f.output()[before:], "region overlay 1 3 65 27") }, "resized pinned preview")
	before = len(f.output())
	f.send("/")
	waitFor(t, f, func() bool { return strings.Contains(f.output()[before:], "region reset") }, "path popup removes overlay")
}

func TestWebMediaLinksRemainStableAcrossSelections(t *testing.T) {
	b := &Browser{cacheDir: t.TempDir()}
	first := filepath.Join(t.TempDir(), "quote'\";\x1b\\image.png")
	second := filepath.Join(t.TempDir(), "next.png")
	a, err := b.webMediaLink(first)
	if err != nil {
		t.Fatal(err)
	}
	c, err := b.webMediaLink(second)
	if err != nil || a == c || strings.ContainsAny(filepath.Base(a), "'\";\x1b") {
		t.Fatal("media links are mutable or unsafe", a, c, err)
	}
	if target, err := os.Readlink(a); err != nil || target != first {
		t.Fatal("next selection changed the pending media path", target, err)
	}
}
