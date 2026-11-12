package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"image"
	"image/png"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"st-go/config"
	"st-go/platform_neutral/api"
	term "st-go/platform_neutral/stterminal"
)

func TestFragmentedInput(t *testing.T) {
	wire := "a世\x03\x1b[1;5A\x1b[<64;8;4M\x1b[200~pasted\x1dq\n\x1b[201~\x1bPregion secondary status 1 3 0 100 0 0\x1b\\\x1b]52;c;eA==\a\x1d"
	want := []localEvent{
		{input: inputMessage{Type: "key", Key: "a"}}, {input: inputMessage{Type: "text", Text: "世"}},
		{input: inputMessage{Type: "key", Key: "C", Modifiers: 4}}, {input: inputMessage{Type: "key", Key: "ArrowUp", Modifiers: 4}},
		{input: inputMessage{Type: "press", Button: 4, X: 8, Y: 4}}, {input: inputMessage{Type: "release", Button: 4, X: 8, Y: 4}},
		{input: inputMessage{Type: "paste", Text: "pasted\x1dq\n"}},
		{reply: "region secondary status 1 3 0 100 0 0"}, {reply: "52;c;eA=="}, {escape: true},
	}
	for split := 0; split <= len(wire); split++ {
		var p inputParser
		var got []localEvent
		emit := func(e localEvent) { got = append(got, e) }
		p.feed([]byte(wire[:split]), time.Now(), emit)
		p.feed([]byte(wire[split:]), time.Now(), emit)
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("split %d: %#v", split, got)
		}
	}
	var p inputParser
	var got []localEvent
	for _, b := range []byte(wire) {
		p.feed([]byte{b}, time.Now(), func(e localEvent) { got = append(got, e) })
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("bytewise: %#v", got)
	}
	now := time.Now()
	p.feed([]byte{27}, now, func(localEvent) { t.Fatal("early Escape") })
	p.expire(now.Add(time.Second), func(e localEvent) {
		if e.input.Key != "Escape" {
			t.Fatalf("expiry: %+v", e)
		}
	})
}

// Feed the emitted ANSI/DCS to the actual local terminal parser. Embedding
// Hooks makes an unexpected host operation fail rather than silently pass.
type renderHooks struct {
	term.Hooks
	opens      []term.RegionImage
	navigation []term.RegionNavigation
}

func (*renderHooks) SetMode(bool, uint)                                 {}
func (*renderHooks) SetCursor(int) bool                                 { return true }
func (*renderHooks) ImageClearAll()                                     {}
func (*renderHooks) SetPointerMotion(bool)                              {}
func (*renderHooks) SetTitle(string)                                    {}
func (*renderHooks) SetIconTitle(string)                                {}
func (*renderHooks) LoadCols()                                          {}
func (*renderHooks) StartDraw() bool                                    { return false }
func (*renderHooks) SetRegionScrollReporting(term.RegionID, bool) error { return nil }
func (*renderHooks) SetRegions([]term.DisplayRegion) error              { return nil }
func (*renderHooks) Regions() []term.DisplayRegion                      { return nil }
func (*renderHooks) ClearRegion(term.RegionID) error                    { return nil }
func (h *renderHooks) OpenRegion(_ term.RegionID, r term.RegionImage) error {
	h.opens = append(h.opens, r)
	return nil
}
func (*renderHooks) SetRegionOverlay(term.CellRect) error { return nil }
func (h *renderHooks) NavigateRegion(_ term.RegionID, n term.RegionNavigation) error {
	h.navigation = append(h.navigation, n)
	return nil
}
func (*renderHooks) ImageDecode(_ []byte, opts term.ImageDecodeOptions) (int, int, []term.Glyph, bool) {
	g := make([]term.Glyph, opts.ViewCols*opts.ViewRows)
	for i := range g {
		g[i] = term.Glyph{U: term.ImageRune}
	}
	return opts.ViewCols, opts.ViewRows, g, true
}
func localTerm(cols, rows int) (*term.Term, *renderHooks) {
	cfg := config.Default()
	cfg.Cols, cfg.Rows = uint(cols), uint(rows)
	h := &renderHooks{}
	return term.NewTerm(cfg, h), h
}

