package aiharness

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strings"
)

// This is the local V2 service registration, not the provider credential store.
// Provider OAuth/subscription credentials remain owned by the OpenCode server.
type serviceRegistration struct {
	URL      string `json:"url"`
	Password string `json:"password"`
}

func localService() (serviceRegistration, error) {
	var service serviceRegistration
	state := os.Getenv("XDG_STATE_HOME")
	if state == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return service, err
		}
		state = filepath.Join(home, ".local", "state")
	}
	f, err := os.Open(filepath.Join(state, "opencode", "service.json"))
	if err != nil {
		return service, err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, (64<<10)+1))
	if err != nil {
		return service, err
	}
	if len(data) > 64<<10 || json.Unmarshal(data, &service) != nil || service.URL == "" {
		return serviceRegistration{}, fmt.Errorf("invalid OpenCode service registration")
	}
	return service, nil
}

func serverURL(value string) (*url.URL, error) {
	value = strings.TrimRight(strings.TrimSpace(value), "/")
	if !strings.Contains(value, "://") {
		value = "http://" + value
	}
	u, err := url.Parse(value)
	if err != nil || u.Host == "" || u.Scheme != "http" && u.Scheme != "https" || u.RawQuery != "" || u.Fragment != "" || u.User != nil {
		return nil, fmt.Errorf("invalid AI server URL")
	}
	return u, nil
}

func sameLocalServer(a, b *url.URL) bool {
	local := func(host string) bool {
		return strings.EqualFold(host, "localhost") || host == "127.0.0.1" || host == "::1"
	}
	port := func(u *url.URL) string {
		if p := u.Port(); p != "" {
			return p
		}
		if u.Scheme == "https" {
			return "443"
		}
		return "80"
	}
	return local(a.Hostname()) && local(b.Hostname()) && a.Scheme == b.Scheme && port(a) == port(b) && a.EscapedPath() == b.EscapedPath()
}

func openCodeConnection(server, authorization string, useLocalService bool) (string, string, error) {
	server = strings.TrimSpace(server)
	if authorization == "" {
		authorization = os.Getenv("OPENCODE_TUI_AUTHORIZATION")
	}
	var service serviceRegistration
	// Explicit authorization is authoritative; a server still needs discovering
	// when its URL was omitted. Unrelated explicit servers ignore stale records.
	if useLocalService && (server == "" || authorization == "") {
		var err error
		service, err = localService()
		if server == "" && err != nil {
			return "", "", fmt.Errorf("no usable local OpenCode service; start opencode or set %%server/--ai-server: %w", err)
		}
	}
	if server == "" {
		server = service.URL
	}
	u, err := serverURL(server)
	if err != nil {
		return "", "", err
	}
	if authorization == "" {
		registered, err := serverURL(service.URL)
		if err == nil && service.Password != "" && sameLocalServer(u, registered) {
			authorization = "Basic " + base64.StdEncoding.EncodeToString([]byte("opencode:"+service.Password))
		} else if password := os.Getenv("OPENCODE_PASSWORD"); password != "" {
			authorization = "Basic " + base64.StdEncoding.EncodeToString([]byte("opencode:"+password))
		} else if password, present := os.LookupEnv("OPENCODE_TUI_PASSWORD"); present {
			username := os.Getenv("OPENCODE_TUI_USERNAME")
			if username == "" {
				username = "opencode"
			}
			authorization = "Basic " + base64.StdEncoding.EncodeToString([]byte(username+":"+password))
		}
	}
	return u.String(), authorization, nil
}
