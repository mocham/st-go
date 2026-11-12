package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/net/websocket"
)

func TestServiceAddressSurvivesViews(t *testing.T) {
	for _, test := range []struct{ url, root, id string }{
		{"https://example.org/nested/remote/", "https://example.org/nested/remote/", ""},
		{"https://example.org/nested/remote/api/ptys/abc/view/", "https://example.org/nested/remote/", "abc"},
		{"https://example.org/t/prefix/api/ptys/abc/view/", "https://example.org/t/prefix/", "abc"},
		{"https://example.org/t/token/", "", ""},
	} {
		r, err := newRemote(test.url, "")
		if err != nil {
			t.Fatal(err)
		}
		service, id := r.serviceTarget()
		if test.root == "" {
			if service != nil {
				t.Fatal("standalone URL treated as a service")
			}
			continue
		}
		if service == nil || service.base.String() != test.root || id != test.id {
			t.Fatalf("%s: service=%+v id=%q", test.url, service, id)
		}
		view := ptyInfo{ID: "next", View: service.resource("api/ptys/next/view/")}
		if err := r.selectView(view); err != nil {
			t.Fatal(err)
		}
		if service.base.String() != test.root {
			t.Fatal("selecting a tab mutated the service address")
		}
		for _, bad := range []ptyInfo{{ID: "../else", View: view.View}, {ID: "next", View: "https://other.invalid/api/ptys/next/view/"}, {ID: "next", View: "/wrong/api/ptys/next/view/"}} {
			if service.validatePTY(bad) == nil {
				t.Fatalf("accepted invalid tab: %+v", bad)
			}
		}
	}
}

func testTabs(t *testing.T) *app {
	t.Helper()
	r, _ := newRemote("http://localhost/service/", "")
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	a := &app{remote: r, ctx: ctx, out: io.Discard, reconnect: true, pending: map[string]pendingResource{}, resources: map[string]string{}, failed: map[string]bool{}, tabs: &serviceTabs{remote: r, results: make(chan serviceResult, 8)}}
	_ = a.screen.size(80, 22)
	return a
}

func tabFixture(id, title string) ptyInfo {
	return ptyInfo{ID: id, Title: title, View: "/service/api/ptys/" + id + "/view/"}
}

func TestClosedTabFallbackAndEmptyService(t *testing.T) {
	a := testTabs(t)
	one, two := tabFixture("one", "First"), tabFixture("two", "Second")
	a.tabs.entries = []ptyInfo{one, two}
	if err := a.selectTab(one.ID); err != nil {
		t.Fatal(err)
	}
	oldGeneration := a.generation
	a.pending["old"] = pendingResource{id: 1, cancel: func() {}}
	a.resources["old"] = "old.pdf"
	if err := a.serviceResult(serviceResult{kind: "list", entries: []ptyInfo{two}}); err != nil {
		t.Fatal(err)
	}
	if a.tabs.selected != "two" || a.remote.base.String() != "http://localhost/service/api/ptys/two/view/" || a.generation <= oldGeneration {
		t.Fatal("did not invalidate the dead attachment and select the next tab")
	}
	if len(a.pending) != 0 || len(a.resources) != 0 {
		t.Fatal("old preview work survived a tab switch")
	}
	if err := a.serviceResult(serviceResult{kind: "list"}); err != nil {
		t.Fatal(err)
	}
	if a.tabs.selected != "" || !a.retryAt.IsZero() || a.quit {
		t.Fatal("last PTY closure did not leave a usable service UI")
	}
	if err := a.paint(); err != nil {
		t.Fatal(err)
	}
	if err := a.serviceResult(serviceResult{kind: "list", entries: []ptyInfo{one}}); err != nil {
		t.Fatal(err)
	}
	if a.tabs.selected != "one" {
		t.Fatal("newly discovered PTY was not attachable")
	}
}

func TestTabBarWidthMouseAndKeys(t *testing.T) {
	a := testTabs(t)
	var out bytes.Buffer
	a.out = &out
	a.tabs.entries = []ptyInfo{tabFixture("one", "Long long first title"), tabFixture("two", "界界界界界 second"), tabFixture("three", "Third title")}
	_ = a.selectTab("three")
	_ = a.screen.size(30, 8)
	if err := a.drawTabs(); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, hit := range a.tabs.hits {
		if hit.id == "three" {
			found = true
		}
		if hit.left < 1 || hit.right > 31 {
			t.Fatalf("tab hit outside viewport: %+v", hit)
		}
	}
	if !found {
		t.Fatal("selected tab hidden on a narrow terminal")
	}
	_ = a.screen.size(80, 8)
	a.tabs.first = 0
	if err := a.drawTabs(); err != nil {
		t.Fatal(err)
	}
	var hit tabHit
	for _, h := range a.tabs.hits {
		if h.id == "two" {
			hit = h
		}
	}
	if hit.id == "" {
		t.Fatal("second tab not drawn")
	}
	if handled, err := a.tabInput(inputMessage{Type: "press", Button: 1, X: hit.left, Y: 9}); !handled || err != nil {
		t.Fatalf("tab click: %v %v", handled, err)
	}
	if a.tabs.selected != "two" {
		t.Fatal("tab click went to remote PTY")
	}
	if handled, err := a.tabInput(inputMessage{Type: "key", Key: "1", Modifiers: 8}); !handled || err != nil || a.tabs.selected != "one" {
		t.Fatal("Alt+1 did not select first tab")
	}
	_ = a.input(localEvent{escape: true})
	_ = a.input(localEvent{input: inputMessage{Type: "key", Key: "Tab"}})
	if a.tabs.selected != "two" {
		t.Fatal("prefix Tab did not select next tab")
	}
	text, width := tabText("界é\x1bBAD", 3)
	if text != "界é" || width != 3 {
		t.Fatalf("cell width clipping: %q %d", text, width)
	}
}

