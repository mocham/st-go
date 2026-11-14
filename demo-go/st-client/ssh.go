package main

import (
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"golang.org/x/net/websocket"
)

// Keep the URL's Host/Origin intact: both HTTP and WebSocket dial a private
// Unix forward, rather than rewriting URLs to an unrelated local TCP port.
type sshTunnel struct {
	mu                   sync.Mutex
	target, dir, forward string
	cmd                  *exec.Cmd
	done                 chan struct{}
	err                  error
	closed               bool
}

func newSSHTunnel(target string, r *remote) (*sshTunnel, error) {
	if target == "" || strings.HasPrefix(target, "-") || strings.ContainsAny(target, " \t\r\n\x00") {
		return nil, fmt.Errorf("-ssh requires an SSH host alias or user@host")
	}
	port := r.base.Port()
	if port == "" {
		port = "80"
		if r.base.Scheme == "https" {
			port = "443"
		}
	}
	host := r.base.Hostname()
	if strings.ContainsAny(host, "/\x00\r\n") {
		return nil, fmt.Errorf("invalid SSH forwarding host")
	}
	dir, err := os.MkdirTemp("", "st-ssh-")
	if err != nil {
		return nil, err
	}
	t := &sshTunnel{target: target, dir: dir, forward: net.JoinHostPort(host, port)}
	// sockaddr_un has a small fixed path budget on Linux.
	// OpenSSH also appends a random suffix while binding its control socket.
	if len(t.socket("control"))+17 >= 104 {
		os.RemoveAll(dir)
		return nil, fmt.Errorf("temporary directory path is too long for SSH sockets; use a shorter TMPDIR")
	}
	r.ssh = t
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	transport.DialContext = t.dial
	r.http.Transport = transport
	return t, nil
}

func (t *sshTunnel) socket(name string) string { return filepath.Join(t.dir, name) }

// Only the initial connection may ask OpenSSH to interact with the terminal.
// Later retries run in batch mode while st-client owns the terminal input.
func (t *sshTunnel) ensure(ctx context.Context, input io.Reader, output io.Writer) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.closed {
		return fmt.Errorf("SSH connection closed")
	}
	if t.cmd != nil {
		select {
		case <-t.done:
			t.cmd = nil
		default:
			return nil
		}
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	os.Remove(t.socket("forward"))
	os.Remove(t.socket("control"))
	args := []string{"-M", "-S", t.socket("control"), "-N", "-T",
		"-o", "ControlMaster=yes", "-o", "ControlPersist=no", "-o", "ForkAfterAuthentication=no",
		"-o", "ExitOnForwardFailure=yes", "-o", "ConnectTimeout=10", "-o", "ServerAliveInterval=15", "-o", "ServerAliveCountMax=3",
		"-L", t.socket("forward") + ":" + t.forward}
	if input == nil {
		args = append(args, "-o", "BatchMode=yes", "-o", "StrictHostKeyChecking=yes")
	}
	args = append(args, "--", t.target)
	cmd := exec.Command("ssh", args...)
	cmd.WaitDelay = time.Second
	diagnostic := &sshOutput{interactive: output}
	cmd.Stdin = input
	cmd.Stdout, cmd.Stderr = diagnostic, diagnostic
	defer diagnostic.quiet()
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("SSH: %w", err)
	}
	t.cmd, t.done = cmd, make(chan struct{})
	done := t.done
	go func() { t.err = cmd.Wait(); close(done) }()
	ticker := time.NewTicker(25 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-done:
			return fmt.Errorf("SSH exited: %v %s", t.err, strings.TrimSpace(diagnostic.String()))
		case <-ctx.Done():
			cmd.Process.Kill()
			<-done
			return ctx.Err()
		case <-ticker.C:
			// Both sockets are created after authentication and successful forward
			// setup. Do not probe the forward with an empty HTTP connection.
			if f, err := os.Stat(t.socket("forward")); err == nil && f.Mode()&os.ModeSocket != 0 {
				if c, err := os.Stat(t.socket("control")); err == nil && c.Mode()&os.ModeSocket != 0 {
					return nil
				}
			}
		}
	}
}

func (t *sshTunnel) dial(ctx context.Context, _, _ string) (net.Conn, error) {
	if err := t.ensure(ctx, nil, nil); err != nil {
		return nil, err
	}
	return (&net.Dialer{}).DialContext(ctx, "unix", t.socket("forward"))
}

func (t *sshTunnel) close() {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.closed = true
	if t.cmd != nil {
		t.cmd.Process.Kill()
		<-t.done
	}
	os.RemoveAll(t.dir)
}

func (r *remote) dialWebSocket(ctx context.Context, config *websocket.Config) (*websocket.Conn, error) {
	if r.ssh == nil {
		return config.DialContext(ctx)
	}
	conn, err := r.ssh.dial(ctx, "", "")
	if err != nil {
		return nil, err
	}
	stop := context.AfterFunc(ctx, func() { conn.Close() })
	defer stop()
	if deadline, ok := ctx.Deadline(); ok {
		conn.SetDeadline(deadline)
	}
	var stream net.Conn = conn
	if r.base.Scheme == "https" {
		secure := tls.Client(conn, &tls.Config{ServerName: r.base.Hostname(), MinVersion: tls.VersionTLS12})
		if err := secure.HandshakeContext(ctx); err != nil {
			conn.Close()
			return nil, err
		}
		stream = secure
	}
	ws, err := websocket.NewClient(config, stream)
	if err != nil {
		conn.Close()
		return nil, err
	}
	if !stop() || ctx.Err() != nil {
		ws.Close()
		return nil, ctx.Err()
	}
	conn.SetDeadline(time.Time{})
	return ws, nil
}

// External tools can be noisy; retain only a bounded diagnostic tail.
type limitedOutput struct{ data []byte }

func (b *limitedOutput) Write(p []byte) (int, error) {
	n := len(p)
	if len(p) >= 4096 {
		b.data = append(b.data[:0], p[len(p)-4096:]...)
	} else {
		b.data = append(b.data, p...)
		if len(b.data) > 4096 {
			b.data = b.data[len(b.data)-4096:]
		}
	}
	return n, nil
}
func (b *limitedOutput) String() string { return string(b.data) }

// OpenSSH diagnostics may arrive after authentication. Once the UI starts,
// retain them without writing asynchronous messages over the terminal screen.
type sshOutput struct {
	mu          sync.Mutex
	interactive io.Writer
	tail        limitedOutput
}

func (b *sshOutput) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.tail.Write(p)
	if b.interactive != nil {
		return b.interactive.Write(p)
	}
	return len(p), nil
}
func (b *sshOutput) quiet() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.interactive = nil
}
func (b *sshOutput) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.tail.String()
}
