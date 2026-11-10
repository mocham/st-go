package web

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"image"
	"image/png"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/net/websocket"
	"st-go/platform_neutral/api"
)

func testDriver(t *testing.T) *Driver {
	t.Helper()
	d, err := New(api.WindowOptions{Cols: 8, Rows: 4, CellWidth: 2, CellHeight: 2}, Options{Port: 0})
	if err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	d.Bind(api.Callbacks{Lock: mu.Lock, Unlock: mu.Unlock, Closed: func() bool { return false }, Expose: func() {}, ExposeSurface: func(api.Surface) {},
		KeyPress: func(uint, uint16) {}, Text: func(string) {}, PointerPress: func(byte, uint16, int, int) {}, PointerRelease: func(byte, uint16, int, int) {}, PointerMotion: func(uint16, int, int) {},
		Resize: func(int, int) {}, Focus: func(bool) {}, Close: d.Close, PaintDirty: func() {}, Paste: func(string) {}})
	t.Cleanup(d.Close)
	return d
}

func TestSharedClipboardIsOptInAcrossWebSessions(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cache.sqlite3")
	newPeer := func() *Driver {
		d, err := New(api.WindowOptions{Cols: 8, Rows: 4, CellWidth: 2, CellHeight: 2, CachePath: path}, Options{Port: 0})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(d.Close)
		return d
	}
	first, second := newPeer(), newPeer()
	first.SetClipboard('c', "private")
	second.QueryClipboard('c', func(_ string, ok bool) {
		if ok {
			t.Error("disabled web sessions shared clipboard state")
		}
	})
	if !first.SetClipboardSync(true) || !second.SetClipboardSync(true) {
		t.Fatal("could not enable shared clipboard")
	}
	deadline := time.Now().Add(2 * time.Second)
	for (!first.ClipboardSyncState().Ready || !second.ClipboardSyncState().Ready) && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	first.SetClipboard('c', "shared 世界")
	for {
		reply := make(chan struct {
			text string
			ok   bool
		}, 1)
		second.QueryClipboard('c', func(text string, ok bool) {
			reply <- struct {
				text string
				ok   bool
			}{text, ok}
		})
		result := <-reply
		if result.ok && result.text == "shared 世界" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("enabled web sessions did not converge", result)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func runDriver(t *testing.T, d *Driver) {
	t.Helper()
	done := make(chan struct{})
	go func() { defer close(done); d.Run() }()
	t.Cleanup(func() {
		d.Close()
		select {
		case <-done:
		case <-time.After(3 * time.Second):
			t.Error("server failed to stop")
		}
	})
}

func dial(t *testing.T, d *Driver) *websocket.Conn {
	t.Helper()
	u, _ := url.Parse(d.URL())
	config, err := websocket.NewConfig("ws://"+u.Host+u.Path+"ws", "http://"+u.Host)
	if err != nil {
		t.Fatal(err)
	}
	conn, err := websocket.DialConfig(config)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	conn.SetReadDeadline(time.Now().Add(3 * time.Second))
	var hello serverMessage
	if err := websocket.JSON.Receive(conn, &hello); err != nil || hello.Type != "hello" || hello.CellWidth != 2 {
		t.Fatalf("hello=%+v, %v", hello, err)
	}
	return conn
}

func TestEmbeddedAssetsAndRequestIsolation(t *testing.T) {
	d := testDriver(t)
	u, _ := url.Parse(d.URL())
	for _, test := range []struct {
		path, origin, host string
		status             int
	}{
		{u.Path, "", u.Host, 200}, {u.Path + "client.js", "", u.Host, 200}, {u.Path + "style.css", "", u.Host, 200},
		{"/", "", u.Host, 404}, {"/t/wrong/", "", u.Host, 404}, {u.Path + "../../etc/passwd", "", u.Host, 404},
		{u.Path, "", "attacker.example:" + u.Port(), 403}, {u.Path + "ws", "http://attacker.example", u.Host, 403}, {u.Path + "ws", "", u.Host, 403},
	} {
		req := httptest.NewRequest("GET", "http://"+u.Host+test.path, nil)
		req.Host = test.host
		if test.origin != "" {
			req.Header.Set("Origin", test.origin)
		}
		out := httptest.NewRecorder()
		d.ServeHTTP(out, req)
		if out.Code != test.status {
			t.Errorf("%s host=%s origin=%s: %d want %d", test.path, test.host, test.origin, out.Code, test.status)
		}
		if out.Header().Get("Referrer-Policy") != "no-referrer" {
			t.Fatal("missing token privacy header")
		}
	}
}

func TestDefaultHTTPPortHostNormalization(t *testing.T) {
	d := testDriver(t)
	d.port = 80
	for _, host := range []string{"127.0.0.1", "127.0.0.1:80", "localhost", "[::1]"} {
		if !d.validHost(host) {
			t.Errorf("default-port Host %q rejected", host)
		}
	}
	for _, host := range []string{"attacker.example", "localhost:8080", "localhost@attacker.example"} {
		if d.validHost(host) {
			t.Errorf("unsafe Host %q accepted", host)
		}
	}
}

func TestPixelDamageAndRetainedSurfaces(t *testing.T) {
	d := testDriver(t)
	pixels := make([]uint32, d.width*d.height)
	for i := range pixels {
		pixels[i] = 0xff112233
	}
	d.Upload(pixels, d.width, 0, 0, d.width, d.height)
	pixels[0] = 0xff000000
	frames := d.frames()
	if len(frames) != 1 || frames[0].pixels[0] != 0xff112233 {
		t.Fatal("upload did not copy the caller's pixels")
	}
	data, err := encodeFrame(frames[0])
	if err != nil {
		t.Fatal(err)
	}
	if string(data[:4]) != "STW1" || binary.LittleEndian.Uint32(data[4:8]) != uint32(d.width) {
		t.Fatal("invalid frame header")
	}
	decoded, err := png.Decode(bytes.NewReader(data[28:]))
	if err != nil {
		t.Fatal(err)
	}
	r, g, b, _ := decoded.At(0, 0).RGBA()
	if r>>8 != 0x11 || g>>8 != 0x22 || b>>8 != 0x33 {
		t.Fatal("ARGB to PNG conversion changed colors")
	}
	for _, y := range []int{0, 4} {
		d.Upload(pixels, d.width, 0, y, 2, 1)
	}
	frames = d.frames()
	if len(frames) != 2 {
		t.Fatalf("separate damage became %d rectangles", len(frames))
	}
	d.Upload(pixels, d.width, 0, 0, 6, 6)
	d.Upload(pixels, d.width, 1, 1, 6, 6)
	if frames := d.frames(); len(frames) != 2 {
		t.Fatal("overlapping damage expanded into previously clean pixels")
	}
	for i := range 20 {
		d.Upload(pixels, d.width, i%5, i%3, 10, 5)
	}
	frames = d.frames()
	area := 0
	for _, frame := range frames {
		area += len(frame.pixels)
	}
	if area > d.width*d.height {
		t.Fatal("overlapping damage exceeded snapshot memory budget")
	}
	view, err := d.NewSurface(image.Rect(2, 2, 4, 4), 0xff000000)
	if err != nil {
		t.Fatal(err)
	}
	view.Present([]uint32{0xff00ff00, 0xff00ff00, 0xff00ff00, 0xff00ff00}, 2, 2, 0, false)
	d.Upload(pixels, d.width, 0, 0, d.width, d.height)
	frames = d.frames()
	if frames[0].pixels[2*d.width+2] != 0xff00ff00 {
		t.Fatal("terminal erase overwrote retained preview")
	}
	view.Close()
	frames = d.frames()
	if len(frames) == 0 || frames[0].pixels[0] != 0xff112233 {
		t.Fatal("closing surface did not restore underlying terminal pixels")
	}
}

func TestSocketInputClipboardReconnectAndShutdown(t *testing.T) {
	d := testDriver(t)
	events := make(chan string, 8)
	d.callbacks.Text = func(text string) { events <- text }
	d.callbacks.KeyPress = func(key uint, mods uint16) {
		if key == 0xfe20 && mods == 1 {
			events <- "shift-tab"
		}
	}
	d.callbacks.Resize = func(w, h int) { events <- "resize" }
	runDriver(t, d)
	conn := dial(t, d)
	u, _ := url.Parse(d.URL())
	request, _ := http.NewRequest("GET", d.URL()+"ws", nil)
	request.Header.Set("Origin", "http://"+u.Host)
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusConflict {
		t.Fatal("second controller was not rejected")
	}
	var frame []byte
	if err := websocket.Message.Receive(conn, &frame); err != nil || len(frame) < 28 || string(frame[:4]) != "STW1" {
		t.Fatalf("initial pixels: %v", err)
	}
	for _, m := range []inputMessage{{Type: "text", Text: "café 世界"}, {Type: "key", Key: "Tab", Modifiers: 1}, {Type: "resize", Width: 32, Height: 16}} {
		if err := websocket.JSON.Send(conn, m); err != nil {
			t.Fatal(err)
		}
	}
	for _, want := range []string{"café 世界", "shift-tab", "resize"} {
		select {
		case got := <-events:
			if got != want {
				t.Fatalf("input=%q want %q", got, want)
			}
		case <-time.After(2 * time.Second):
			t.Fatal("input not dispatched")
		}
	}
	replies := make(chan string, 1)
	d.callbacks.Lock()
	d.QueryClipboard('c', func(text string, ok bool) {
		if ok {
			replies <- text
		}
	})
	d.callbacks.Unlock()
	var query serverMessage
	for query.Type != "clipboard_read" {
		var data []byte
		if err := websocket.Message.Receive(conn, &data); err != nil {
			t.Fatal(err)
		}
		if len(data) > 0 && data[0] == '{' {
			if err := json.Unmarshal(data, &query); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := websocket.JSON.Send(conn, inputMessage{Type: "clipboard_reply", ID: query.ID, Text: "browser clipboard", OK: true}); err != nil {
		t.Fatal(err)
	}
	select {
	case got := <-replies:
		if got != "browser clipboard" {
			t.Fatal(got)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("clipboard reply lost")
	}
	conn.Close()
	deadline := time.Now().Add(2 * time.Second)
	for {
		d.mu.Lock()
		active := d.active
		d.mu.Unlock()
		if !active {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("disconnect retained controller")
		}
		time.Sleep(time.Millisecond)
	}
	second := dial(t, d)
	if err := websocket.Message.Receive(second, &frame); err != nil {
		t.Fatal(err)
	}
	if binary.LittleEndian.Uint32(frame[4:8]) != 32 {
		t.Fatal("reconnect lost viewport state")
	}
	d.Close()
	second.SetReadDeadline(time.Now().Add(time.Second))
	for {
		if err := websocket.Message.Receive(second, &frame); err != nil {
			break
		}
	}
}

func TestClipboardFailureFinishesOnceWithoutBlockingInput(t *testing.T) {
	d := testDriver(t)
	c := &client{controls: make(chan serverMessage, 4)}
	d.client = c
	defer func() { d.mu.Lock(); d.client = nil; d.mu.Unlock() }()
	replies := 0
	reply := func(text string, ok bool) {
		replies++
		if ok || text != "" {
			t.Error("failed read returned clipboard text")
		}
	}
	d.callbacks.Lock()
	d.QueryClipboard('c', reply)
	query := <-c.controls
	// A different browser and stale IDs must not complete this request.
	d.finishQuery(&client{}, query.ID, "wrong browser", true)
	d.finishQuery(c, query.ID+1, "stale reply", true)
	if replies != 0 {
		t.Fatal("accepted stale clipboard reply")
	}
	d.finishQuery(c, query.ID, "", false)
	d.finishQuery(c, query.ID, "duplicate", true)
	d.callbacks.Unlock()
	if replies != 1 {
		t.Fatalf("clipboard finished %d times", replies)
	}
	input := ""
	d.callbacks.Text = func(text string) { input = text }
	if err := d.dispatch(c, inputMessage{Type: "text", Text: "still typing"}); err != nil || input != "still typing" {
		t.Fatal("clipboard denial blocked terminal input")
	}
}

func TestInvalidGeometryAndInputAreRejected(t *testing.T) {
	d := testDriver(t)
	c := &client{pressed: make(map[byte]inputMessage)}
	for _, m := range []inputMessage{{Type: "resize", Width: 1 << 30, Height: 1 << 30}, {Type: "press", Button: 0}, {Type: "key", Key: "not a key"}, {Type: "text", Text: string([]byte{0xff})}, {Type: "text", Text: strings.Repeat("x", 32769)}} {
		if err := d.dispatch(c, m); err == nil {
			t.Fatalf("accepted %+v", m)
		}
	}
	w, h := d.ScreenSize()
	d.RequestWindowRect(api.Rect{Width: 1, Height: 1})
	w2, h2 := d.ScreenSize()
	if d.SupportsWindowGeometry() || w != w2 || h != h2 {
		t.Fatal("browser honored desktop placement")
	}
	if _, err := New(api.WindowOptions{Cols: 1, Rows: 1, CellWidth: 1, CellHeight: 1}, Options{Port: -1}); err == nil {
		t.Fatal("accepted invalid port")
	}
}

func TestBrowserAssetsUseDevicePixelViewportWithoutRasterScaling(t *testing.T) {
	client, err := assets.ReadFile("assets/client.js")
	if err != nil {
		t.Fatal(err)
	}
	style, err := assets.ReadFile("assets/style.css")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(client, []byte("ResizeObserver")) || !bytes.Contains(client, []byte(`type: "resize"`)) || !bytes.Contains(client, []byte("viewport.clientWidth * ratio")) || !bytes.Contains(client, []byte("viewport.clientHeight * ratio")) {
		t.Fatal("browser client does not size its backing buffer to viewport device pixels")
	}
	if !bytes.Contains(client, []byte(`screen.style.width = width / ratio + "px"`)) || !bytes.Contains(client, []byte(`screen.style.height = height / ratio + "px"`)) || !bytes.Contains(client, []byte("window.devicePixelRatio")) {
		t.Fatal("canvas does not map source pixels to browser device pixels")
	}
	if !bytes.Contains(style, []byte("overflow: auto")) {
		t.Fatal("fixed-size canvas is not scrollable")
	}
}

func TestHTTPClientCanFetchSelfContainedPage(t *testing.T) {
	d := testDriver(t)
	runDriver(t, d)
	response, err := http.Get(d.URL())
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	body, _ := io.ReadAll(response.Body)
	if response.StatusCode != 200 || !bytes.Contains(body, []byte("<canvas")) || bytes.Contains(body, []byte("https://")) {
		t.Fatal("page requires external assets or has no canvas")
	}
}
