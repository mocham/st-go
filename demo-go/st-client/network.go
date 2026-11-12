package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path"
	"strings"
	"sync"
	"time"

	"golang.org/x/net/websocket"
)

type remote struct {
	base          *url.URL
	authorization string
	http          *http.Client
	generation    uint64
}

func newRemote(raw, authorization string) (*remote, error) {
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.RawPath != "" {
		return nil, fmt.Errorf("URL must be an http(s) service or terminal-view URL without credentials, query, or fragment")
	}
	if !strings.HasSuffix(u.Path, "/") {
		u.Path += "/"
	}
	if path.Clean(u.Path) != strings.TrimSuffix(u.Path, "/") && u.Path != "/" {
		return nil, fmt.Errorf("URL path must be canonical")
	}
	r := &remote{base: u, authorization: authorization}
	r.http = &http.Client{Timeout: 60 * time.Second, CheckRedirect: func(req *http.Request, via []*http.Request) error {
		if len(via) >= 5 || req.URL.Scheme != u.Scheme || req.URL.Host != u.Host {
			return fmt.Errorf("unexpected resource redirect")
		}
		return nil
	}}
	return r, nil
}

func (r *remote) resource(name string) string {
	return r.base.ResolveReference(&url.URL{Path: name}).String()
}

func (r *remote) request(ctx context.Context, method, address string, body any, limit int64) ([]byte, error) {
	return r.requestAttached(ctx, method, address, body, limit, "")
}

type httpError struct {
	code   int
	status string
}

func (e *httpError) Error() string { return "server returned " + e.status }

func (r *remote) requestAttached(ctx context.Context, method, address string, body any, limit int64, attachment string) ([]byte, error) {
	var data []byte
	var err error
	if body != nil {
		data, err = json.Marshal(body)
		if err != nil {
			return nil, err
		}
	}
	req, err := http.NewRequestWithContext(ctx, method, address, bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	if r.authorization != "" {
		req.Header.Set("Authorization", r.authorization)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if method != "GET" {
		req.Header.Set("X-ST-Service-Request", "1")
	}
	if attachment != "" {
		req.Header.Set("X-ST-Attachment", attachment)
	}
	response, err := r.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return nil, &httpError{response.StatusCode, response.Status}
	}
	data, err = io.ReadAll(io.LimitReader(response.Body, limit+1))
	if len(data) > int(limit) {
		return nil, fmt.Errorf("response exceeds %d-byte limit", limit)
	}
	return data, err
}

func (r *remote) isView() bool {
	_, id := r.serviceTarget()
	return id != "" || r.standalone()
}

func (r *remote) standalone() bool {
	parts := strings.Split(strings.Trim(r.base.Path, "/"), "/")
	return len(parts) == 2 && parts[0] == "t" && parts[1] != ""
}

// Keep the service address independently from its currently attached view.
// Managed deep links retain the full reverse-proxy prefix, even after deletion.
func (r *remote) serviceTarget() (*remote, string) {
	if r.standalone() {
		return nil, ""
	}
	service := *r
	u := *r.base
	id := ""
	if i := strings.LastIndex(u.Path, "/api/ptys/"); i >= 0 {
		parts := strings.Split(strings.TrimSuffix(u.Path[i+len("/api/ptys/"):], "/"), "/")
		if len(parts) == 2 && parts[1] == "view" && validPTYID(parts[0]) {
			id = parts[0]
			u.Path = u.Path[:i+1]
		}
	}
	service.base = &u
	return &service, id
}

func validPTYID(id string) bool {
	if id == "" || len(id) > 128 {
		return false
	}
	for _, r := range id {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_') {
			return false
		}
	}
	return true
}

func (r *remote) validatePTY(info ptyInfo) error {
	if !validPTYID(info.ID) {
		return fmt.Errorf("invalid service PTY ID")
	}
	u, err := url.Parse(info.View)
	if err != nil {
		return err
	}
	u = r.base.ResolveReference(u)
	if u.String() != r.resource("api/ptys/"+info.ID+"/view/") {
		return fmt.Errorf("invalid terminal view returned by service")
	}
	return nil
}

func (r *remote) selectView(info ptyInfo) error {
	u, err := url.Parse(info.View)
	if err != nil {
		return err
	}
	u = r.base.ResolveReference(u)
	if u.Scheme != r.base.Scheme || u.Host != r.base.Host || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.RawPath != "" || !strings.HasSuffix(u.Path, "/view/") {
		return fmt.Errorf("invalid terminal view returned by server")
	}
	r.base = u
	return nil
}

func (r *remote) sessions(ctx context.Context) ([]ptyInfo, error) {
	data, err := r.request(ctx, "GET", r.resource("api/ptys"), nil, 1<<20)
	if err != nil {
		return nil, err
	}
	var list []ptyInfo
	err = json.Unmarshal(data, &list)
	return list, err
}

type wireEvent struct {
	connection *connection
	message    serverMessage
	err        error
}

type connection struct {
	ws   *websocket.Conn
	out  chan inputMessage
	done chan struct{}
	once sync.Once
}

func (c *connection) close() { c.once.Do(func() { close(c.done); c.ws.Close() }) }
func (c *connection) send(m inputMessage) error {
	select {
	case <-c.done:
		return io.EOF
	default:
	}
	select {
	case c.out <- m:
		return nil
	case <-c.done:
		return io.EOF
	case <-time.After(5 * time.Second):
		return fmt.Errorf("connection input queue stalled")
	}
}

func (r *remote) connect(ctx context.Context, events chan<- wireEvent) (*connection, error) {
	endpoint := "ws"
	if strings.HasSuffix(r.base.Path, "/view/") {
		endpoint = "../attach"
	}
	u, _ := url.Parse(r.resource(endpoint))
	if u.Scheme == "https" {
		u.Scheme = "wss"
	} else {
		u.Scheme = "ws"
	}
	origin := r.base.Scheme + "://" + r.base.Host
	config, err := websocket.NewConfig(u.String(), origin)
	if err != nil {
		return nil, err
	}
	if r.authorization != "" {
		config.Header.Set("Authorization", r.authorization)
	}
	dialCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	ws, err := config.DialContext(dialCtx)
	if err != nil {
		return nil, err
	}
	ws.MaxPayloadBytes = maxMessage
	c := &connection{ws: ws, out: make(chan inputMessage, 128), done: make(chan struct{})}
	emit := func(event wireEvent) {
		select {
		case events <- event:
		case <-ctx.Done():
		}
	}
	go func() {
		defer c.close()
		for {
			select {
			case <-ctx.Done():
				return
			case <-c.done:
				return
			case m := <-c.out:
				ws.SetWriteDeadline(time.Now().Add(5 * time.Second))
				if err := websocket.JSON.Send(ws, m); err != nil {
					emit(wireEvent{connection: c, err: err})
					return
				}
			}
		}
	}()
	return c, nil
}

func (c *connection) read(ctx context.Context, events chan<- wireEvent) {
	defer c.close()
	for {
		var data []byte
		c.ws.SetReadDeadline(time.Now().Add(60 * time.Second))
		err := websocket.Message.Receive(c.ws, &data)
		var message serverMessage
		if err == nil {
			if !json.Valid(data) {
				err = fmt.Errorf("server sent a raster frame; st-client requires semantic-scene st-web")
			} else {
				err = json.Unmarshal(data, &message)
			}
		}
		select {
		case events <- wireEvent{c, message, err}:
		case <-ctx.Done():
			return
		}
		if err != nil {
			return
		}
	}
}
