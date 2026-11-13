package main

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/binary"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

func checkConsole(getenv func(string) string) error {
	typ := strings.ToLower(getenv("XDG_SESSION_TYPE"))
	if getenv("DISPLAY") != "" || getenv("WAYLAND_DISPLAY") != "" || typ == "x11" || typ == "wayland" {
		return fmt.Errorf("already in an X11 or Wayland environment; run gxw from a Linux text console")
	}
	return nil
}

func localDisplay(value string) (string, error) {
	if value == "" {
		return "", nil
	}
	if !strings.HasPrefix(value, ":") {
		return "", fmt.Errorf("-display must be a local display such as :0")
	}
	n, err := strconv.Atoi(strings.TrimPrefix(value, ":"))
	if err != nil || n < 0 || n > 65535 {
		return "", fmt.Errorf("invalid local display %q", value)
	}
	return fmt.Sprintf(":%d", n), nil
}

func currentVT() int {
	path, _ := os.Readlink("/proc/self/fd/0")
	if !strings.HasPrefix(path, "/dev/tty") {
		return 0
	}
	n, _ := strconv.Atoi(strings.TrimPrefix(path, "/dev/tty"))
	return n
}

func serverArgs(display, auth string, vt int) []string {
	var result []string
	if display != "" {
		result = append(result, display)
	}
	result = append(result, "-displayfd", "3", "-nolisten", "tcp", "-auth", auth, "-keeptty")
	if vt > 0 {
		result = append(result, fmt.Sprintf("vt%d", vt))
	}
	return result
}

func sessionEnvironment(base []string, display, auth string) []string {
	var result []string
	for _, entry := range base {
		key, _, _ := strings.Cut(entry, "=")
		switch key {
		case "DISPLAY", "WAYLAND_DISPLAY", "XAUTHORITY", "XINITRC", "XSERVERRC", "GXW_XINIT_CHILD", "GXW_SESSION_ARGS", "GXW_SESSION_CHILD", "XDG_SESSION_TYPE", "XDG_CURRENT_DESKTOP", "DESKTOP_SESSION", "SESSION_MANAGER":
			continue
		}
		result = append(result, entry)
	}
	if display != "" {
		result = append(result, "DISPLAY="+display)
	}
	return append(result, "XAUTHORITY="+auth, "XDG_SESSION_TYPE=x11", "XDG_CURRENT_DESKTOP=gxw", "DESKTOP_SESSION=gxw")
}

// A fresh, private authorization file is shared by our X server and clients.
// No existing ~/.Xauthority or user xauth/xinit script is consulted or changed.
func writeAuthority(path, display string, cookie []byte) error {
	host, err := os.Hostname()
	if err != nil {
		return err
	}
	data := binary.BigEndian.AppendUint16(nil, 256) // FamilyLocal
	for _, field := range [][]byte{[]byte(host), []byte(strings.TrimPrefix(display, ":")), []byte("MIT-MAGIC-COOKIE-1"), cookie} {
		data = binary.BigEndian.AppendUint16(data, uint16(len(field)))
		data = append(data, field...)
	}
	// Replacing the display number after -displayfd must never expose a partial
	// record. Xorg loads the same cookie before and after this atomic update.
	if err := os.WriteFile(path+".new", data, 0600); err != nil {
		return err
	}
	return os.Rename(path+".new", path)
}

