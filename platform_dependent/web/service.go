package web

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os/signal"
	"path"
	"sort"
	"strings"
	"sync"
	"syscall"

	"st-go/platform_neutral/api"
)

// Service owns a persistent listener. Each PTY has a separate Driver, callback
// domain, scene/resource store and exclusive attachment; no native window or
// selection is shared between independently connected browsers.
type Service struct {
	*Driver
	options    Options
	prefix     string
	mu         sync.Mutex
	opMu       sync.Mutex
	closeOnce  sync.Once
	entries    map[string]*servicePTY
	next       api.WidgetID
	factory    api.SessionFactory
	defaults   api.TabSpawnRequest
	operations chan func()
}

type servicePTY struct {
	driver  *Driver
	id      string
	number  api.WidgetID
	close   func()
	title   string
	results map[string]spawnResult // serialized on the service operation worker
	order   []string
}

type spawnResult struct {
	id  api.WidgetID
	err error
}

type ptyInfo struct {
	ID       string `json:"id"`
	Title    string `json:"title"`
	State    string `json:"state"`
	Occupied bool   `json:"occupied"`
	View     string `json:"view"`
}

func NewService(window api.WindowOptions, options Options) (*Service, error) {
	if options.BasePath == "" {
		options.BasePath = "/"
	}
	prefix := options.BasePath
	if !strings.HasPrefix(prefix, "/") {
		return nil, fmt.Errorf("base path must start with /")
	}
	for _, c := range prefix {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || strings.ContainsRune("/_-.", c)) {
			return nil, fmt.Errorf("base path must contain plain URL path characters")
		}
	}
	prefix = strings.TrimSuffix(prefix, "/")
	if prefix == "/" || prefix != "" && path.Clean(prefix) != prefix {
		return nil, fmt.Errorf("base path must be canonical")
	}
	prefix += "/"
	if options.MaxPTYs == 0 {
		options.MaxPTYs = 8
	}
	if options.MaxPTYs < 1 || options.MaxPTYs > 64 {
		return nil, fmt.Errorf("max PTYs must be between 1 and 64")
	}
	driver, err := New(window, options)
	if err != nil {
		return nil, err
	}
	options.Port = driver.port
	s := &Service{Driver: driver, options: options, prefix: prefix, entries: make(map[string]*servicePTY), operations: make(chan func(), 64)}
	driver.server.Handler = s
	return s, nil
}

func (s *Service) URL() string {
	u := s.Driver.URL()
	return u[:strings.Index(u, "/t/")] + s.prefix
}

func (s *Service) ServeSessions(factory api.SessionFactory, initial, defaults api.TabSpawnRequest) (err error) {
	defer func() { reportDaemon("", err) }()
	defer s.Close()
	if factory == nil {
		return fmt.Errorf("session factory required")
	}
	if err := defaults.Validate(); err != nil {
		return err
	}
	s.factory, s.defaults = factory, defaults
	if _, err := s.create(initial); err != nil {
		log.Printf("initial PTY: %v (service remains available)", err)
	}
	go s.operate()
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	served := make(chan error, 1)
	go func() { served <- s.server.Serve(s.listener) }()
	reportDaemon(s.URL(), nil)
	fmt.Printf("Web terminal service: %s\n", s.URL())
	select {
	case <-s.done:
	case <-ctx.Done():
	case err := <-served:
		if !errors.Is(err, http.ErrServerClosed) {
			return err
		}
	}
	return nil
}

func (s *Service) Close() {
	s.closeOnce.Do(func() {
		s.Driver.Close()
		s.opMu.Lock()
		defer s.opMu.Unlock()
		s.mu.Lock()
		entries := make([]*servicePTY, 0, len(s.entries))
		for _, entry := range s.entries {
			entries = append(entries, entry)
		}
		clear(s.entries)
		s.mu.Unlock()
		for _, entry := range entries {
			entry.close()
		}
	})
}

func (s *Service) create(request api.TabSpawnRequest) (*servicePTY, error) {
	s.opMu.Lock()
	defer s.opMu.Unlock()
	select {
	case <-s.done:
		return nil, fmt.Errorf("service closed")
	default:
	}
	if s.factory == nil {
		return nil, fmt.Errorf("service not ready")
	}
	if len(request.Command) == 0 {
		request.Command = append([]string(nil), s.defaults.Command...)
	}
	if request.Directory == "" {
		request.Directory = s.defaults.Directory
	}
	if err := request.Validate(); err != nil {
		return nil, err
	}
	if request.Probe {
		return nil, fmt.Errorf("not a PTY creation request")
	}
	s.mu.Lock()
	// Closed drivers do not consume admission slots while their removal goroutine
	// is waiting to run (notably an immediate create after deleting the last PTY).
	for id, entry := range s.entries {
		select {
		case <-entry.driver.done:
			delete(s.entries, id)
		default:
		}
	}
	full := len(s.entries) >= s.options.MaxPTYs
	s.mu.Unlock()
	if full {
		return nil, fmt.Errorf("PTY limit reached")
	}
	driver, err := newDriver(s.window, s.options, false)
	if err != nil {
		return nil, err
	}
	driver.managed = true
	s.next++
	entry := &servicePTY{driver: driver, id: driver.token[:32], number: s.next, results: make(map[string]spawnResult)}
	closePTY, err := s.factory(driver, request, s.spawner(entry))
	if err != nil {
		driver.Close()
		return nil, err
	}
	entry.close = closePTY
	s.mu.Lock()
	s.entries[entry.id] = entry
	s.mu.Unlock()
	go func() {
		<-driver.done
		s.mu.Lock()
		delete(s.entries, entry.id)
		s.mu.Unlock()
	}()
	return entry, nil
}

