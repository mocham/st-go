package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"syscall"
	"time"
)

const maxAPIResponse = 16 << 20

type modelRef struct {
	ID         string `json:"id"`
	ProviderID string `json:"providerID"`
	Variant    string `json:"variant,omitempty"`
}

type modelInfo struct {
	ID         string `json:"id"`
	ProviderID string `json:"providerID"`
	Name       string `json:"name"`
	Enabled    bool   `json:"enabled"`
	Limit      struct {
		Context int `json:"context"`
	} `json:"limit"`
	Variants []struct {
		ID string `json:"id"`
	} `json:"variants"`
}

type sessionPage struct {
	Data   []sessionInfo `json:"data"`
	Cursor struct {
		Next string `json:"next"`
	} `json:"cursor"`
}

type sessionSnapshot struct {
	session       *sessionInfo
	messages      []apiMessage
	permissions   []permissionRequest
	busy          bool
	messageCursor string
	inbox         []inboxItem
	active        map[string]bool
}

type inboxItem struct {
	ID      string `json:"id"`
	Type    string `json:"type"`
	Payload struct {
		Text string `json:"text"`
	} `json:"payload"`
}

type tokenUsage struct {
	Input, Output, Reasoning float64
	Cache                    struct{ Read, Write float64 }
}

func (usage tokenUsage) total() float64 {
	return usage.Input + usage.Output + usage.Reasoning + usage.Cache.Read + usage.Cache.Write
}

type messagePage struct {
	Data   []apiMessage `json:"data"`
	Cursor struct {
		Next string `json:"next"`
	} `json:"cursor"`
}

type locationRef struct {
	Directory string `json:"directory"`
}

type sessionInfo struct {
	ID       string      `json:"id"`
	Title    string      `json:"title"`
	Agent    string      `json:"agent"`
	Model    *modelRef   `json:"model"`
	Location locationRef `json:"location"`
	ParentID string      `json:"parentID"`
	Revert   *struct {
		MessageID string `json:"messageID"`
	} `json:"revert"`
}

type messageContent struct {
	Type  string    `json:"type"`
	Text  string    `json:"text"`
	ID    string    `json:"id"`
	Name  string    `json:"name"`
	State toolState `json:"state"`
}

type toolState struct {
	Status   string          `json:"status"`
	Input    json.RawMessage `json:"input"`
	Content  []toolContent   `json:"content"`
	Metadata map[string]any  `json:"metadata"`
	Error    *struct {
		Message string `json:"message"`
	} `json:"error"`
}

type toolContent struct {
	Type string `json:"type"`
	Text string `json:"text"`
	URI  string `json:"uri"`
	Name string `json:"name"`
	MIME string `json:"mime"`
}

type messageAttachment struct {
	Data   string `json:"data"`
	MIME   string `json:"mime"`
	Name   string `json:"name"`
	Source struct {
		Type string `json:"type"`
		URI  string `json:"uri"`
	} `json:"source"`
}

type apiMessage struct {
	Outcome  string              `json:"outcome"`
	ID       string              `json:"id"`
	Type     string              `json:"type"`
	Text     string              `json:"text"`
	Agent    string              `json:"agent"`
	Content  []messageContent    `json:"content"`
	Files    []messageAttachment `json:"files"`
	Snapshot struct {
		Files []string `json:"files"`
	} `json:"snapshot"`
	Model  *modelRef   `json:"model"`
	Tokens *tokenUsage `json:"tokens"`
	Status string      `json:"status"`
	Time   struct {
		Created float64 `json:"created"`
	} `json:"time"`
}

type permissionRequest struct {
	ID        string   `json:"id"`
	SessionID string   `json:"sessionID"`
	Action    string   `json:"action"`
	Resources []string `json:"resources"`
	Message   string   `json:"message"`
}

type apiEvent struct {
	Type string          `json:"type"`
	Data json.RawMessage `json:"data"`
}

