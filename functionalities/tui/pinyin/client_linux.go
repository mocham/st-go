//go:build linux

package pinyin

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"sync"
	"time"

	"golang.org/x/sys/unix"
)

type Client struct {
	conn    *net.UnixConn
	scanner *bufio.Scanner
	cmd     *exec.Cmd
	cancel  context.CancelFunc
	exited  chan struct{}
	once    sync.Once
	address string
}

func HelperPath() (string, error) {
	if path := os.Getenv("GOVIM_PINYIN_HELPER"); path != "" {
		return filepath.Abs(path)
	}
	if executable, err := os.Executable(); err == nil {
		path := filepath.Join(filepath.Dir(executable), "govim-pinyind")
		if info, err := os.Stat(path); err == nil && info.Mode().IsRegular() && info.Mode().Perm()&0111 != 0 {
			return path, nil
		}
	}
	path, err := exec.LookPath("govim-pinyind")
	if err != nil {
		return "", fmt.Errorf("CJK requires govim-pinyind (build with make govim)")
	}
	return filepath.Abs(path)
}

// Start uses a random Linux abstract address: there is no socket file, stale
// pathname, or user dictionary on disk. Both peers verify kernel credentials.
func Start(ctx context.Context, helper string) (*Client, error) {
	c, err := launch(ctx, helper)
	if err != nil {
		return nil, err
	}
	return c.connect(ctx)
}

// NewProcessWorker performs only exec.Start synchronously. The caller can keep
// ordinary insertion when spawning fails, without waiting for native startup.
// This function is called lazily on an explicit input-method trigger.
func NewProcessWorker(helper string) (*Worker, error) {
	ctx, cancel := context.WithCancel(context.Background())
	c, err := launch(ctx, helper)
	if err != nil {
		cancel()
		return nil, err
	}
	return newWorker(ctx, cancel, func(ctx context.Context) (Service, error) { return c.connect(ctx) }), nil
}

func launch(ctx context.Context, helper string) (*Client, error) {
	var random [16]byte
	if _, err := rand.Read(random[:]); err != nil {
		return nil, err
	}
	address := "@govim-pinyin-" + hex.EncodeToString(random[:])
	child, cancel := context.WithCancel(ctx)
	cmd := exec.CommandContext(child, helper, "--socket", address, "--parent", strconv.Itoa(os.Getpid()))
	if err := cmd.Start(); err != nil {
		cancel()
		return nil, err
	}
	c := &Client{cmd: cmd, cancel: cancel, exited: make(chan struct{}), address: address}
	go func() { cmd.Wait(); close(c.exited) }()
	return c, nil
}

func (c *Client) connect(ctx context.Context) (*Client, error) {
	failed := true
	defer func() {
		if failed {
			c.Close()
		}
	}()
	deadline := time.Now().Add(2 * time.Second)
	for {
		conn, err := net.DialUnix("unix", nil, &net.UnixAddr{Name: c.address, Net: "unix"})
		if err == nil {
			c.conn = conn
			break
		}
		if time.Now().After(deadline) {
			return nil, fmt.Errorf("pinyin helper startup timed out")
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-c.exited:
			return nil, fmt.Errorf("pinyin helper exited during startup")
		case <-time.After(5 * time.Millisecond):
		}
	}
	if err := CheckPeer(c.conn, c.cmd.Process.Pid); err != nil {
		return nil, err
	}
	if err := c.conn.SetDeadline(deadline); err != nil {
		return nil, err
	}
	c.scanner = Scanner(c.conn)
	var ready Response
	if err := Read(c.scanner, &ready); err != nil {
		return nil, fmt.Errorf("pinyin helper startup: %w", err)
	}
	if !ready.Ready || ready.Error != "" {
		return nil, fmt.Errorf("pinyin helper initialization failed")
	}
	failed = false
	return c, nil
}

func CheckPeer(conn *net.UnixConn, pid int) error {
	raw, err := conn.SyscallConn()
	if err != nil {
		return err
	}
	var credential *unix.Ucred
	var inner error
	if err := raw.Control(func(fd uintptr) { credential, inner = unix.GetsockoptUcred(int(fd), unix.SOL_SOCKET, unix.SO_PEERCRED) }); err != nil {
		return err
	}
	if inner != nil {
		return inner
	}
	if credential == nil || int(credential.Pid) != pid || int(credential.Uid) != os.Getuid() {
		return fmt.Errorf("unexpected pinyin socket peer")
	}
	return nil
}
func (c *Client) Lookup(ctx context.Context, r Request) (Response, error) {
	var response Response
	if err := r.Validate(); err != nil {
		return response, err
	}
	deadline := time.Now().Add(750 * time.Millisecond)
	if end, ok := ctx.Deadline(); ok && end.Before(deadline) {
		deadline = end
	}
	if err := ctx.Err(); err != nil {
		return response, err
	}
	if err := c.conn.SetDeadline(deadline); err != nil {
		return response, err
	}
	if err := Write(c.conn, r); err != nil {
		return response, err
	}
	if err := Read(c.scanner, &response); err != nil {
		return response, err
	}
	return response, response.Validate(r)
}
func (c *Client) Exited() <-chan struct{} { return c.exited }
func (c *Client) PID() int                { return c.cmd.Process.Pid }
func (c *Client) Close() error {
	if c == nil {
		return nil
	}
	c.once.Do(func() {
		c.cancel()
		if c.conn != nil {
			c.conn.Close()
		}
	})
	<-c.exited
	return nil
}
