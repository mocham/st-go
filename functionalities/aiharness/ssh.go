package aiharness

import (
	"bytes"
	"context"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// OpenCodeSSH opens a private loopback SSH forward for each Generate call. Its
// configuration is frozen at construction; no subprocess starts on the UI thread.
type OpenCodeSSH struct {
	server        *url.URL
	destination   string
	authorization string
}

var sshDestination = regexp.MustCompile(`^([a-zA-Z0-9_][a-zA-Z0-9_.-]*@)?([a-zA-Z0-9_][a-zA-Z0-9_.-]*|\[[0-9a-fA-F:]+\])$`)

// NewOpenCodeSSH uses [user@]host (or an SSH config alias) and a remote loopback
// server URL, defaulting to http://127.0.0.1:4096. SSH ports, keys and jump hosts
// belong in SSH config. Local OpenCode service credentials are never used here.
func NewOpenCodeSSH(server, destination, authorization string) (*OpenCodeSSH, error) {
	destination = strings.TrimSpace(destination)
	if !sshDestination.MatchString(destination) {
		return nil, fmt.Errorf("AI server SSH destination must be [user@]host or an SSH config alias")
	}
	if strings.TrimSpace(server) == "" {
		server = "http://127.0.0.1:4096"
	}
	server, authorization, err := openCodeConnection(server, authorization, false)
	if err != nil {
		return nil, err
	}
	u, err := serverURL(server)
	if err != nil {
		return nil, err
	}
	host := u.Hostname()
	if !strings.EqualFold(host, "localhost") && host != "127.0.0.1" && host != "::1" {
		return nil, fmt.Errorf("AI server over SSH must use a remote loopback URL (localhost, 127.0.0.1 or [::1])")
	}
	if port := u.Port(); port != "" {
		n, err := strconv.Atoi(port)
		if err != nil || n < 1 || n > 65535 {
			return nil, fmt.Errorf("invalid AI server port")
		}
	}
	return &OpenCodeSSH{server: u, destination: destination, authorization: authorization}, nil
}

func (c *OpenCodeSSH) Generate(ctx context.Context, prompt string, model *Model) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Minute)
	defer cancel()
	tunnel, err := c.startTunnel(ctx)
	if err != nil {
		return "", err
	}
	defer tunnel.close()
	client := newOpenCode(c.server.String(), c.authorization)
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	// Keep the remote URL's Host, TLS name and base path, but dial only our local
	// forward. This also avoids accidentally matching a local service's login.
	transport.DialContext = func(ctx context.Context, network, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, network, tunnel.address)
	}
	defer transport.CloseIdleConnections()
	client.client.Transport = transport
	text, err := client.Generate(ctx, prompt, model)
	if ctx.Err() != nil {
		return "", ctx.Err()
	}
	if err != nil {
		select {
		case <-tunnel.done:
			return "", tunnel.failure()
		default:
		}
	}
	return text, err
}

// exec writes stderr from one goroutine; read it only after done is closed.
type sshOutput struct{ buffer bytes.Buffer }

func (b *sshOutput) Write(p []byte) (int, error) {
	n := len(p)
	if remaining := 4096 - b.buffer.Len(); remaining > 0 {
		if len(p) > remaining {
			p = p[:remaining]
		}
		b.buffer.Write(p)
	}
	return n, nil
}

type sshTunnel struct {
	command *exec.Cmd
	address string
	stderr  sshOutput
	done    chan struct{}
	err     error
}

func (c *OpenCodeSSH) startTunnel(ctx context.Context) (*sshTunnel, error) {
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		return nil, fmt.Errorf("allocate SSH forward: %w", err)
	}
	address := listener.Addr().String()
	// ssh must own the listener. ExitOnForwardFailure makes a port collision fail
	// rather than leaving a tunnel process running without its requested forward.
	if err := listener.Close(); err != nil {
		return nil, err
	}
	port := c.server.Port()
	if port == "" {
		port = "80"
		if c.server.Scheme == "https" {
			port = "443"
		}
	}
	forward := address + ":" + net.JoinHostPort(c.server.Hostname(), port)
	command := exec.CommandContext(ctx, "ssh", "-n", "-T", "-N",
		"-o", "BatchMode=yes", "-o", "ExitOnForwardFailure=yes",
		"-o", "ConnectTimeout=10", "-o", "ServerAliveInterval=15", "-o", "ServerAliveCountMax=3",
		"-o", "ControlPath=none", "-o", "ForkAfterAuthentication=no",
		"-L", forward, "--", c.destination)
	command.WaitDelay = time.Second
	tunnel := &sshTunnel{command: command, address: address, done: make(chan struct{})}
	command.Stderr = &tunnel.stderr
	if err := command.Start(); err != nil {
		return nil, fmt.Errorf("start AI SSH tunnel: %w", err)
	}
	go func() {
		tunnel.err = command.Wait()
		close(tunnel.done)
	}()
	if err := tunnel.waitReady(ctx); err != nil {
		tunnel.close()
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, err
	}
	return tunnel, nil
}

func (t *sshTunnel) close() {
	_ = t.command.Process.Kill()
	<-t.done // Reap the subprocess before releasing this batch, including on cancel.
}

func (t *sshTunnel) failure() error {
	return fmt.Errorf("AI SSH tunnel exited (%v): %s", t.err, strings.TrimSpace(t.stderr.buffer.String()))
}

func (t *sshTunnel) waitReady(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	ticker := time.NewTicker(25 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return fmt.Errorf("waiting for AI SSH tunnel: %w", ctx.Err())
		case <-t.done:
			return t.failure()
		case <-ticker.C:
			connection, err := (&net.Dialer{Timeout: 100 * time.Millisecond}).DialContext(ctx, "tcp4", t.address)
			if err != nil {
				continue
			}
			connection.Close()
			select {
			case <-t.done:
				return t.failure()
			default:
				return nil
			}
		}
	}
}