type eventData struct {
	SessionID          string          `json:"sessionID"`
	AssistantMessageID string          `json:"assistantMessageID"`
	Ordinal            int             `json:"ordinal"`
	Delta              string          `json:"delta"`
	Text               string          `json:"text"`
	Error              json.RawMessage `json:"error"`
}

type apiClient struct {
	base                 *url.URL
	authorization        string
	requests             *http.Client
	streams              *http.Client
	streamConnectTimeout time.Duration
	streamIdleTimeout    time.Duration
}

func newAPIClient(baseURL, authorization string) (*apiClient, error) {
	baseURL = strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if baseURL != "" && !strings.Contains(baseURL, "://") {
		baseURL = "http://" + baseURL
	}
	parsed, err := url.Parse(baseURL)
	if err != nil || parsed.Scheme != "http" && parsed.Scheme != "https" || parsed.Host == "" {
		return nil, fmt.Errorf("invalid OpenCode server URL %q", baseURL)
	}
	if parsed.RawQuery != "" || parsed.Fragment != "" {
		return nil, fmt.Errorf("OpenCode server URL must be a base address without a query or fragment")
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.DialContext = (&net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}).DialContext
	// Like the official frontend, bound waiting for response headers, not the
	// lifetime of a request body or a running session.
	transport.ResponseHeaderTimeout = 60 * time.Second
	return &apiClient{
		base:                 parsed,
		authorization:        authorization,
		requests:             &http.Client{Transport: transport},
		streams:              &http.Client{Transport: transport},
		streamConnectTimeout: 10 * time.Second, streamIdleTimeout: 45 * time.Second,
	}, nil
}

type apiError struct {
	status  int
	message string
}

func (err *apiError) Error() string { return err.message }

func rejectedPrompt(err error) bool {
	var failure *apiError
	return errors.As(err, &failure) && failure.status >= 400 && failure.status < 500 && failure.status != 408
}

func (client *apiClient) endpoint(path string) string {
	return strings.TrimRight(client.base.String(), "/") + path
}

func connectionError(err error) error {
	if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) || errors.Is(err, syscall.ECONNRESET) {
		return fmt.Errorf("%w (connection closed before an HTTP response; check http:// versus https:// and the server listener/port mapping; a Docker-published server must listen on a container-reachable interface, usually 0.0.0.0)", err)
	}
	return err
}

func authenticationHint(response *http.Response) string {
	if response.StatusCode != http.StatusUnauthorized {
		return ""
	}
	if strings.HasPrefix(strings.ToLower(response.Header.Get("WWW-Authenticate")), "basic") {
		return "; set OPENCODE_TUI_PASSWORD to the server password (OPENCODE_TUI_USERNAME defaults to opencode), or supply OPENCODE_TUI_AUTHORIZATION"
	}
	return "; set OPENCODE_TUI_AUTHORIZATION to the server's complete Authorization header"
}

func (client *apiClient) request(ctx context.Context, method, path string, input, output any) error {
	var body io.Reader
	if input != nil {
		encoded, err := json.Marshal(input)
		if err != nil {
			return err
		}
		body = bytes.NewReader(encoded)
	}
	request, err := http.NewRequestWithContext(ctx, method, client.endpoint(path), body)
	if err != nil {
		return err
	}
	request.Header.Set("Accept", "application/json")
	if input != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	if client.authorization != "" {
		request.Header.Set("Authorization", client.authorization)
	}
	response, err := client.requests.Do(request)
	if err != nil {
		return connectionError(err)
	}
	defer response.Body.Close()
	data, err := io.ReadAll(io.LimitReader(response.Body, maxAPIResponse+1))
	if err != nil {
		return fmt.Errorf("read OpenCode %s %s response (%s): %w", method, path, response.Status, err)
	}
	if len(data) > maxAPIResponse {
		return fmt.Errorf("OpenCode response exceeds %d bytes", maxAPIResponse)
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		var problem struct {
			Message string `json:"message"`
		}
		_ = json.Unmarshal(data, &problem)
		if problem.Message == "" {
			problem.Message = strings.TrimSpace(string(data))
		}
		if problem.Message == "" {
			problem.Message = response.Status
		}
		return &apiError{response.StatusCode, fmt.Sprintf("OpenCode API %s: %s%s", response.Status, problem.Message, authenticationHint(response))}
	}
	if output == nil {
		return nil
	}
	if len(bytes.TrimSpace(data)) == 0 {
		return fmt.Errorf("OpenCode %s %s returned an empty JSON response (%s)", method, path, response.Status)
	}
	if err := json.Unmarshal(data, output); err != nil {
		return fmt.Errorf("decode OpenCode response: %w", err)
	}
	return nil
}

