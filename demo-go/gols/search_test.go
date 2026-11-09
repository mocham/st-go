package main

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"st-go/limits"
	"st-go/pdfmeta"
)

func searchTestBrowser(t *testing.T) (*Browser, *testOutput) {
	t.Helper()
	opened, err := openCorpus(writeTestCorpus(t))
	if err != nil {
		t.Fatal(err)
	}
	out := &testOutput{}
	b := &Browser{fd: -1, in: &input{fd: -1}, out: out, corpora: []*corpus{opened}, running: true}
	if err := b.setup(); err != nil {
		opened.close()
		t.Fatal(err)
	}
	t.Cleanup(b.cleanup)
	file := filepath.Join(b.cacheDir, "synthetic.pdf")
	if err := os.WriteFile(file, []byte("%PDF-1.4\n"), 0600); err != nil {
		t.Fatal(err)
	}
	b.view = viewState{kind: viewTOC, corpusIndex: 0, course: "AA", documentID: 2}
	b.openedDocumentID = 2
	b.openedPreview = renderedPreview{path: file, pdf: true, pageCount: 3, toc: []previewTOCEntry{{Title: "First", Page: 1}, {Title: "Second", Page: 2}}}
	b.idx = 1
	b.buildEntries()
	b.renderAll()
	out.Reset()
	return b, out
}

func sendSearchResult(t *testing.T, b *Browser, result pdfmeta.Search) {
	t.Helper()
	data, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	token := b.documentSearch.token
	for offset := 0; offset < len(data); {
		end := min(len(data), offset+pdfmeta.ChunkBytes)
		next := end
		if end == len(data) {
			next = -1
		}
		b.searchReply(fmt.Sprintf("pdfsearch data %d %d %d %s", token, offset, next, base64.StdEncoding.EncodeToString(data[offset:end])))
		offset = end
	}
}

func typeSearchKeys(b *Browser, text string) {
	for _, c := range []byte(text) {
		b.handleKey(c)
	}
}

func TestDocumentSearchShorthandHighlightAndOccurrences(t *testing.T) {
	b, out := searchTestBrowser(t)
	b.navigation.Visible = false
	typeSearchKeys(b, `/caf\'e {alpha}`+"\r")
	if !b.documentSearch.pending || !strings.Contains(out.String(), "'café α'") || b.view.kind != viewTOC {
		t.Fatalf("search command/state=%q %+v", out.String(), b.documentSearch)
	}
	sendSearchResult(t, b, pdfmeta.Search{Pages: []int{1, 1, 2}})
	if b.page != 1 || !strings.Contains(b.status, "Match 1/3 on page 1") || !strings.Contains(out.String(), fmt.Sprintf("pdfsearch %d 1", b.searchToken)) {
		t.Fatal("first occurrence not highlighted")
	}
	for _, test := range []struct {
		key          string
		active, page int
	}{{"n", 1, 1}, {"n", 2, 2}, {"n", 0, 1}, {"N", 2, 2}} {
		typeSearchKeys(b, test.key)
		if b.documentSearch.active != test.active || b.page != test.page {
			t.Fatalf("%q: %+v page%d", test.key, b.documentSearch, b.page)
		}
	}
	path := b.displayedPreview.path
	out.Reset()
	b.toggleNavigation()
	b.handleCSI('Z')
	b.renderAll()
	if b.displayedPreview.path != path || !b.documentSearch.visible || len(b.previewer.cache) != 0 {
		t.Fatal("navigation regenerated or discarded the searched PDF")
	}
	typeSearchKeys(b, ":noh\r")
	if b.documentSearch.visible || !strings.Contains(out.String(), "pdfsearch clear") {
		t.Fatal("nohlsearch did not clear")
	}
	out.Reset()
	b.handleKey('n')
	if !b.documentSearch.pending || !strings.Contains(out.String(), "'café α'") {
		t.Fatal("n did not restore the previous search")
	}
}

func TestDocumentSearchBackwardRepeatAndTOC(t *testing.T) {
	b, _ := searchTestBrowser(t)
	b.page = 2
	typeSearchKeys(b, "?needle\r")
	sendSearchResult(t, b, pdfmeta.Search{Pages: []int{1, 1, 2, 3}})
	if b.documentSearch.active != 2 || b.page != 2 || b.entries[b.idx].toc.Page != 2 {
		t.Fatal("backward anchor or TOC selection incorrect")
	}
	b.handleKey('n')
	if b.documentSearch.active != 1 {
		t.Fatal("backward n did not advance within the previous page")
	}
	b.handleKey('N')
	if b.documentSearch.active != 2 {
		t.Fatal("backward N did not reverse")
	}
	typeSearchKeys(b, "/\r")
	if b.documentSearch.query != "needle" || b.documentSearch.backward {
		t.Fatal("empty forward prompt did not reuse query")
	}
}