func (s *Service) operate() {
	for {
		select {
		case <-s.done:
			return
		case operation := <-s.operations:
			operation()
		}
	}
}

func (s *Service) spawner(origin *servicePTY) api.SessionSpawn {
	return func(id string, request api.TabSpawnRequest, reply func(api.WidgetID, error)) bool {
		request.Command = append([]string(nil), request.Command...)
		origin.driver.mu.Lock()
		controller := origin.driver.client
		origin.driver.mu.Unlock()
		select {
		case <-s.done:
			return false
		default:
		}
		select {
		case s.operations <- func() {
			select {
			case <-origin.driver.done:
				return
			default:
			}
			if result, ok := origin.results[id]; ok {
				reply(result.id, result.err)
				return
			}
			result := spawnResult{err: request.Validate()}
			if result.err == nil && !request.Probe {
				var entry *servicePTY
				entry, result.err = s.create(request)
				if entry != nil {
					result.id = entry.number
					if !request.Detached && controller != nil {
						info := s.info(entry)
						origin.driver.mu.Lock()
						if origin.driver.client == controller {
							select {
							case controller.controls <- serverMessage{Type: "open_pty", OpenPTY: &info}:
							default:
							}
						}
						origin.driver.mu.Unlock()
					}
				}
			}
			if len(origin.order) == 256 {
				delete(origin.results, origin.order[0])
				origin.order = origin.order[1:]
			}
			origin.order = append(origin.order, id)
			origin.results[id] = result
			reply(result.id, result.err)
		}:
			return true
		default:
			return false
		}
	}
}

func (s *Service) info(entry *servicePTY) ptyInfo {
	entry.driver.mu.Lock()
	title, occupied := entry.driver.window.Title, entry.driver.active
	entry.driver.mu.Unlock()
	s.mu.Lock()
	if entry.title != "" {
		title = entry.title
	}
	s.mu.Unlock()
	return ptyInfo{ID: entry.id, Title: title, State: "running", Occupied: occupied, View: s.prefix + "api/ptys/" + entry.id + "/view/"}
}

func (s *Service) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	securityHeaders(w)
	// Health has no session data or authority and also serves local container
	// checks whose Host differs from the configured proxy origin.
	if r.URL.Path == s.prefix+"healthz" && (r.Method == "GET" || r.Method == "HEAD") {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
		return
	}
	if !s.validHost(r.Host) {
		http.Error(w, "invalid host", http.StatusForbidden)
		return
	}
	if r.URL.RawPath != "" || path.Clean(r.URL.Path) != strings.TrimSuffix(r.URL.Path, "/") && r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	if s.prefix != "/" && r.URL.Path == strings.TrimSuffix(s.prefix, "/") {
		http.Redirect(w, r, s.prefix, http.StatusPermanentRedirect)
		return
	}
	if !strings.HasPrefix(r.URL.Path, s.prefix) {
		http.NotFound(w, r)
		return
	}
	resource := strings.TrimPrefix(r.URL.Path, s.prefix)
	if r.Method != "GET" && r.Method != "HEAD" {
		// Basic-auth browsers send credentials automatically. Mutation requests
		// need a matching Origin, or an explicit non-browser API header when no
		// Origin is present. Cross-origin preflights are never granted.
		if !s.sameOrigin(r) && !(r.Header.Get("Origin") == "" && r.Header.Get("X-ST-Service-Request") == "1") {
			http.Error(w, "same origin required", http.StatusForbidden)
			return
		}
	}
	if resource == "api/ptys" {
		s.serveCollection(w, r)
		return
	}
	if strings.HasPrefix(resource, "api/ptys/") {
		parts := strings.SplitN(strings.TrimPrefix(resource, "api/ptys/"), "/", 2)
		s.mu.Lock()
		entry := s.entries[parts[0]]
		s.mu.Unlock()
		if entry == nil {
			http.NotFound(w, r)
			return
		}
		if len(parts) == 1 {
			s.servePTY(w, r, entry)
			return
		}
		if parts[1] == "attach" {
			entry.driver.serveResource(w, r, "ws")
			return
		}
		if strings.HasPrefix(parts[1], "view/") {
			resource := strings.TrimPrefix(parts[1], "view/")
			if resource == "" {
				s.servePage(w, r, entry.id)
			} else {
				entry.driver.serveResource(w, r, resource)
			}
			return
		}
		http.NotFound(w, r)
		return
	}
	if r.Method != "GET" && r.Method != "HEAD" {
		methodNotAllowed(w, "GET, HEAD")
		return
	}
	if resource == "" {
		s.servePage(w, r, "")
		return
	}
	if resource == "ws" || strings.HasPrefix(resource, "media/") {
		http.NotFound(w, r)
		return
	}
	s.Driver.serveResource(w, r, resource)
}

