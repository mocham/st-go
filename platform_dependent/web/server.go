// Package web presents the native terminal through an embedded HTTP/canvas
// client. One server hosts one PTY/session, with one attached browser at a time.
package web

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"embed"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"image"
	"log"
	"mime"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"golang.org/x/net/websocket"
	"st-go/limits"
	"st-go/platform_neutral/api"
	"st-go/platform_neutral/sharedclipboard"
)

//go:embed assets/*
var assets embed.FS

type Options struct {
	Listen            string
	Port              int
	Service           bool
	BasePath          string
	Origin            string
	AttachmentTimeout time.Duration
	MaxPTYs           int
}

func RegisterFlags(fs *flag.FlagSet) api.OpenHost {
	o := Options{}
	// The shared frontend removes -e and its command from os.Args while parsing.
	// Capture the original invocation so the detached service preserves it.
	args := append([]string(nil), os.Args[1:]...)
	child := initDaemonChild()
	var daemon bool
	fs.BoolVar(&daemon, "daemon", false, "detach a persistent service; print its PID and URL after startup")
	fs.StringVar(&o.Listen, "listen", "127.0.0.1", "web server bind IP (loopback by default)")
	fs.IntVar(&o.Port, "port", 8080, "web server port (0 chooses an available port)")
	fs.BoolVar(&o.Service, "service", false, "persistent shared PTY service for SSH or an authenticating proxy")
	fs.StringVar(&o.BasePath, "base-path", "/", "service URL path prefix")
	fs.StringVar(&o.Origin, "origin", "", "expected browser origin, e.g. https://terminal.example.com")
	fs.DurationVar(&o.AttachmentTimeout, "attachment-timeout", 90*time.Second, "release a browser attachment after this period without contact")
	fs.IntVar(&o.MaxPTYs, "max-ptys", 8, "maximum concurrent service PTYs")
	return func(window api.WindowOptions) (api.Host, error) {
		if daemon {
			if !child {
				pid, url, err := launchDaemon(args)
				if err != nil {
					return nil, err
				}
				fmt.Printf("Web terminal daemon: PID %d %s\n", pid, url)
				os.Exit(0)
			}
			o.Service = true
		}
		if o.Service {
			host, err := NewService(window, o)
			if err != nil {
				reportDaemon("", err)
			}
			return host, err
		}
		if o.BasePath != "/" {
			return nil, fmt.Errorf("-base-path requires -service")
		}
		return New(window, o)
	}
}

type Driver struct {
	mu                sync.Mutex
	window            api.WindowOptions
	callbacks         api.Callbacks
	width, height     int
	pixels            []uint32
	textCursor        api.TextCursor
	surfaces          []*surface
	scenes            []*sceneSurface
	font              []byte
	media             map[uint64]*mediaResource
	mediaJobs         chan mediaJob
	nextMediaID       uint64
	mediaBytes        int
	documents         map[uint64]*documentJob
	nextDocumentID    uint64
	documentCancels   map[*client][]uint64
	nextSurfaceID     uint64
	htmlVersion       uint64
	dirty             []image.Rectangle
	wake              chan struct{}
	done              chan struct{}
	once              sync.Once
	listener          net.Listener
	server            *http.Server
	token, host       string
	port              int
	active            bool
	mutating          bool
	client            *client
	clipboard         map[byte]string
	clipboardVersion  uint64
	clipboardUpdate   serverMessage
	query             *clipboardQuery
	nextQuery         uint64
	clipboardSync     *sharedclipboard.Sync
	syncVersion       uint64
	syncUpdate        serverMessage
	origin            string
	attachmentTimeout time.Duration
	managed           bool
}

var _ api.Host = (*Driver)(nil)

func New(window api.WindowOptions, o Options) (*Driver, error) {
	return newDriver(window, o, true)
}