func (client *apiClient) info(ctx context.Context) error {
	var response struct {
		Version string `json:"version"`
	}
	if err := client.request(ctx, http.MethodGet, "/api/info", nil, &response); err != nil {
		return err
	}
	if response.Version == "" {
		return fmt.Errorf("/api/info did not return OpenCode server information; check the server URL and that it serves the V2 API")
	}
	return nil
}

func (client *apiClient) createSession(ctx context.Context, directory string) (sessionInfo, error) {
	var response struct {
		Data sessionInfo `json:"data"`
	}
	err := client.request(ctx, http.MethodPost, "/api/session", map[string]any{
		"location": locationRef{Directory: directory},
	}, &response)
	return response.Data, err
}

func (client *apiClient) getSession(ctx context.Context, id string) (sessionInfo, error) {
	var response struct {
		Data sessionInfo `json:"data"`
	}
	err := client.request(ctx, http.MethodGet, "/api/session/"+url.PathEscape(id), nil, &response)
	return response.Data, err
}

func (client *apiClient) deleteSession(ctx context.Context, id string) error {
	return client.request(ctx, http.MethodDelete, "/api/session/"+url.PathEscape(id), nil, nil)
}

func (client *apiClient) inbox(ctx context.Context, id string) ([]inboxItem, error) {
	var response struct {
		Data []inboxItem `json:"data"`
	}
	err := client.request(ctx, http.MethodGet, "/api/session/"+url.PathEscape(id)+"/inbox", nil, &response)
	return response.Data, err
}

func (client *apiClient) sessions(ctx context.Context, search, cursor string) (sessionPage, error) {
	query := url.Values{"limit": {"50"}, "archived": {"false"}}
	if search != "" {
		query.Set("search", search)
	}
	if cursor != "" {
		query.Set("cursor", cursor)
	} else {
		query.Set("order", "desc")
	}
	return client.listSessions(ctx, query)
}

func (client *apiClient) sessionCatalog(ctx context.Context, cursor string) (sessionPage, error) {
	query := url.Values{"limit": {"50"}}
	if cursor == "" {
		query.Set("order", "desc")
	} else {
		query.Set("cursor", cursor)
	}
	return client.listSessions(ctx, query)
}

func (client *apiClient) listSessions(ctx context.Context, query url.Values) (sessionPage, error) {
	var response sessionPage
	err := client.request(ctx, http.MethodGet, "/api/session?"+query.Encode(), nil, &response)
	return response, err
}

func modelQuery(directory string) string {
	return url.Values{"location[directory]": {directory}}.Encode()
}

func (client *apiClient) models(ctx context.Context, directory string) ([]modelInfo, error) {
	var response struct {
		Data []modelInfo `json:"data"`
	}
	err := client.request(ctx, http.MethodGet, "/api/model?"+modelQuery(directory), nil, &response)
	return response.Data, err
}

func (client *apiClient) defaultModel(ctx context.Context, directory string) (*modelInfo, error) {
	var response struct {
		Data *modelInfo `json:"data"`
	}
	err := client.request(ctx, http.MethodGet, "/api/model/default?"+modelQuery(directory), nil, &response)
	return response.Data, err
}

func (client *apiClient) switchModel(ctx context.Context, sessionID string, model modelRef) error {
	return client.request(ctx, http.MethodPost, "/api/session/"+url.PathEscape(sessionID)+"/model", map[string]any{"model": model}, nil)
}