func (s *Service) servePage(w http.ResponseWriter, r *http.Request, id string) {
	if r.Method != "GET" && r.Method != "HEAD" {
		methodNotAllowed(w, "GET, HEAD")
		return
	}
	data, err := assets.ReadFile("assets/service.html")
	if err != nil {
		http.Error(w, "missing embedded asset", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	// Both values are server-generated and limited to plain URL path characters.
	w.Write([]byte(strings.NewReplacer("{{prefix}}", s.prefix, "{{pty}}", id).Replace(string(data))))
}

func (s *Service) serveCollection(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case "GET":
		s.mu.Lock()
		entries := make([]*servicePTY, 0, len(s.entries))
		for _, entry := range s.entries {
			entries = append(entries, entry)
		}
		s.mu.Unlock()
		sort.Slice(entries, func(i, j int) bool { return entries[i].number < entries[j].number })
		result := make([]ptyInfo, 0, len(entries))
		for _, entry := range entries {
			select {
			case <-entry.driver.done:
				continue
			default:
			}
			result = append(result, s.info(entry))
		}
		writeJSON(w, http.StatusOK, result)
	case "POST":
		var request struct {
			Command   []string `json:"command"`
			Directory string   `json:"directory"`
			Title     string   `json:"title"`
		}
		if !decodeRequest(w, r, &request) {
			return
		}
		entry, err := s.create(api.TabSpawnRequest{Command: request.Command, Directory: request.Directory, Title: request.Title})
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		w.Header().Set("Location", s.prefix+"api/ptys/"+entry.id)
		writeJSON(w, http.StatusCreated, s.info(entry))
	default:
		methodNotAllowed(w, "GET, POST")
	}
}

func (s *Service) servePTY(w http.ResponseWriter, r *http.Request, entry *servicePTY) {
	var title string
	if r.Method == "PATCH" {
		var request struct {
			Title string `json:"title"`
		}
		if !decodeRequest(w, r, &request) {
			return
		}
		valid := api.TabSpawnRequest{Command: []string{"rename"}, Directory: "/", Title: request.Title}
		if request.Title == "" || valid.Validate() != nil {
			http.Error(w, "invalid title", http.StatusBadRequest)
			return
		}
		title = request.Title
	}
	if r.Method == "DELETE" || r.Method == "PATCH" {
		d := entry.driver
		d.mu.Lock()
		// Check authority after reading the body: a slow request must neither
		// reserve an attachment nor outlive the lease that authorized it.
		owned := d.client != nil && !d.client.expired() && subtle.ConstantTimeCompare([]byte(d.client.token), []byte(r.Header.Get("X-ST-Attachment"))) == 1
		if d.mutating || d.active && !owned || r.Header.Get("X-ST-Attachment") != "" && !owned {
			d.mu.Unlock()
			http.Error(w, "terminal occupied", http.StatusConflict)
			return
		}
		d.mutating = true
		d.mu.Unlock()
		defer func() { d.mu.Lock(); d.mutating = false; d.mu.Unlock() }()
	}
	switch r.Method {
	case "GET":
		writeJSON(w, http.StatusOK, s.info(entry))
	case "DELETE":
		entry.close()
		w.WriteHeader(http.StatusNoContent)
	case "PATCH":
		s.mu.Lock()
		entry.title = title
		s.mu.Unlock()
		writeJSON(w, http.StatusOK, s.info(entry))
	default:
		methodNotAllowed(w, "GET, PATCH, DELETE")
	}
}

func decodeRequest(w http.ResponseWriter, r *http.Request, target any) bool {
	if strings.Split(r.Header.Get("Content-Type"), ";")[0] != "application/json" {
		http.Error(w, "JSON required", http.StatusUnsupportedMediaType)
		return false
	}
	defer r.Body.Close()
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, api.TabRequestLimit))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		http.Error(w, "invalid JSON request", http.StatusBadRequest)
		return false
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		http.Error(w, "one JSON value required", http.StatusBadRequest)
		return false
	}
	return true
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(value)
}

func methodNotAllowed(w http.ResponseWriter, allow string) {
	w.Header().Set("Allow", allow)
	http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
}

var _ api.SessionService = (*Service)(nil)