func TestSceneRenderingRetainsAndErases(t *testing.T) {
	s := screen{cw: 8, ch: 16, border: 2}
	if err := s.size(12, 3); err != nil {
		t.Fatal(err)
	}
	w, h := s.dimensions()
	row := api.SceneRow{X: 2, Y: 2, Width: 96, Height: 16, Background: 0xff112233, Text: []api.TextRun{
		{X: 2, Text: "A", Advance: 8, Foreground: 0xffabcdef},
		{X: 10, Text: "界", Advance: 16, Foreground: 0xffabcdef},
		{X: 26, Text: "e\u0301", Cluster: true, Advance: 8, Foreground: 0xffabcdef},
		{X: 34, Text: "Z", Advance: 8, Foreground: 0xffabcdef},
	}}
	m := serverMessage{Width: w, Height: h, Scenes: []scene{{ID: 1, Width: w, Height: h, Update: api.SceneUpdate{Reset: true, Rows: []api.SceneRow{row}, Fills: []api.SceneFill{{X: 0, Y: 0, Width: w, Height: 2}, {X: 0, Y: 0, Width: 2, Height: h}}}}}}
	core, _ := localTerm(12, 3)
	paint := func() {
		t.Helper()
		if err := s.apply(m); err != nil {
			t.Fatal(err)
		}
		var out bytes.Buffer
		if err := s.draw(&out); err != nil {
			t.Fatal(err)
		}
		core.Twrite(out.Bytes(), false)
	}
	paint()
	if text := core.LineText(0); !strings.HasPrefix(text, "A界éZ") {
		t.Fatalf("wide/combining/border rendering: %q", text)
	}
	// Only the second row changes; the first row must survive.
	m.Scenes[0].Update = api.SceneUpdate{Rows: []api.SceneRow{{Y: 18, Height: 16, Text: []api.TextRun{{X: 2, Text: "second", Advance: 8}}}}}
	paint()
	if !strings.HasPrefix(core.LineText(0), "A界éZ") || !strings.HasPrefix(core.LineText(1), "second") {
		t.Fatal("lost retained rows")
	}
	// Replacing a wide glyph with two blanks must erase its continuation too.
	m.Scenes[0].Update = api.SceneUpdate{Rows: []api.SceneRow{{Y: 2, Height: 16, Text: []api.TextRun{{X: 2, Text: "x", Advance: 8}}}}}
	paint()
	if strings.TrimSpace(core.LineText(0)) != "x" {
		t.Fatalf("stale wide glyph: %q", core.LineText(0))
	}
	m.Scenes[0].Hidden = true
	paint()
	if strings.TrimSpace(core.LineText(1)) != "" {
		t.Fatal("hidden surface remained visible")
	}
}

func TestInlineMediaClipsDeduplicatesAndClears(t *testing.T) {
	s := screen{cw: 8, ch: 16}
	_ = s.size(8, 3)
	path := t.TempDir() + "/sample.png"
	if err := os.WriteFile(path, []byte{0x89, 'P', 'N', 'G'}, 0600); err != nil {
		t.Fatal(err)
	}
	s.imagePaths = map[uint64]string{7: path}
	m := serverMessage{Width: 64, Height: 48, Scenes: []scene{{ID: 1, Width: 64, Height: 48, Update: api.SceneUpdate{Reset: true, Rows: []api.SceneRow{
		{Y: 0, Height: 16, Media: []api.MediaPlacement{{ID: 7, Width: 32, Height: 32, ClipWidth: 32, ClipHeight: 16}}},
		{Y: 16, Height: 16, Text: []api.TextRun{{X: 16, Text: "OK", Advance: 8}}, Media: []api.MediaPlacement{{ID: 7, Width: 32, Height: 32, ClipWidth: 16, ClipY: 16, ClipHeight: 16}}},
	}}}}}
	if err := s.apply(m); err != nil {
		t.Fatal(err)
	}
	core, _ := localTerm(8, 3)
	var out bytes.Buffer
	if err := s.draw(&out); err != nil {
		t.Fatal(err)
	}
	if strings.Count(out.String(), "\x1bPopen ") != 1 {
		t.Fatalf("duplicate image placements: %q", out.String())
	}
	core.Twrite(out.Bytes(), false)
	if !strings.Contains(core.LineText(1), "OK") {
		t.Fatalf("image overwrote clipped text: %q", core.LineText(1))
	}
	out.Reset()
	_ = s.draw(&out)
	if strings.Contains(out.String(), "\x1bPopen ") {
		t.Fatal("unchanged image reopened")
	}
	m.Scenes[0].Update = api.SceneUpdate{Reset: true}
	_ = s.apply(m)
	out.Reset()
	_ = s.draw(&out)
	core.Twrite(out.Bytes(), false)
	for y := 0; y < 3; y++ {
		if strings.TrimSpace(core.LineText(y)) != "" {
			t.Fatalf("stale image row: %q", core.LineText(y))
		}
	}
}