func TestOpenPTYAddsTabAndListCannotUndoIt(t *testing.T) {
	a := testTabs(t)
	one, two := tabFixture("one", "First"), tabFixture("two", "Second")
	a.tabs.entries = []ptyInfo{one}
	_ = a.selectTab("one")
	if err := a.message(serverMessage{Type: "open_pty", OpenPTY: &two}); err != nil {
		t.Fatal(err)
	}
	if len(a.tabs.entries) != 2 || a.tabs.selected != "two" {
		t.Fatal("application-spawned PTY replaced the service tab list")
	}
	// A list fetched before the application created the PTY must not remove it.
	a.tabs.busy = "create" // delay the coalesced refresh while inspecting state
	if err := a.serviceResult(serviceResult{kind: "list", revision: 0, entries: []ptyInfo{one}}); err != nil {
		t.Fatal(err)
	}
	if len(a.tabs.entries) != 2 || a.tabs.selected != "two" || !a.tabs.refreshAgain {
		t.Fatal("stale list removed the new tab")
	}
}

func TestTabCompletionsRespectNewerSelection(t *testing.T) {
	a := testTabs(t)
	one, two, three := tabFixture("one", "First"), tabFixture("two", "Second"), tabFixture("three", "Third")
	a.tabs.entries = []ptyInfo{one, two}
	a.tabs.listing = true
	_ = a.selectTab("one")
	selection := a.tabs.selection
	_ = a.selectTab("two")
	if err := a.serviceResult(serviceResult{kind: "create", selection: selection, pty: three}); err != nil {
		t.Fatal(err)
	}
	if a.tabs.selected != "two" || len(a.tabs.entries) != 3 {
		t.Fatal("late create stole focus from a newer tab selection")
	}
	if err := a.serviceResult(serviceResult{kind: "close", target: "one"}); err != nil {
		t.Fatal(err)
	}
	if a.tabs.selected != "two" || a.tabs.index("one") >= 0 {
		t.Fatal("late close affected the wrong tab")
	}
	a.tabs.refreshAgain = false
	if err := a.serviceResult(serviceResult{kind: "list", revision: a.tabs.revision, err: fmt.Errorf("network unavailable")}); err != nil {
		t.Fatal(err)
	}
	if a.tabs.selected != "two" || len(a.tabs.entries) != 2 {
		t.Fatal("network failure was mistaken for closed terminals")
	}
}

func TestOccupiedAndRecentlyReleasedTabs(t *testing.T) {
	a := testTabs(t)
	one, two := tabFixture("one", "First"), tabFixture("two", "Second")
	two.Occupied = true
	a.tabs.entries = []ptyInfo{one, two}
	a.tabs.listing = true
	_ = a.selectTab("one")
	_ = a.selectTab("two")
	if a.tabs.selected != "one" {
		t.Fatal("occupied tab displaced the current session")
	}
	a.tabs.released = map[string]time.Time{"two": time.Now().Add(time.Second)}
	_ = a.selectTab("two")
	if a.tabs.selected != "two" {
		t.Fatal("stale occupancy blocked switching back to our just-released tab")
	}
}

type tabTestService struct {
	mu      sync.Mutex
	entries []ptyInfo
	sockets map[string]*websocket.Conn
	serial  int
	events  chan string
}

