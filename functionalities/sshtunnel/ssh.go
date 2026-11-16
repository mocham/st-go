// Package sshtunnel manages private SSH forwards to remote loopback HTTP servers.
package sshtunnel

import (
	"bytes"
	"context"
	"fmt"
	"net"
	"net/url"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"time"
)

const DefaultServer = "http://127.0.0.1:4096"

// Config is immutable and starts no subprocess until Open is called.
type Config struct {
	server      *url.URL
	destination string
}

var destinationPattern = regexp.MustCompile(`^([a-zA-Z0-9_][a-zA-Z0-9_.-]*@)?([a-zA-Z0-9_][a-zA-Z0-9_.-]*|\[[0-9a-fA-F:]+\])$`)

// New accepts [user@]host or an SSH config alias. Ports, identity files and jump
// hosts belong in SSH config. server names the HTTP endpoint on the remote host.
func New(server, destination string) (*Config, error) {
	destination = strings.TrimSpace(destination)
	if !destinationPattern.MatchString(destination) {
		return nil, fmt.Errorf("SSH destination must be [user@]host or an SSH config alias")
	}
	server = strings.TrimRight(strings.TrimSpace(server), "/")
	if server == "" {
		server = DefaultServer
	} else if !strings.Contains(server, "://") {
		server = "http://" + server
	}
	u, err := url.Parse(server)
	if err != nil || u.Host == "" || u.Scheme != "http" && u.Scheme != "https" || u.RawQuery != "" || u.Fragment != "" || u.User != nil {
		return nil, fmt.Errorf("invalid SSH server URL")
	}
	host := u.Hostname()
	if !strings.EqualFold(host, "localhost") && host != "127.0.0.1" && host != "::1" {
		return nil, fmt.Errorf("server over SSH must use a remote loopback URL (localhost, 127.0.0.1 or [::1])")
	}
	if port := u.Port(); port != "" {
		n, err := strconv.Atoi(port)
		if err != nil || n < 1 || n > 65535 {
			return nil, fmt.Errorf("invalid SSH server port")
		}
	}
	return &Config{server: u, destination: destination}, nil
}

func (c *Config) ServerURL() string { return c.server.String() }

// Open waits for a loopback-only local listener. The caller must Close the
// tunnel; cancelling ctx also kills and reaps SSH. There is no lifetime timeout.
func (c *Config) Open(ctx context.Context) (*Tunnel, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
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
	tunnel := &Tunnel{command: command, address: address, done: make(chan struct{})}
	command.Stderr = &tunnel.stderr
	if err := command.Start(); err != nil {
		return nil, fmt.Errorf("start SSH tunnel: %w", err)
	}
	go func() {
		tunnel.err = command.Wait()
		close(tunnel.done)
	}()
	if err := tunnel.waitReady(ctx); err != nil {
		tunnel.Close()
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, err
	}
	return tunnel, nil
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

type Tunnel struct {
	command *exec.Cmd
	address string
	stderr  sshOutput
	done    chan struct{}
	err     error
}

// Close is safe to call repeatedly, including after context cancellation.
func (t *Tunnel) Close() {
	_ = t.command.Process.Kill()
	<-t.done
}

func (t *Tunnel) Done() <-chan struct{} { return t.done }

// Err returns nil while SSH is running, or its bounded diagnostic on exit.
func (t *Tunnel) Err() error {
	select {
	case <-t.done:
		return fmt.Errorf("SSH tunnel exited (%v): %s", t.err, strings.TrimSpace(t.stderr.buffer.String()))
	default:
		return nil
	}
}

// DialContext keeps HTTP's remote Host/TLS name while connecting only through
// the local forward. Disable HTTP proxies and redirects on the calling client.
func (t *Tunnel) DialContext(ctx context.Context, _, _ string) (net.Conn, error) {
	if err := t.Err(); err != nil {
		return nil, err
	}
	return (&net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}).DialContext(ctx, "tcp4", t.address)
}

func (t *Tunnel) waitReady(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	ticker := time.NewTicker(25 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return fmt.Errorf("waiting for SSH tunnel: %w", ctx.Err())
		case <-t.done:
			return t.Err()
		case <-ticker.C:
			connection, err := (&net.Dialer{Timeout: 100 * time.Millisecond}).DialContext(ctx, "tcp4", t.address)
			if err != nil {
				continue
			}
			connection.Close()
			return t.Err()
		}
	}
}