func launchSession(ctx context.Context, o options, args []string) error {
	if err := checkConsole(os.Getenv); err != nil {
		return err
	}
	display, err := localDisplay(o.display)
	if err != nil {
		return err
	}
	vt := o.vt
	if vt == 0 {
		vt = currentVT()
	}
	if vt < 0 || vt > 63 {
		return fmt.Errorf("-vt must be between 1 and 63, or 0 for automatic selection")
	}
	server, err := exec.LookPath("Xorg")
	if err != nil {
		return fmt.Errorf("Xorg is required: %w", err)
	}
	server, err = filepath.Abs(server)
	if err != nil {
		return err
	}
	self, err := os.Executable()
	if err != nil {
		return err
	}
	dir, err := os.MkdirTemp("", "gxw-session-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	auth := filepath.Join(dir, "Xauthority")
	cookie := make([]byte, 16)
	if _, err := rand.Read(cookie); err != nil {
		return err
	}
	if err := writeAuthority(auth, display, cookie); err != nil {
		return err
	}
	return superviseSession(ctx, server, self, display, auth, cookie, vt, args, os.Environ(), sessionTimeouts{startup: 20 * time.Second, shutdown: 10 * time.Second})
}

type sessionTimeouts struct{ startup, shutdown time.Duration }

type sessionProcess struct {
	cmd  *exec.Cmd
	done chan struct{}
	err  error // published by closing done
}

func startSessionProcess(cmd *exec.Cmd) (*sessionProcess, error) {
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	p := &sessionProcess{cmd: cmd, done: make(chan struct{})}
	go func() { p.err = cmd.Wait(); close(p.done) }()
	return p, nil
}

func (p *sessionProcess) stop(timeout time.Duration) {
	select {
	case <-p.done:
		return
	default:
	}
	_ = p.cmd.Process.Signal(syscall.SIGTERM)
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case <-p.done:
	case <-timer.C:
		_ = p.cmd.Process.Kill()
		<-p.done
	}
}

func superviseSession(ctx context.Context, server, self, display, auth string, cookie []byte, vt int, args, baseEnv []string, timeouts sessionTimeouts) error {
	if ctx.Err() != nil {
		return nil
	}
	read, write, err := os.Pipe()
	if err != nil {
		return err
	}
	defer read.Close()
	cmd := exec.Command(server, serverArgs(display, auth, vt)...)
	cmd.Env = sessionEnvironment(baseEnv, "", auth)
	cmd.ExtraFiles = []*os.File{write}
	// Retain the launcher's foreground console and login session. In particular
	// rootless Xorg/logind must not be detached with Setsid or a background tty.
	xserver, err := startSessionProcess(cmd)
	write.Close()
	if err != nil {
		return fmt.Errorf("start Xorg: %w", err)
	}
	defer xserver.stop(timeouts.shutdown)
	type readyResult struct {
		display string
		err     error
	}
	ready := make(chan readyResult, 1)
	go func() {
		line, err := bufio.NewReader(io.LimitReader(read, 32)).ReadString('\n')
		var value string
		if err == nil {
			value, err = localDisplay(":" + strings.TrimSuffix(line, "\n"))
		}
		ready <- readyResult{value, err}
	}()
	timer := time.NewTimer(timeouts.startup)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return nil
	case <-xserver.done:
		return fmt.Errorf("Xorg exited before readiness: %v", xserver.err)
	case <-timer.C:
		return fmt.Errorf("Xorg startup timed out")
	case r := <-ready:
		if r.err != nil {
			return fmt.Errorf("Xorg readiness: %w", r.err)
		}
		if display != "" && display != r.display {
			return fmt.Errorf("Xorg selected %s, requested %s", r.display, display)
		}
		display = r.display
	}
	read.Close()
	if err := writeAuthority(auth, display, cookie); err != nil {
		return err
	}
	if ctx.Err() != nil {
		return nil
	}
	select {
	case <-xserver.done:
		return fmt.Errorf("Xorg exited before WM startup: %v", xserver.err)
	default:
	}
	cmd = exec.Command(self, append([]string{"--gxw-session"}, args...)...)
	cmd.Env = append(sessionEnvironment(baseEnv, display, auth), "GXW_SESSION_CHILD=1")
	client, err := startSessionProcess(cmd)
	if err != nil {
		return fmt.Errorf("start gxw: %w", err)
	}
	defer client.stop(timeouts.shutdown)
	select {
	case <-ctx.Done():
		return nil
	case <-xserver.done:
		return fmt.Errorf("Xorg exited during session: %v", xserver.err)
	case <-client.done:
		if client.err != nil {
			return fmt.Errorf("gxw session: %w", client.err)
		}
		return nil
	}
}