func newDriver(window api.WindowOptions, o Options, bind bool) (*Driver, error) {
	if o.Listen == "" {
		o.Listen = "127.0.0.1"
	}
	ip := net.ParseIP(o.Listen)
	if ip == nil {
		return nil, fmt.Errorf("listen must be an IP address")
	}
	if o.Port < 0 || o.Port > 65535 {
		return nil, fmt.Errorf("port must be between 0 and 65535")
	}
	if o.AttachmentTimeout == 0 {
		o.AttachmentTimeout = 90 * time.Second
	}
	if o.AttachmentTimeout < time.Second || o.AttachmentTimeout > 24*time.Hour {
		return nil, fmt.Errorf("attachment timeout must be between 1s and 24h")
	}
	if o.Origin != "" {
		origin, err := canonicalOrigin(o.Origin)
		if err != nil {
			return nil, err
		}
		o.Origin = origin
	}
	if window.CellWidth < 1 || window.CellHeight < 1 || window.Border < 0 || window.Border > 65535 || !limits.Grid(window.Cols, window.Rows) {
		return nil, fmt.Errorf("invalid terminal geometry")
	}
	if _, ok := limits.Product("web window cells", limits.PixelBytes, window.Cols, window.Rows, window.CellWidth, window.CellHeight, 4); !ok {
		return nil, fmt.Errorf("web window exceeds pixel budget")
	}
	w, h := window.Cols*window.CellWidth+2*window.Border, window.Rows*window.CellHeight+2*window.Border
	if _, ok := limits.RGBA("web window", w, h); !ok || w > 65535 || h > 65535 {
		return nil, fmt.Errorf("invalid web window dimensions")
	}
	var secret [32]byte
	if _, err := rand.Read(secret[:]); err != nil {
		return nil, err
	}
	var listener net.Listener
	if bind {
		var err error
		listener, err = net.Listen("tcp", net.JoinHostPort(o.Listen, strconv.Itoa(o.Port)))
		if err != nil {
			return nil, err
		}
		o.Port = listener.Addr().(*net.TCPAddr).Port
	}
	d := &Driver{window: window, width: w, height: h, pixels: make([]uint32, w*h), wake: make(chan struct{}, 1), done: make(chan struct{}),
		listener: listener, token: hex.EncodeToString(secret[:]), host: o.Listen, port: o.Port, clipboard: make(map[byte]string), origin: o.Origin, attachmentTimeout: o.AttachmentTimeout}
	d.clipboardSync = sharedclipboard.New(window.CachePath, sharedclipboard.Hooks{Changed: d.sharedClipboardChanged, Status: d.sharedClipboardStatus})
	d.server = &http.Server{Handler: d, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 15 * time.Second, IdleTimeout: 30 * time.Second, MaxHeaderBytes: 8192}
	return d, nil
}

func (d *Driver) URL() string {
	host := d.host
	if ip := net.ParseIP(host); ip != nil && ip.IsUnspecified() {
		if ip.To4() != nil {
			host = "127.0.0.1"
		} else {
			host = "::1"
		}
	}
	origin := d.origin
	if origin == "" {
		origin = "http://" + net.JoinHostPort(host, strconv.Itoa(d.port))
	}
	return origin + "/t/" + d.token + "/"
}
func (d *Driver) Bind(c api.Callbacks) { d.callbacks = c }
func (d *Driver) Show() {
	if !d.managed {
		fmt.Printf("Web terminal: %s\n", d.URL())
	}
}
func (d *Driver) SetupKeys()                    {}
func (d *Driver) BackendName() string           { return "web" }
func (d *Driver) SetTitle(title string)         { d.mu.Lock(); d.window.Title = title; d.mu.Unlock() }
func (d *Driver) SetIconTitle(string)           {}
func (d *Driver) SupportsWindowGeometry() bool  { return false }
func (d *Driver) RequestWindowRect(api.Rect)    {}
func (d *Driver) ScreenSize() (int, int)        { d.mu.Lock(); defer d.mu.Unlock(); return d.width, d.height }
func (d *Driver) ClientSize() (int, int, error) { w, h := d.ScreenSize(); return w, h, nil }
func (d *Driver) WindowRect() (api.Rect, error) {
	w, h := d.ScreenSize()
	return api.Rect{Width: w, Height: h}, nil
}

func (d *Driver) Run() {
	if d.listener == nil {
		<-d.done
		return
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	served := make(chan error, 1)
	go func() { served <- d.server.Serve(d.listener) }()
	select {
	case <-d.done:
	case <-ctx.Done():
		d.callbacks.Lock()
		d.callbacks.Close()
		d.callbacks.Unlock()
	case err := <-served:
		if !errors.Is(err, http.ErrServerClosed) {
			log.Printf("web server: %v", err)
		}
		d.callbacks.Lock()
		d.callbacks.Close()
		d.callbacks.Unlock()
	}
	d.Close()
}

func (d *Driver) Close() {
	d.once.Do(func() {
		if d.clipboardSync != nil {
			d.clipboardSync.Close()
		}
		close(d.done)
		d.mu.Lock()
		client := d.client
		for _, r := range d.media {
			r.cancel()
		}
		clear(d.media)
		clear(d.documents)
		clear(d.documentCancels)
		if d.query != nil {
			d.query.timer.Stop()
			d.query = nil
		}
		d.mu.Unlock()
		if client != nil {
			client.conn.Close()
		}
		if d.server != nil {
			d.server.Close()
		}
		if d.listener != nil {
			d.listener.Close()
		}
	})
}

func (d *Driver) validHost(host string) bool {
	if d.origin != "" {
		u, _ := url.Parse(d.origin)
		if strings.ContainsAny(host, "/?#@") {
			return false
		}
		origin, err := canonicalOrigin(u.Scheme + "://" + host)
		return err == nil && origin == d.origin
	}
	name, port, err := net.SplitHostPort(host)
	if err != nil {
		// Browsers omit the default HTTP port from Host, even if :80 was
		// present in the printed URL.
		if d.port != 80 {
			return false
		}
		name, port = strings.TrimSuffix(strings.TrimPrefix(host, "["), "]"), "80"
	}
	if port != strconv.Itoa(d.port) {
		return false
	}
	if strings.EqualFold(name, "localhost") {
		return true
	}
	ip := net.ParseIP(name)
	bound := net.ParseIP(d.host)
	return ip != nil && (bound.IsUnspecified() || ip.Equal(bound) || bound.IsLoopback() && ip.IsLoopback())
}

func (d *Driver) sameOrigin(r *http.Request) bool {
	if d.origin != "" {
		origin, err := canonicalOrigin(r.Header.Get("Origin"))
		return err == nil && origin == d.origin
	}
	origin, err := url.Parse(r.Header.Get("Origin"))
	return err == nil && origin.Scheme == "http" && origin.Host == r.Host && origin.User == nil && origin.RawQuery == "" && origin.Fragment == "" && (origin.Path == "" || origin.Path == "/")
}

func (d *Driver) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	securityHeaders(w)
	if !d.validHost(r.Host) {
		http.Error(w, "invalid host", http.StatusForbidden)
		return
	}
	parts := strings.SplitN(r.URL.Path, "/", 4)
	if len(parts) != 4 || parts[1] != "t" || subtle.ConstantTimeCompare([]byte(parts[2]), []byte(d.token)) != 1 {
		http.NotFound(w, r)
		return
	}
	d.serveResource(w, r, parts[3])
}