func (s *tabTestService) serve(w http.ResponseWriter, r *http.Request) {
	const root = "/service/api/ptys"
	switch {
	case r.URL.Path == root && r.Method == "GET":
		s.mu.Lock()
		list := append([]ptyInfo{}, s.entries...)
		for i := range list {
			list[i].Occupied = s.sockets[list[i].ID] != nil
		}
		s.mu.Unlock()
		json.NewEncoder(w).Encode(list)
	case r.URL.Path == root && r.Method == "POST":
		if r.Header.Get("X-ST-Service-Request") != "1" {
			http.Error(w, "missing request header", 403)
			return
		}
		s.mu.Lock()
		s.serial++
		p := tabFixture(fmt.Sprintf("pty%d", s.serial), fmt.Sprintf("Shell %d", s.serial))
		s.entries = append(s.entries, p)
		s.mu.Unlock()
		json.NewEncoder(w).Encode(p)
		s.events <- "created:" + p.ID
	case strings.HasPrefix(r.URL.Path, root+"/") && r.Method == "DELETE":
		id := strings.TrimPrefix(r.URL.Path, root+"/")
		if r.Header.Get("X-ST-Attachment") != "lease-"+id || r.Header.Get("X-ST-Service-Request") != "1" {
			http.Error(w, "wrong attachment", 409)
			s.events <- "bad-close:" + id
			return
		}
		s.remove(id)
		w.WriteHeader(204)
		s.events <- "closed:" + id
	case strings.HasSuffix(r.URL.Path, "/attach"):
		id := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, root+"/"), "/attach")
		s.mu.Lock()
		exists := false
		for _, p := range s.entries {
			exists = exists || p.ID == id
		}
		occupied := s.sockets[id] != nil
		s.mu.Unlock()
		if !exists {
			http.NotFound(w, r)
			return
		}
		if occupied {
			http.Error(w, "occupied", 409)
			return
		}
		websocket.Handler(func(ws *websocket.Conn) {
			s.mu.Lock()
			s.sockets[id] = ws
			s.mu.Unlock()
			defer func() {
				ws.Close()
				s.mu.Lock()
				if s.sockets[id] == ws {
					delete(s.sockets, id)
				}
				s.mu.Unlock()
				s.events <- "detached:" + id
			}()
			_ = websocket.JSON.Send(ws, map[string]any{"type": "hello", "attachment": "lease-" + id, "cellWidth": 8, "cellHeight": 16, "heartbeatMS": 250})
			for {
				var m inputMessage
				if websocket.JSON.Receive(ws, &m) != nil {
					return
				}
				switch m.Type {
				case "paint_ack":
					if m.ID == 0 {
						s.events <- "attached:" + id
					}
				case "ping":
					_ = websocket.JSON.Send(ws, map[string]any{"type": "pong"})
				case "key":
					if m.Key == "e" {
						s.remove(id)
						s.events <- "exited:" + id
						return
					}
				}
			}
		}).ServeHTTP(w, r)
	default:
		http.NotFound(w, r)
	}
}

func (s *tabTestService) remove(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, p := range s.entries {
		if p.ID == id {
			s.entries = append(s.entries[:i], s.entries[i+1:]...)
			break
		}
	}
	if ws := s.sockets[id]; ws != nil {
		ws.Close()
		delete(s.sockets, id)
	}
}

func waitTabEvent(t *testing.T, s *tabTestService, want string) {
	t.Helper()
	timer := time.NewTimer(8 * time.Second)
	defer timer.Stop()
	for {
		select {
		case event := <-s.events:
			if strings.HasPrefix(event, "bad-close:") {
				t.Fatal(event)
			}
			if event == want {
				return
			}
		case <-timer.C:
			t.Fatal("missing service event:", want)
		}
	}
}

func TestPTYServiceTabsSurviveLastExit(t *testing.T) {
	s := &tabTestService{sockets: map[string]*websocket.Conn{}, events: make(chan string, 128)}
	server := httptest.NewServer(http.HandlerFunc(s.serve))
	defer server.Close()
	master, slave := openPTY(t, 80, 24)
	out, stderr := capture(), capture()
	done := make(chan struct{})
	status := 0
	go func() {
		defer close(done)
		status = run([]string{server.URL + "/service/api/ptys/already-closed/view/"}, slave, out, stderr)
	}()
	defer func() {
		master.Write([]byte("\x1dq"))
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Error("client cleanup blocked")
		}
	}()
	out.await(t, func(text string) bool { return strings.Contains(text, "No available terminal") })
	master.Write([]byte("\x1dn"))
	waitTabEvent(t, s, "attached:pty1")
	master.Write([]byte("\x1dn"))
	waitTabEvent(t, s, "attached:pty2")
	master.Write([]byte("\x1d1"))
	waitTabEvent(t, s, "attached:pty1")
	s.mu.Lock()
	ids := []string{}
	for _, p := range s.entries {
		ids = append(ids, p.ID)
	}
	s.mu.Unlock()
	if !reflect.DeepEqual(ids, []string{"pty1", "pty2"}) {
		t.Fatalf("switching killed a PTY: %v", ids)
	}
	master.Write([]byte("\x1dx"))
	waitTabEvent(t, s, "closed:pty1")
	waitTabEvent(t, s, "attached:pty2")
	// Exiting inside the remote shell, rather than using Close, removes the
	// last PTY. The same client must remain usable and create a replacement.
	master.Write([]byte("e"))
	waitTabEvent(t, s, "exited:pty2")
	out.await(t, func(text string) bool { return strings.Count(text, "No available terminal") >= 2 })
	select {
	case <-done:
		t.Fatalf("last PTY exit killed the client: %d %s", status, stderr.text())
	default:
	}
	master.Write([]byte("\x1dn"))
	waitTabEvent(t, s, "attached:pty3")
	master.Write([]byte("\x1dq"))
	select {
	case <-done:
		if status != 0 {
			t.Fatalf("client failed: %s", stderr.text())
		}
	case <-time.After(5 * time.Second):
		t.Fatal("quit blocked")
	}
	s.mu.Lock()
	remaining := len(s.entries)
	s.mu.Unlock()
	if remaining != 1 {
		t.Fatal("quit killed remaining service PTY")
	}
}
