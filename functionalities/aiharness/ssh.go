package aiharness

import (
	"context"
	"net/http"
	"time"

	"st-go/sshtunnel"
)

// OpenCodeSSH opens a private loopback SSH forward for each Generate call. Its
// configuration is frozen at construction; no subprocess starts on the UI thread.
type OpenCodeSSH struct {
	target        *sshtunnel.Config
	authorization string
}

// NewOpenCodeSSH uses [user@]host (or an SSH config alias) and a remote loopback
// server URL, defaulting to http://127.0.0.1:4096. SSH ports, keys and jump hosts
// belong in SSH config. Local OpenCode service credentials are never used here.
func NewOpenCodeSSH(server, destination, authorization string) (*OpenCodeSSH, error) {
	target, err := sshtunnel.New(server, destination)
	if err != nil {
		return nil, err
	}
	_, authorization, err = openCodeConnection(target.ServerURL(), authorization, false)
	if err != nil {
		return nil, err
	}
	return &OpenCodeSSH{target: target, authorization: authorization}, nil
}

func (c *OpenCodeSSH) Generate(ctx context.Context, prompt string, model *Model) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Minute)
	defer cancel()
	tunnel, err := c.target.Open(ctx)
	if err != nil {
		return "", err
	}
	defer tunnel.Close()
	client := newOpenCode(c.target.ServerURL(), c.authorization)
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	transport.DialContext = tunnel.DialContext
	defer transport.CloseIdleConnections()
	client.client.Transport = transport
	text, err := client.Generate(ctx, prompt, model)
	if ctx.Err() != nil {
		return "", ctx.Err()
	}
	if err != nil {
		if failure := tunnel.Err(); failure != nil {
			return "", failure
		}
	}
	return text, err
}
