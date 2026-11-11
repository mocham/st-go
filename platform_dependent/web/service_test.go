package web

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/net/websocket"
	"st-go/platform_neutral/api"
)

func testService(t *testing.T, origin string) *Service {
	t.Helper()
	s, err := NewService(api.WindowOptions{Cols: 8, Rows: 4, CellWidth: 2, CellHeight: 2}, Options{BasePath: "/tools/terminal/", Origin: origin, MaxPTYs: 2, AttachmentTimeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	s.defaults = api.TabSpawnRequest{Command: []string{"/bin/sh"}, Directory: "/"}
	s.factory = func(host api.Host, request api.TabSpawnRequest, _ api.SessionSpawn) (func(), error) {
		d := host.(*Driver)
		var mu sync.Mutex
		d.Bind(api.Callbacks{Lock: mu.Lock, Unlock: mu.Unlock, Closed: func() bool {
			select {
			case <-d.done:
				return true
			default:
				return false
			}
		},
			Text: func(string) {}, Focus: func(bool) {}, PointerRelease: func(byte, uint16, int, int) {}, PaintDirty: func() {}})
		d.SetTitle(request.Title)
		return d.Close, nil
	}
	t.Cleanup(s.Close)
	return s
}

func serviceRequest(s *Service, method, resource, origin, token, body string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, s.URL()+resource, strings.NewReader(body))
	r.Header.Set("Origin", origin)
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("X-ST-Attachment", token)
	w := httptest.NewRecorder()
	s.ServeHTTP(w, r)
	return w
}

func TestServicePrefixOriginAndMedia(t *testing.T) {
	s := testService(t, "https://terminal.example.com:443")
	if s.URL() != "https://terminal.example.com/tools/terminal/" {
		t.Fatal(s.URL())
	}
	entry, err := s.create(api.TabSpawnRequest{Title: "first"})
	if err != nil {
		t.Fatal(err)
	}
	base := "api/ptys/" + entry.id + "/view/"
	for _, resource := range []string{"", "healthz", "api/ptys", "service.js", base, base + "scene.js", base + "client.js", base + "style.css", base + "service.js", base + "pdfjs/build/pdf.mjs", base + "pdfjs/build/pdf.worker.mjs"} {
		w := serviceRequest(s, "GET", resource, "", "", "")
		if w.Code != 200 {
			t.Fatalf("%s: %d %s", resource, w.Code, w.Body.String())
		}
		if resource == base && (!strings.Contains(w.Body.String(), `data-prefix="/tools/terminal/"`) || strings.Contains(w.Body.String(), "<iframe")) {
			t.Fatal("service is not a top-level prefixed page")
		}
	}
	data := []byte("%PDF-1.7\nservice range fixture\n")
	id := entry.driver.RegisterMedia(api.MediaSource{Data: data})
	r := httptest.NewRequest("GET", s.URL()+base+fmt.Sprintf("media/%d", id), nil)
	r.Header.Set("Range", "bytes=0-7")
	w := httptest.NewRecorder()
	s.ServeHTTP(w, r)
	if w.Code != 206 || w.Body.String() != string(data[:8]) {
		t.Fatalf("prefixed PDF range: %d %q", w.Code, w.Body.String())
	}
	for _, test := range []struct {
		method, path, host, origin string
		code                       int
	}{
		{"GET", "/tools/terminal", "terminal.example.com", "", 308},
		{"GET", "/api/ptys", "terminal.example.com", "", 404},
		{"GET", "/tools/terminal//api/ptys", "terminal.example.com", "", 404},
		{"GET", "/tools/terminal/%61pi/ptys", "terminal.example.com", "", 404},
		{"GET", "/tools/terminal/", "evil.example", "", 403},
		{"POST", "/tools/terminal/api/ptys", "terminal.example.com", "https://evil.example", 403},
		{"POST", "/tools/terminal/api/ptys", "terminal.example.com", "", 403},
		{"GET", "/tools/terminal/api/ptys/" + entry.id + "/attach", "terminal.example.com", "http://terminal.example.com", 403},
		{"GET", "/tools/terminal/healthz", "127.0.0.1:8080", "", 200},
	} {
		r := httptest.NewRequest(test.method, "https://"+test.host+test.path, strings.NewReader("{}"))
		r.Header.Set("Origin", test.origin)
		w := httptest.NewRecorder()
		s.ServeHTTP(w, r)
		if w.Code != test.code {
			t.Errorf("%+v: got %d", test, w.Code)
		}
		if test.code == 308 && w.Header().Get("Location") != "/tools/terminal/" {
			t.Fatal("prefix redirect", w.Header())
		}
	}
	if w := serviceRequest(s, "POST", "api/ptys", s.origin, "", `{"command":["sh"],"unknown":1}`); w.Code != 400 {
		t.Fatal(w.Code)
	}
	if w := serviceRequest(s, "POST", "api/ptys", s.origin, "", `{} {}`); w.Code != 400 {
		t.Fatal(w.Code)
	}
	if w := serviceRequest(s, "POST", "api/ptys", s.origin, "", `{}`); w.Code != 201 || !strings.HasPrefix(w.Header().Get("Location"), s.prefix+"api/ptys/") {
		t.Fatal(w.Code, w.Body.String())
	}
	if w := serviceRequest(s, "POST", "api/ptys", s.origin, "", `{}`); w.Code != 400 {
		t.Fatal("admission limit", w.Code)
	}
}

func waitService(t *testing.T, description string, check func() bool) {
	t.Helper()
	end := time.Now().Add(4 * time.Second)
	for !check() {
		if time.Now().After(end) {
			t.Fatal("timed out:", description)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestServiceExclusiveLeaseAndMutations(t *testing.T) {
	s := testService(t, "")
	entry, err := s.create(api.TabSpawnRequest{})
	if err != nil {
		t.Fatal(err)
	}
	go s.server.Serve(s.listener)
	u, _ := url.Parse(s.URL())
	origin := "http://" + u.Host
	attach := "ws://" + u.Host + s.prefix + "api/ptys/" + entry.id + "/attach"
	const contenders = 12
	results := make(chan *websocket.Conn, contenders)
	start := make(chan struct{})
	for range contenders {
		go func() { <-start; c, _ := websocket.Dial(attach, "", origin); results <- c }()
	}
	close(start)
	var winner *websocket.Conn
	for range contenders {
		if c := <-results; c != nil {
			t.Cleanup(func() { c.Close() })
			if winner != nil {
				t.Fatal("two controllers acquired the same PTY")
			}
			winner = c
		}
	}
	if winner == nil {
		t.Fatal("no controller acquired the PTY")
	}
	winner.SetReadDeadline(time.Now().Add(5 * time.Second))
	var hello serverMessage
	if err := websocket.JSON.Receive(winner, &hello); err != nil || hello.Attachment == "" {
		t.Fatal("attachment hello", err)
	}
	resource := "api/ptys/" + entry.id
	if w := serviceRequest(s, "PATCH", resource, origin, "", `{"title":"stolen"}`); w.Code != 409 {
		t.Fatal("unowned rename", w.Code)
	}
	if w := serviceRequest(s, "DELETE", resource, origin, "", ""); w.Code != 409 {
		t.Fatal("unowned close", w.Code)
	}
	if w := serviceRequest(s, "PATCH", resource, origin, hello.Attachment, `{"title":"mine"}`); w.Code != 200 {
		t.Fatal("owner rename", w.Code)
	}
	readPong := func() {
		t.Helper()
		for {
			var data []byte
			if err := websocket.Message.Receive(winner, &data); err != nil {
				t.Fatal(err)
			}
			var m serverMessage
			if json.Unmarshal(data, &m) == nil && m.Type == "pong" {
				return
			}
		}
	}
	// Heartbeats keep a quiet PTY leased and provide upstream traffic for nginx.
	for range 3 {
		time.Sleep(400 * time.Millisecond)
		if err := websocket.JSON.Send(winner, inputMessage{Type: "ping"}); err != nil {
			t.Fatal(err)
		}
		readPong()
	}
	if !s.info(entry).Occupied {
		t.Fatal("heartbeat did not renew attachment")
	}
	second, err := s.create(api.TabSpawnRequest{})
	if err != nil {
		t.Fatal(err)
	}
	other, err := websocket.Dial("ws://"+u.Host+s.prefix+"api/ptys/"+second.id+"/attach", "", origin)
	if err != nil {
		t.Fatal("different PTY unavailable", err)
	}
	other.Close()
	waitService(t, "clean detach", func() bool { return !s.info(second).Occupied })
	entry.driver.mu.Lock()
	stale := entry.driver.client
	entry.driver.mu.Unlock()
	waitService(t, "silent controller expiry", func() bool { return !s.info(entry).Occupied })
	if w := serviceRequest(s, "DELETE", resource, origin, hello.Attachment, ""); w.Code != 409 {
		t.Fatal("expired mutation accepted", w.Code)
	}
	if err := entry.driver.dispatch(stale, inputMessage{Type: "text", Text: "late"}); err == nil {
		t.Fatal("expired input accepted")
	}
	select {
	case <-entry.driver.done:
		t.Fatal("expiry killed terminal")
	default:
	}
	fresh, err := websocket.Dial(attach, "", origin)
	if err != nil {
		t.Fatal("reattach", err)
	}
	defer fresh.Close()
	fresh.SetReadDeadline(time.Now().Add(time.Second))
	var next serverMessage
	if err := websocket.JSON.Receive(fresh, &next); err != nil || next.Attachment == hello.Attachment {
		t.Fatal("lease not rotated", err)
	}
	if w := serviceRequest(s, "DELETE", resource, origin, hello.Attachment, ""); w.Code != 409 {
		t.Fatal("old token displaced current controller", w.Code)
	}
	if w := serviceRequest(s, "DELETE", resource, origin, next.Attachment, ""); w.Code != 204 {
		t.Fatal(w.Code)
	}
	second.close()
	waitService(t, "empty service", func() bool {
		w := serviceRequest(s, "GET", "api/ptys", "", "", "")
		return strings.TrimSpace(w.Body.String()) == "[]"
	})
	if w := serviceRequest(s, "GET", "healthz", "", "", ""); w.Code != 200 {
		t.Fatal("zero PTYs unhealthy")
	}
	if w := serviceRequest(s, "POST", "api/ptys", origin, "", "{}"); w.Code != 201 {
		t.Fatal("could not create after last PTY closed", w.Code)
	}
}

func TestServiceNonBrowserMutationHeader(t *testing.T) {
	s := testService(t, "https://terminal.example.com")
	r := httptest.NewRequest("POST", s.URL()+"api/ptys", strings.NewReader("{}"))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("X-ST-Service-Request", "1")
	w := httptest.NewRecorder()
	s.ServeHTTP(w, r)
	if w.Code != 201 {
		t.Fatal(w.Code)
	}
	r = httptest.NewRequest("POST", s.URL()+"api/ptys", io.NopCloser(strings.NewReader("{}")))
	r.Header.Set("Origin", "https://evil.example")
	r.Header.Set("X-ST-Service-Request", "1")
	w = httptest.NewRecorder()
	s.ServeHTTP(w, r)
	if w.Code != 403 {
		t.Fatal("explicit bad Origin bypassed", w.Code)
	}
}

func TestServiceRejectsAmbiguousPrefixes(t *testing.T) {
	for _, prefix := range []string{"//", "relative/", "/a//b/", "/a/../b/", "/a%2fb/", "/a?b/"} {
		s, err := NewService(api.WindowOptions{Cols: 8, Rows: 4, CellWidth: 2, CellHeight: 2}, Options{BasePath: prefix})
		if err == nil {
			s.Close()
			t.Errorf("accepted prefix %q", prefix)
		}
	}
}
