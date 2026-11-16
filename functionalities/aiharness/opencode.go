package aiharness

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"
)

type Model struct {
	ProviderID string `json:"providerID"`
	ID         string `json:"id"`
	Variant    string `json:"variant,omitempty"`
}

func ParseModel(value string) (*Model, error) {
	if value == "" {
		return nil, nil
	}
	provider, rest, ok := strings.Cut(value, "/")
	id, variant, hasVariant := strings.Cut(rest, "#")
	if !ok || provider == "" || id == "" || hasVariant && variant == "" {
		return nil, fmt.Errorf("AI model must be provider/model[#variant]")
	}
	return &Model{provider, id, variant}, nil
}

// Generator must honor context cancellation. Prompt and model are read-only;
// implementations return the requested UUID-to-insertion JSON object without
// adding ambient document context.
type Generator interface {
	Generate(context.Context, string, *Model) (string, error)
}

// OpenCode uses only the V2 stateless endpoint, with no session or tool loop.
type OpenCode struct {
	baseURL       string
	endpoint      string
	authorization string
	client        *http.Client
	mu            sync.Mutex
}

// NewOpenCode uses an explicit server, or discovers the existing local V2
// service when server is empty. A matching local service authenticates using
// its private registration. authorization overrides this server authentication.
func NewOpenCode(server, authorization string) (*OpenCode, error) {
	server, authorization, err := openCodeConnection(server, authorization, true)
	if err != nil {
		return nil, err
	}
	return newOpenCode(server, authorization), nil
}

func newOpenCode(server, authorization string) *OpenCode {
	return &OpenCode{baseURL: server, authorization: authorization, client: &http.Client{
		Timeout:       20 * time.Minute,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}}
}

const generateTextPath = "/api/experimental/generate/text"
const legacyV2GeneratePath = "/api/experimental/generate"

// Select the advertised stateless route before sending any generation request.
// V2.0.32 uses the earlier path. Never retry a generation to probe compatibility.
func (c *OpenCode) generationEndpoint(ctx context.Context) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.endpoint != "" {
		return c.endpoint, nil
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	response, err := c.request(ctx, http.MethodGet, c.baseURL+"/openapi.json", nil)
	if err != nil {
		return "", err
	}
	defer response.Body.Close()
	if response.StatusCode == http.StatusNotFound {
		// A proxy may expose only the current generation route.
		c.endpoint = c.baseURL + generateTextPath
		return c.endpoint, nil
	}
	data, err := readOpenCodeResponse(response, 8<<20)
	if err != nil {
		return "", fmt.Errorf("OpenCode API discovery: %w", err)
	}
	var spec struct {
		Paths map[string]map[string]json.RawMessage `json:"paths"`
	}
	if err := json.Unmarshal(data, &spec); err != nil {
		return "", fmt.Errorf("invalid OpenCode API description: %w", err)
	}
	for _, path := range []string{generateTextPath, legacyV2GeneratePath} {
		if _, ok := spec.Paths[path]["post"]; ok {
			c.endpoint = c.baseURL + path
			return c.endpoint, nil
		}
	}
	return "", fmt.Errorf("OpenCode server does not advertise stateless text generation")
}

func (c *OpenCode) request(ctx context.Context, method, endpoint string, body []byte) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, method, endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if c.authorization != "" {
		req.Header.Set("Authorization", c.authorization)
	}
	return c.client.Do(req)
}

func readOpenCodeResponse(response *http.Response, limit int64) ([]byte, error) {
	data, err := io.ReadAll(io.LimitReader(response.Body, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		return nil, fmt.Errorf("OpenCode response exceeds size limit")
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		if response.StatusCode == http.StatusUnauthorized {
			return nil, fmt.Errorf("OpenCode HTTP 401: server login missing/rejected; export OPENCODE_PASSWORD, OPENCODE_TUI_PASSWORD or GOVIM_AI_AUTHORIZATION in govim's launch environment")
		}
		return nil, fmt.Errorf("OpenCode HTTP %d: %.1024s", response.StatusCode, data)
	}
	return data, nil
}

func (c *OpenCode) Generate(ctx context.Context, prompt string, model *Model) (string, error) {
	endpoint, err := c.generationEndpoint(ctx)
	if err != nil {
		return "", err
	}
	body, err := json.Marshal(struct {
		Prompt string `json:"prompt"`
		Model  *Model `json:"model,omitempty"`
	}{prompt, model})
	if err != nil {
		return "", err
	}
	response, err := c.request(ctx, http.MethodPost, endpoint, body)
	if err != nil {
		return "", err
	}
	defer response.Body.Close()
	// The server envelope JSON-encodes the model's JSON text again. Allow its
	// worst-case escaping overhead while keeping the decoded batch bounded.
	data, err := readOpenCodeResponse(response, 6*MaxBatchResponse+1024)
	if err != nil {
		return "", err
	}
	var result struct {
		Data struct {
			Text string `json:"text"`
		} `json:"data"`
	}
	if err := json.Unmarshal(data, &result); err != nil {
		return "", fmt.Errorf("OpenCode generation: %w", err)
	}
	return result.Data.Text, nil
}