func TestPreviewNavigationAndStaleStatus(t *testing.T) {
	var out bytes.Buffer
	r, _ := newRemote("http://localhost/t/token/", "")
	a := app{remote: r, out: &out, hello: true, resources: map[string]string{}, c: &connection{done: make(chan struct{}), out: make(chan inputMessage, 16)}}
	job := a.mediaJob(9)
	a.resources[job.key] = "/safe/a'b;doc.pdf"
	p := &previewState{job: job, rect: image.Rect(3, 2, 15, 9), media: api.MediaView{ID: 9, Generation: 2, PositionID: 3, Page: 4}}
	a.preview = p
	if err := a.presentPreview(); err != nil {
		t.Fatal(err)
	}
	core, h := localTerm(30, 12)
	core.Twrite(out.Bytes(), false)
	if len(h.opens) != 1 || h.opens[0].Path != "/safe/a'b;doc.pdf" || !h.opens[0].Continuous {
		t.Fatalf("DCS path round trip: %+v", h.opens)
	}
	if len(h.navigation) != 1 || h.navigation[0].Kind != "page" || h.navigation[0].Value != 4 {
		t.Fatalf("page jump overridden: %+v", h.navigation)
	}
	if err := a.tick(time.Now()); err != nil {
		t.Fatal(err)
	}
	p.media.Generation++
	if err := a.localReply("region secondary status 4 6 500000 100 0 0"); err != nil {
		t.Fatal(err)
	}
	if len(a.c.out) != 0 {
		t.Fatal("stale generation sent media status")
	}
	p.nextInfo = time.Now()
	_ = a.tick(time.Now())
	_ = a.localReply("region secondary status 4 6 500000 100 0 0")
	select {
	case m := <-a.c.out:
		if m.MediaStatus == nil || m.MediaStatus.Generation != 3 {
			t.Fatalf("status=%+v", m)
		}
	default:
		t.Fatal("current status missing")
	}
}

func TestDocumentChunksCancellationAndFailures(t *testing.T) {
	var out bytes.Buffer
	r, _ := newRemote("http://localhost/t/token/", "")
	a := app{remote: r, out: &out, resources: map[string]string{}, failed: map[string]bool{}, c: &connection{done: make(chan struct{}), out: make(chan inputMessage, 16)}}
	a.resources[a.mediaJob(5).key] = "/safe/document.pdf"
	request := documentRequest{ID: 42, Media: 5, Query: api.DocumentQuery{Kind: "search", Text: "term"}}
	_ = a.documents(serverMessage{Documents: []documentRequest{request}})
	if err := a.startQuery(); err != nil {
		t.Fatal(err)
	}
	token := a.query.token
	data := []byte(`{"pages":[2,3]}`)
	if err := a.queryReply(fmt.Sprintf("pdfsearch data %d 0 5 %s", token, base64.StdEncoding.EncodeToString(data[:5]))); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), fmt.Sprintf("pdfsearch chunk %d 5", token)) {
		t.Fatal("next chunk not requested")
	}
	_ = a.queryReply(fmt.Sprintf("pdfsearch data %d 5 -1 %s", token, base64.StdEncoding.EncodeToString(data[5:])))
	m := <-a.c.out
	if m.ID != 42 || !bytes.Equal(m.Data, data) {
		t.Fatalf("document reply=%+v", m)
	}
	_ = a.documents(serverMessage{Documents: []documentRequest{request}})
	_ = a.startQuery()
	_ = a.documents(serverMessage{Cancelled: []uint64{42}})
	_ = a.queryReply(fmt.Sprintf("pdfsearch data %d 0 -1 %s", a.queryToken, base64.StdEncoding.EncodeToString(data)))
	if len(a.c.out) != 0 || a.query != nil {
		t.Fatal("cancelled query completed")
	}
	a.failed[a.mediaJob(5).key] = true
	_ = a.documents(serverMessage{Documents: []documentRequest{request}})
	_ = a.startQuery()
	select {
	case m := <-a.c.out:
		if !strings.Contains(string(m.Data), "unavailable") {
			t.Fatal(string(m.Data))
		}
	default:
		t.Fatal("failed resource left query hanging")
	}
}

