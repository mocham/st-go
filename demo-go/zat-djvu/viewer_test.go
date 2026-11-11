package main

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"st-go/pdfmeta"
)

func queuedBackend() *djvuBackend {
	return &djvuBackend{wake: make(chan struct{}, 1), done: make(chan struct{}), results: make(chan backendResult, 8)}
}

func TestFragmentedInputAndReplies(t *testing.T) {
	var parser inputParser
	var keys, replies []string
	now := time.Now()
	input := "12G\x1b[A\x1bPregion secondary image-status 640 480 300000 100 zoom 0 0\x1b\\:open café.djvu\r\x1b[<64;3;4M\x1b[6~"
	for _, character := range []byte(input) {
		parser.feed([]byte{character}, now, func(key string) { keys = append(keys, key) }, func(reply string) { replies = append(replies, reply) })
	}
	want := []string{"1", "2", "G", "up", ":", "o", "p", "e", "n", " ", "c", "a", "f", "é", ".", "d", "j", "v", "u", "\r", "mouse:<64;3;4M", "pagedown"}
	if !reflect.DeepEqual(keys, want) || len(replies) != 1 || len(parser.pending) != 0 {
		t.Fatalf("keys=%q replies=%q pending=%q", keys, replies, parser.pending)
	}
	parser.feed([]byte("\x1b"), now, func(key string) { keys = append(keys, key) }, func(string) {})
	parser.expire(now.Add(time.Second), func(key string) { keys = append(keys, key) })
	if keys[len(keys)-1] != "escape" {
		t.Fatal("standalone Escape was not resolved")
	}
}

func TestRenderedPageUsesDirectRequestedScale(t *testing.T) {
	var output bytes.Buffer
	backend := queuedBackend()
	viewer := newViewer(options{file: "book.djvu", page: 2, zoom: 120}, backend, 8, &output)
	viewer.requestRender(2)
	if len(backend.queue) != 1 || backend.queue[0].kind != jobRender || backend.queue[0].page != 2 {
		t.Fatalf("render queue = %#v", backend.queue)
	}
	path := "/tmp/page;'quoted'.png"
	viewer.backendReply(backendResult{kind: jobRender, token: viewer.renderToken, page: 2, path: path, width: 1200, height: 1600})
	written := output.String()
	if strings.Count(written, "\x1bP") != 1 || !strings.Contains(written, "viewer zoom 120") || !strings.Contains(written, "'\"'\"'") {
		t.Fatalf("render command = %q", written)
	}
	viewer.reply("region secondary image-status 1200 1600 250000 120 zoom 0 0")
	if viewer.position != 250000 || viewer.zoom != 120 || viewer.loading() {
		t.Fatalf("image status was not applied: %+v", viewer)
	}
}

func TestViewerPageNavigationCoalesces(t *testing.T) {
	backend := queuedBackend()
	viewer := newViewer(options{file: "book.djvu", page: 1, zoom: 100}, backend, 20, io.Discard)
	viewer.page = 1
	viewer.key("J")
	viewer.key("J")
	viewer.key("5")
	viewer.key("G")
	if viewer.page != 5 || len(backend.queue) != 1 || backend.queue[0].page != 5 {
		t.Fatalf("page=%d queue=%#v", viewer.page, backend.queue)
	}
	viewer.position = 320000
	viewer.key("m")
	viewer.key("a")
	viewer.key("G")
	viewer.key("'")
	viewer.key("a")
	if viewer.page != 5 || viewer.renderPosition != 320000 {
		t.Fatalf("mark restored page=%d position=%d", viewer.page, viewer.renderPosition)
	}
}

func TestViewerCrossesRasterScrollEdges(t *testing.T) {
	backend := queuedBackend()
	viewer := newViewer(options{file: "book.djvu", page: 2, zoom: 100}, backend, 4, io.Discard)
	viewer.page, viewer.position = 2, 0
	viewer.reply("region secondary event scroll -1")
	if viewer.page != 1 || viewer.renderPosition != 1000000 || len(backend.queue) != 1 || backend.queue[0].page != 1 {
		t.Fatalf("previous edge: page=%d position=%d queue=%#v", viewer.page, viewer.renderPosition, backend.queue)
	}
	viewer.reply("region secondary event scroll 1")
	if viewer.page != 2 || viewer.renderPosition != 0 || len(backend.queue) != 1 || backend.queue[0].page != 2 {
		t.Fatalf("next edge: page=%d position=%d queue=%#v", viewer.page, viewer.renderPosition, backend.queue)
	}
	viewer.page = viewer.pages
	backend.queue = nil
	viewer.reply("region secondary event scroll 1")
	if len(backend.queue) != 0 || viewer.page != viewer.pages {
		t.Fatal("scrolling past the final page queued another render")
	}
}