func TestDocumentSearchBoundsStaleChunksAndNoMatch(t *testing.T) {
	b, out := searchTestBrowser(t)
	b.findDocumentText(strings.Repeat("{alpha}", 200), false)
	if !b.documentSearch.pending {
		t.Fatal("budget applied before expansion")
	}
	old := b.documentSearch.token
	b.findDocumentText("new query", false)
	data := base64.StdEncoding.EncodeToString([]byte(`{"pages":[2]}`))
	b.searchReply(fmt.Sprintf("pdfsearch data %d 0 -1 %s", old, data))
	if !b.documentSearch.pending {
		t.Fatal("stale token accepted")
	}
	sendSearchResult(t, b, pdfmeta.Search{})
	if b.status != "Text not found" || !b.documentSearch.visible {
		t.Fatal("empty results were not reported")
	}
	out.Reset()
	b.findDocumentText(strings.Repeat("{alpha}", 600), false)
	if strings.Contains(out.String(), "pdfsearch query") {
		t.Fatal("oversized query sent")
	}
	b.findDocumentText("chunked", false)
	pages := make([]int, limits.PreviewSearchMatches)
	for i := range pages {
		pages[i] = 1
	}
	sendSearchResult(t, b, pdfmeta.Search{Pages: pages, Truncated: true})
	if len(b.documentSearch.pages) != len(pages) || !strings.Contains(out.String(), "pdfsearch chunk") || !strings.Contains(b.status, "limit reached") {
		t.Fatal("bounded chunked result lost matches or truncation notice")
	}
	b.findDocumentText("invalid", false)
	b.searchReply(fmt.Sprintf("pdfsearch data %d 0 -1 %s", b.documentSearch.token, base64.StdEncoding.EncodeToString([]byte(`{"pages":[2,1]}`))))
	if b.documentSearch.visible || !strings.Contains(b.status, "Invalid PDF search page") {
		t.Fatal("unsorted native results accepted")
	}
	b.findDocumentText("timeout", false)
	b.expireDocumentSearch(time.Now().Add(31 * time.Second))
	if b.documentSearch.pending || !strings.Contains(b.status, "updated st-go") {
		t.Fatal("missing terminal support left search stuck")
	}
}

func TestDocumentSearchCancelAndDocumentChange(t *testing.T) {
	b, out := searchTestBrowser(t)
	typeSearchKeys(b, "/query\x15café\x7f")
	if string(b.searchPromptBuffer) != "/caf" {
		t.Fatalf("UTF-8 editing=%q", b.searchPromptBuffer)
	}
	b.handleKey(0x03)
	if len(b.searchPromptBuffer) != 0 || !b.running || strings.Contains(out.String(), "pdfsearch query") {
		t.Fatal("Ctrl+C in prompt did not cancel cleanly")
	}
	b.findDocumentText("needle", false)
	old := b.documentSearch.token
	b.goBack()
	if b.documentSearch.visible || b.documentSearch.pending {
		t.Fatal("leaving document retained highlights")
	}
	b.searchReply(fmt.Sprintf("pdfsearch data %d 0 -1 e30=", old))
	if b.documentSearch.visible {
		t.Fatal("late reply resurrected old search")
	}
}

func TestSelectedDocumentSearchPinsPreviewVariant(t *testing.T) {
	b, _ := searchTestBrowser(t)
	b.view = viewState{kind: viewDocuments, corpusIndex: 0, course: "AA"}
	b.idx = 2
	b.buildEntries()
	b.renderAll()
	path := b.displayedPreview.path
	if path == "" || len(b.previewer.cache) != 1 {
		t.Fatal("missing selected-document PDF")
	}
	b.findDocumentText("needle", false)
	b.toggleNavigationPin()
	b.renderAll()
	if b.displayedPreview.path != path || len(b.previewer.cache) != 1 || !b.documentSearch.pending {
		t.Fatal("pinning navigation regenerated the searched page-size variant")
	}
}

func TestSearchPromptArrowDoesNotConsumeFollowingText(t *testing.T) {
	b, _ := searchTestBrowser(t)
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	defer writer.Close()
	b.in = &input{fd: int(reader.Fd())}
	b.startDocumentSearchPrompt('/')
	writer.Write([]byte("[Dx"))
	b.handleKey(0x1b)
	key, status := b.in.readByte(time.Millisecond)
	if status != inData || key != 'x' {
		t.Fatal("prompt arrow consumed the next typed character")
	}
	b.handleKey(key)
	if string(b.searchPromptBuffer) != "/x" {
		t.Fatalf("prompt=%q", b.searchPromptBuffer)
	}
}