func TestResourceCacheRoundTripAndAttachmentScope(t *testing.T) {
	doc := api.HTMLDocument{HTML: `<article id="a"><h1>Heading</h1><p>Some <em>semantic</em> text.</p></article>`, CSS: `#a { color: red; }`}
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		if r.Header.Get("Authorization") != "Bearer example" {
			t.Error("missing authorization")
		}
		json.NewEncoder(w).Encode(doc)
	}))
	defer server.Close()
	r, _ := newRemote(server.URL+"/t/token/", "Bearer example")
	ctx, cancel := context.WithCancel(context.Background())
	c, err := newCache(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { cancel(); c.close() }()
	job := makeResource(r, "html/abc", true, 80, 24)
	job.ctx = ctx
	path, err := c.load(job)
	if err != nil {
		t.Fatal(err)
	}
	pdf, _ := os.ReadFile(path)
	if !bytes.HasPrefix(pdf, []byte("%PDF-")) {
		t.Fatal("no local PDF")
	}
	path2, err := c.load(job)
	if err != nil || path2 != path || requests.Load() != 1 {
		t.Fatalf("cache miss: %v (%d requests)", err, requests.Load())
	}
	job = makeResource(r, "html/abc", true, 100, 24)
	job.ctx = ctx
	if _, err := c.load(job); err != nil {
		t.Fatal(err)
	}
	if requests.Load() != 1 {
		t.Fatal("resize refetched immutable source")
	}
	entry := c.entries[r.resource("html/abc")]
	raw, _ := os.ReadFile(entry.path)
	var restored api.HTMLDocument
	if err := json.Unmarshal(raw, &restored); err != nil || restored != doc {
		t.Fatalf("lost original semantics/CSS: %+v %v", restored, err)
	}
	before := makeResource(r, "media/1", false, 0, 0)
	r.generation++
	after := makeResource(r, "media/1", false, 0, 0)
	if before.key == after.key {
		t.Fatal("restarted service may alias numeric media IDs")
	}
	stop, abort := context.WithCancel(ctx)
	abort()
	job.ctx = stop
	if _, err := c.load(job); err == nil {
		t.Fatal("cancelled conversion accepted")
	}
}

func TestResourceHTTPBoundsAndOrigin(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/redirect" {
			http.Redirect(w, r, "http://example.invalid/", 302)
			return
		}
		io.WriteString(w, strings.Repeat("x", 32))
	}))
	defer server.Close()
	r, _ := newRemote(server.URL, "")
	if _, err := r.request(context.Background(), "GET", server.URL, nil, 8); err == nil {
		t.Fatal("unbounded body")
	}
	if _, err := r.request(context.Background(), "GET", server.URL+"/redirect", nil, 128); err == nil {
		t.Fatal("cross-origin redirect followed")
	}
	if err := r.selectView(ptyInfo{View: "https://example.invalid/api/ptys/1/view/"}); err == nil {
		t.Fatal("cross-origin attach allowed")
	}
	for _, u := range []string{"file:///etc/passwd", "https://user:secret@example.org/", "https://example.org/a/../b/"} {
		if _, err := newRemote(u, ""); err == nil {
			t.Fatalf("accepted URL %q", u)
		}
	}
}

func TestObsoleteResourcesCancelAndCoalesce(t *testing.T) {
	var body bytes.Buffer
	_ = png.Encode(&body, image.NewRGBA(image.Rect(0, 0, 1, 1)))
	started, cancelled := make(chan struct{}), make(chan struct{})
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		if strings.HasSuffix(r.URL.Path, "/1") {
			close(started)
			<-r.Context().Done()
			close(cancelled)
			return
		}
		w.Write(body.Bytes())
	}))
	defer server.Close()
	r, _ := newRemote(server.URL+"/t/token/", "")
	ctx, cancel := context.WithCancel(context.Background())
	cache, err := newCache(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { cancel(); cache.close() }()
	a := app{remote: r, ctx: ctx, cache: cache, hello: true, out: io.Discard, pending: map[string]pendingResource{}, resources: map[string]string{}, failed: map[string]bool{}}
	old := a.mediaJob(1)
	a.preview = &previewState{job: old}
	if err := a.reconcile(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("download not started")
	}
	if err := a.reconcile(); err != nil {
		t.Fatal(err)
	}
	if len(a.pending) != 1 || len(cache.jobs) != 0 {
		t.Fatal("duplicate fetch queued")
	}
	next := a.mediaJob(2)
	a.preview = &previewState{job: next}
	if err := a.reconcile(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-cancelled:
	case <-time.After(5 * time.Second):
		t.Fatal("obsolete request not cancelled")
	}
	for i := 0; i < 2; i++ {
		select {
		case result := <-cache.results:
			if err := a.resourceReady(result); err != nil {
				t.Fatal(err)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("replacement resource blocked")
		}
	}
	if a.resources[old.key] != "" || a.failed[old.key] || a.resources[next.key] == "" || requests.Load() != 2 {
		t.Fatalf("stale completion affected current resources: %+v", a.resources)
	}
	// Reusing a URL while an old completion is queued must check request ID.
	a.pending[next.key] = pendingResource{id: 99, cancel: func() {}}
	_ = a.resourceReady(resourceResult{id: 98, key: next.key, path: "stale"})
	if a.resources[next.key] == "stale" {
		t.Fatal("old request ID overwrote replacement")
	}
}