func securityHeaders(w http.ResponseWriter) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Security-Policy", "default-src 'none'; script-src 'self' 'wasm-unsafe-eval'; worker-src 'self' blob:; font-src 'self' blob:; style-src 'self' 'unsafe-inline'; connect-src 'self'; img-src 'self' blob: data:; frame-src 'self'; base-uri 'none'; frame-ancestors 'none'")
}

func (d *Driver) serveResource(w http.ResponseWriter, r *http.Request, resource string) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET")
		http.Error(w, "GET required", http.StatusMethodNotAllowed)
		return
	}
	if resource == "ws" {
		if !d.sameOrigin(r) {
			http.Error(w, "same origin required", http.StatusForbidden)
			return
		}
		d.mu.Lock()
		select {
		case <-d.done:
			d.mu.Unlock()
			http.Error(w, "terminal closed", http.StatusGone)
			return
		default:
		}
		if d.active || d.mutating {
			d.mu.Unlock()
			http.Error(w, "a browser is already attached", http.StatusConflict)
			return
		}
		d.active = true
		d.mu.Unlock()
		defer func() { d.mu.Lock(); d.active = false; d.mu.Unlock() }()
		websocket.Server{Handshake: func(_ *websocket.Config, r *http.Request) error {
			if !d.sameOrigin(r) {
				return fmt.Errorf("invalid origin")
			}
			return nil
		}, Handler: d.serveSocket}.ServeHTTP(w, r)
		return
	}
	name, contentType := "", ""
	if strings.HasPrefix(resource, "pdfjs/") {
		data, err := assets.ReadFile("assets/" + resource)
		if err != nil {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", mime.TypeByExtension(filepath.Ext(resource)))
		w.Header().Set("Cache-Control", "private, max-age=86400")
		w.Write(data)
		return
	}
	if strings.HasPrefix(resource, "media/") {
		d.serveMedia(w, r, resource)
		return
	}
	if strings.HasPrefix(resource, "html/") {
		d.serveHTML(w, r, resource)
		return
	}
	if resource == "font.ttf" {
		d.mu.Lock()
		font := d.font
		d.mu.Unlock()
		w.Header().Set("Content-Type", "font/ttf")
		w.Header().Set("Cache-Control", "private, max-age=86400")
		w.Write(font)
		return
	}
	switch resource {
	case "":
		name, contentType = "index.html", "text/html; charset=utf-8"
	case "client.js":
		name, contentType = "client.js", "text/javascript; charset=utf-8"
	case "scene.js":
		name, contentType = "scene.js", "text/javascript; charset=utf-8"
	case "style.css":
		name, contentType = "style.css", "text/css; charset=utf-8"
	case "service.js":
		name, contentType = "service.js", "text/javascript; charset=utf-8"
	case "service.css":
		name, contentType = "service.css", "text/css; charset=utf-8"
	default:
		http.NotFound(w, r)
		return
	}
	data, err := assets.ReadFile("assets/" + name)
	if err != nil {
		http.Error(w, "missing embedded asset", 500)
		return
	}
	w.Header().Set("Content-Type", contentType)
	w.Write(data)
}

func canonicalOrigin(raw string) (string, error) {
	u, err := url.Parse(raw)
	if err != nil || u == nil || (u.Scheme != "https" && u.Scheme != "http") || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || (u.Path != "" && u.Path != "/") {
		return "", fmt.Errorf("origin must be an http(s) origin without credentials, path, query or fragment")
	}
	port := u.Port()
	if port != "" {
		n, err := strconv.Atoi(port)
		if err != nil || n < 1 || n > 65535 {
			return "", fmt.Errorf("invalid origin port")
		}
	}
	host := strings.ToLower(u.Hostname())
	if strings.Contains(host, ":") {
		host = "[" + host + "]"
	}
	if port != "" && !(u.Scheme == "https" && port == "443" || u.Scheme == "http" && port == "80") {
		host += ":" + port
	}
	return u.Scheme + "://" + host, nil
}
