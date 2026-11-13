package main

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"net"
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

// X servers use /tmp regardless of TMPDIR. Treat existing lock/socket paths
// as reserved, including stale entries; also check Linux's abstract sockets,
// which can exist without either filesystem entry.
func nextAvailableDisplay(root string) (string, error) {
	for n := 0; n <= 65535; n++ {
		socket := filepath.Join(root, ".X11-unix", fmt.Sprintf("X%d", n))
		reserved := false
		for _, path := range []string{filepath.Join(root, fmt.Sprintf(".X%d-lock", n)), socket} {
			if _, err := os.Lstat(path); err == nil {
				reserved = true
				break
			} else if !os.IsNotExist(err) {
				return "", fmt.Errorf("check X display :%d: %w", n, err)
			}
		}
		if reserved {
			continue
		}
		conn, err := net.DialTimeout("unix", "\x00"+socket, 100*time.Millisecond)
		if err == nil {
			conn.Close()
			continue
		}
		if !errors.Is(err, syscall.ECONNREFUSED) && !os.IsNotExist(err) {
			return "", fmt.Errorf("probe X display :%d: %w", n, err)
		}
		return fmt.Sprintf(":%d", n), nil
	}
	return "", fmt.Errorf("no available local X display")
}

func currentVT() int {
	path, _ := os.Readlink("/proc/self/fd/0")
	if !strings.HasPrefix(path, "/dev/tty") {
		return 0
	}
	n, _ := strconv.Atoi(strings.TrimPrefix(path, "/dev/tty"))
	return n
}

func xinitArgs(self, server, display, auth string, vt int) []string {
	result := []string{self, "--gxw-session", "--", server, display, "-nolisten", "tcp", "-auth", auth}
	if vt > 0 {
		result = append(result, fmt.Sprintf("vt%d", vt), "-keeptty")
	}
	return result
}

func sessionEnvironment(base []string, auth string, args []string) []string {
	var result []string
	for _, entry := range base {
		key, _, _ := strings.Cut(entry, "=")
		switch key {
		case "DISPLAY", "WAYLAND_DISPLAY", "XAUTHORITY", "XINITRC", "XSERVERRC", "GXW_XINIT_CHILD", "GXW_SESSION_ARGS", "XDG_SESSION_TYPE", "XDG_CURRENT_DESKTOP", "DESKTOP_SESSION", "SESSION_MANAGER":
			continue
		}
		result = append(result, entry)
	}
	// xinit treats any standalone "--" as its client/server separator. Carry
	// gxw's argv losslessly in the environment so initial commands may use it.
	encoded, _ := json.Marshal(args)
	return append(result, "XAUTHORITY="+auth, "GXW_XINIT_CHILD=1", "GXW_SESSION_ARGS="+string(encoded), "XDG_SESSION_TYPE=x11", "XDG_CURRENT_DESKTOP=gxw", "DESKTOP_SESSION=gxw")
}

// A fresh, private authorization file is shared by our X server and clients.
// No existing ~/.Xauthority or user xauth/xinit script is consulted or changed.
func writeAuthority(path, display string) error {
	host, err := os.Hostname()
	if err != nil {
		return err
	}
	cookie := make([]byte, 16)
	if _, err := rand.Read(cookie); err != nil {
		return err
	}
	data := binary.BigEndian.AppendUint16(nil, 256) // FamilyLocal
	for _, field := range [][]byte{[]byte(host), []byte(strings.TrimPrefix(display, ":")), []byte("MIT-MAGIC-COOKIE-1"), cookie} {
		data = binary.BigEndian.AppendUint16(data, uint16(len(field)))
		data = append(data, field...)
	}
	return os.WriteFile(path, data, 0600)
}

func launchSession(ctx context.Context, o options, args []string) error {
	if err := checkConsole(os.Getenv); err != nil {
		return err
	}
	display, err := localDisplay(o.display)
	if err != nil {
		return err
	}
	if display == "" {
		display, err = nextAvailableDisplay("/tmp")
		if err != nil {
			return err
		}
	}
	vt := o.vt
	if vt == 0 {
		vt = currentVT()
	}
	if vt < 0 || vt > 63 {
		return fmt.Errorf("-vt must be between 1 and 63, or 0 for automatic selection")
	}
	xinit, err := exec.LookPath("xinit")
	if err != nil {
		return fmt.Errorf("xinit is required: %w", err)
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
	if err := writeAuthority(auth, display); err != nil {
		return err
	}
	cmd := exec.CommandContext(ctx, xinit, xinitArgs(self, server, display, auth, vt)...)
	cmd.Env = sessionEnvironment(os.Environ(), auth, args)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	// Let xinit shut down both children and restore the VT on termination.
	cmd.Cancel = func() error { return cmd.Process.Signal(syscall.SIGTERM) }
	cmd.WaitDelay = 15 * time.Second
	if err := cmd.Run(); err != nil && ctx.Err() == nil {
		return fmt.Errorf("xinit: %w", err)
	}
	return nil
}