// Snapshot reads are used on attachment and SSE reconnect. The caller owns
// rejecting a snapshot when newer session events arrived during the reads.
func (client *apiClient) snapshot(ctx context.Context, id string, promptCount int) (sessionSnapshot, error) {
	var result sessionSnapshot
	session, err := client.getSession(ctx, id)
	if err != nil {
		return result, err
	}
	if session.ID != id || id == "" {
		return result, fmt.Errorf("server returned an invalid session for %q", id)
	}
	result.session = &session
	result.messages, result.messageCursor, err = client.promptHistory(ctx, id, promptCount)
	if err != nil {
		return result, err
	}
	result.permissions, err = client.permissions(ctx, id)
	if err != nil {
		return result, err
	}
	var active struct {
		Data map[string]json.RawMessage `json:"data"`
	}
	err = client.request(ctx, http.MethodGet, "/api/session/active", nil, &active)
	_, result.busy = active.Data[id]
	if err == nil {
		result.active = make(map[string]bool, len(active.Data))
		for id := range active.Data {
			result.active[id] = true
		}
	}
	return result, err
}

func (client *apiClient) messages(ctx context.Context, sessionID string) ([]apiMessage, error) {
	page, err := client.messagePage(ctx, sessionID, "")
	return page.Data, err
}

func (client *apiClient) messagePage(ctx context.Context, sessionID, cursor string) (messagePage, error) {
	var response messagePage
	query := url.Values{"limit": {"200"}}
	if cursor == "" {
		query.Set("order", "desc")
	} else {
		query.Set("cursor", cursor)
	}
	path := "/api/session/" + url.PathEscape(sessionID) + "/message?" + query.Encode()
	err := client.request(ctx, http.MethodGet, path, nil, &response)
	for left, right := 0, len(response.Data)-1; left < right; left, right = left+1, right-1 {
		response.Data[left], response.Data[right] = response.Data[right], response.Data[left]
	}
	return response, err
}

// Page by messages, but stop only at a prompt boundary so even long tool-heavy
// turns have their prompt and complete response available in the sidebar.
func (client *apiClient) promptHistory(ctx context.Context, sessionID string, wanted int) ([]apiMessage, string, error) {
	var messages []apiMessage
	cursor, count := "", 0
	seen := make(map[string]bool)
	for {
		page, err := client.messagePage(ctx, sessionID, cursor)
		if err != nil {
			return nil, "", err
		}
		for _, message := range page.Data {
			if message.Type == "user" {
				count++
			}
		}
		messages = append(page.Data, messages...)
		cursor = page.Cursor.Next
		if cursor == "" || count >= wanted {
			return messages, cursor, nil
		}
		if seen[cursor] {
			return nil, "", fmt.Errorf("OpenCode repeated a message cursor")
		}
		seen[cursor] = true
	}
}

func (client *apiClient) permissions(ctx context.Context, sessionID string) ([]permissionRequest, error) {
	var response struct {
		Data []permissionRequest `json:"data"`
	}
	path := "/api/session/" + url.PathEscape(sessionID) + "/permission"
	err := client.request(ctx, http.MethodGet, path, nil, &response)
	return response.Data, err
}

func (client *apiClient) prompt(ctx context.Context, sessionID, text, messageID string) error {
	path := "/api/session/" + url.PathEscape(sessionID) + "/prompt"
	return client.request(ctx, http.MethodPost, path, map[string]string{"text": text, "id": messageID}, nil)
}

func (client *apiClient) interrupt(ctx context.Context, sessionID string) error {
	path := "/api/session/" + url.PathEscape(sessionID) + "/interrupt"
	return client.request(ctx, http.MethodPost, path, nil, nil)
}

func (client *apiClient) replyPermission(ctx context.Context, sessionID, requestID, decision string) error {
	path := "/api/session/" + url.PathEscape(sessionID) + "/permission/" + url.PathEscape(requestID) + "/reply"
	return client.request(ctx, http.MethodPost, path, map[string]string{"decision": decision}, nil)
}