func TestSearchOccurrencesAndTextPane(t *testing.T) {
	var output bytes.Buffer
	backend := queuedBackend()
	viewer := newViewer(options{file: "book.djvu", page: 2, zoom: 100}, backend, 3, &output)
	viewer.page, viewer.cols, viewer.rows = 2, 20, 8
	viewer.search = "needle"
	viewer.find(false, false)
	viewer.backendReply(backendResult{kind: jobSearch, token: viewer.searchToken, occurrences: []searchOccurrence{{page: 2, local: 1}, {page: 2, local: 2}, {page: 3, local: 1}}})
	if viewer.searchActive != 0 || viewer.searchPageMatch != 1 || !strings.Contains(viewer.message, "Match 1/3") {
		t.Fatalf("initial search = active %d, local %d, %q", viewer.searchActive, viewer.searchPageMatch, viewer.message)
	}
	viewer.key("n")
	if viewer.searchActive != 1 || viewer.page != 2 {
		t.Fatal("same-page occurrence was skipped")
	}
	viewer.key("n")
	if viewer.page != 3 || len(backend.queue) == 0 || backend.queue[len(backend.queue)-1].kind != jobRender {
		t.Fatal("cross-page occurrence did not request its raster")
	}
	viewer.panel = panelText
	viewer.textPage = 3
	viewer.pageText = "needle filler needle"
	viewer.reflowText()
	viewer.updateTextSearch(true)
	viewer.panelDirty = true
	viewer.draw()
	if !strings.Contains(output.String(), "\x1b[30;48;5;214m") {
		t.Fatal("active hidden-text occurrence was not highlighted")
	}
}

func TestOutlineAndTextResultsAreLocal(t *testing.T) {
	var output bytes.Buffer
	backend := queuedBackend()
	viewer := newViewer(options{file: "book.djvu", page: 1, zoom: 100}, backend, 4, &output)
	viewer.page, viewer.cols, viewer.rows = 1, 30, 10
	viewer.key("\t")
	if !viewer.tocPending || !strings.Contains(output.String(), "region split 65%") {
		t.Fatal("outline pane did not request local metadata")
	}
	viewer.backendReply(backendResult{kind: jobTOC, token: viewer.tocToken, toc: []pdfmeta.Entry{{Title: "Chapter", Page: 3}}})
	viewer.draw()
	if !strings.Contains(output.String(), "Chapter") {
		t.Fatal("outline was not drawn")
	}
	viewer.key("\t")
	viewer.key("backtab")
	viewer.backendReply(backendResult{kind: jobText, token: viewer.textToken, page: 1, text: "Selectable café 世界"})
	output.Reset()
	viewer.draw()
	if !strings.Contains(output.String(), "Selectable café 世界") || strings.Contains(output.String(), "\x1b[31m") {
		t.Fatalf("hidden text pane = %q", output.String())
	}
}

func TestOptionsAndSafeChrome(t *testing.T) {
	path := generatedDjVu(t)
	options, err := parseOptions([]string{"-page", "1", "-zoom", "120", path}, io.Discard)
	if err != nil || options.page != 1 || options.zoom != 120 {
		t.Fatalf("options=%+v err=%v", options, err)
	}
	for _, args := range [][]string{{}, {"-zoom", "501", path}, {"-page", "0", path}, {"-bg", "oops", path}, {t.TempDir()}} {
		if _, err := parseOptions(args, io.Discard); err == nil {
			t.Errorf("accepted %q", args)
		}
	}
	if got := statusText("a\x1b界\n", 3); got != "a??" {
		t.Fatalf("unsafe chrome: %q", got)
	}
	control := filepath.Join(t.TempDir(), "bad\n.djvu")
	if err := os.WriteFile(control, []byte("AT&TFORM"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := validateDjVu(control); err == nil {
		t.Fatal("control-containing path was accepted")
	}
}

func TestSearchAndTextHelpers(t *testing.T) {
	if got := expandSearch(`caf\'e {alpha}`); got != "café α" {
		t.Fatalf("expanded search = %q", got)
	}
	if got := textSearchHits("CAFÉ café", "café"); !reflect.DeepEqual(got, []textHit{{0, 5}, {6, 11}}) {
		t.Fatalf("hits = %#v", got)
	}
	if got := panelLines("ab世界cd", 4); !reflect.DeepEqual(got, []string{"ab世", "界cd"}) {
		t.Fatalf("wrapped text = %q", got)
	}
}