var streamTypes = []string{
	"session.created", "session.deleted", "session.moved", "session.archived", "session.unarchived",
	"session.inbox.enqueued", "session.inbox.delivered", "session.inbox.cancelled",
	"session.text.started", "session.text.delta", "session.text.ended",
	"session.reasoning.started", "session.reasoning.delta", "session.reasoning.ended",
	"session.execution.started", "session.execution.succeeded", "session.execution.failed", "session.execution.interrupted",
	"permission.asked", "permission.replied", "session.renamed", "session.model.selected",
	"session.step.started", "session.step.ended", "session.step.failed", "session.compaction.ended", "session.revert.staged", "session.revert.cleared", "session.revert.committed", "session.status",
	"session.tool.input.started", "session.tool.input.delta", "session.tool.input.ended",
	"session.tool.called", "session.tool.progress", "session.tool.success", "session.tool.failed",
}

// subscribe consumes one live-only OpenCode SSE stream. Reconnection and state
// resynchronization are deliberately owned by the caller.
func (client *apiClient) subscribe(ctx context.Context, emit func(apiEvent) error) error {
	ctx, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)
	watch := newStreamWatch(client.streamConnectTimeout, client.streamIdleTimeout, cancel)
	defer watch.stop()
	query := url.Values{}
	for _, eventType := range streamTypes {
		query.Add("types", eventType)
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, client.endpoint("/api/event?"+query.Encode()), nil)
	if err != nil {
		return err
	}
	request.Header.Set("Accept", "text/event-stream")
	if client.authorization != "" {
		request.Header.Set("Authorization", client.authorization)
	}
	response, err := client.streams.Do(request)
	if err != nil {
		if cause := context.Cause(ctx); cause != nil {
			return cause
		}
		return connectionError(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		data, _ := io.ReadAll(io.LimitReader(response.Body, 4096))
		return fmt.Errorf("OpenCode event stream %s: %s%s", response.Status, strings.TrimSpace(string(data)), authenticationHint(response))
	}
	if !strings.HasPrefix(strings.ToLower(response.Header.Get("Content-Type")), "text/event-stream") {
		return fmt.Errorf("OpenCode event stream returned %q instead of text/event-stream", response.Header.Get("Content-Type"))
	}
	connected := false
	err = decodeSSE(activityReader{response.Body, watch.touch}, func(event apiEvent) error {
		if !connected {
			if event.Type != "server.connected" {
				return fmt.Errorf("OpenCode event stream did not start with server.connected")
			}
			connected = true
			watch.connected()
		}
		return emit(event)
	})
	if cause := context.Cause(ctx); cause != nil {
		return cause
	}
	return err
}

func decodeSSE(reader io.Reader, emit func(apiEvent) error) error {
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 64<<10), maxAPIResponse)
	var data strings.Builder
	name := ""
	flush := func() error {
		defer func() { name = "" }()
		if data.Len() == 0 {
			return nil
		}
		payload := strings.TrimSuffix(data.String(), "\n")
		data.Reset()
		if name == "effect/http-api/stream/failure" {
			return fmt.Errorf("OpenCode event stream failure: %s", payload)
		}
		var event apiEvent
		if err := json.Unmarshal([]byte(payload), &event); err != nil {
			return fmt.Errorf("decode OpenCode event: %w", err)
		}
		return emit(event)
	}
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			if err := flush(); err != nil {
				return err
			}
			continue
		}
		if strings.HasPrefix(line, ":") {
			continue
		}
		if value, ok := strings.CutPrefix(line, "event:"); ok {
			name = strings.TrimSpace(value)
		}
		if value, ok := strings.CutPrefix(line, "data:"); ok {
			if data.Len()+len(value) > maxAPIResponse {
				return fmt.Errorf("OpenCode event exceeds %d bytes", maxAPIResponse)
			}
			data.WriteString(strings.TrimPrefix(value, " "))
			data.WriteByte('\n')
		}
	}
	if err := scanner.Err(); err != nil {
		if errors.Is(err, context.Canceled) {
			return err
		}
		return fmt.Errorf("OpenCode event stream: %w", err)
	}
	if err := flush(); err != nil {
		return err
	}
	return io.EOF
}
