package main

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestRejectExistingGraphicalEnvironment(t *testing.T) {
	for _, env := range []map[string]string{{"DISPLAY": ":0"}, {"DISPLAY": "remote:10.0"}, {"WAYLAND_DISPLAY": "wayland-1"}, {"XDG_SESSION_TYPE": "wayland"}, {"XDG_SESSION_TYPE": "x11"}} {
		if err := checkConsole(func(k string) string { return env[k] }); err == nil || !strings.Contains(err.Error(), "text console") {
			t.Fatalf("accepted graphical environment: %v", env)
		}
	}
	if err := checkConsole(func(k string) string {
		if k == "XDG_SESSION_TYPE" {
			return "tty"
		}
		return ""
	}); err != nil {
		t.Fatal(err)
	}
}

func TestNextDisplay(t *testing.T) {
	root := t.TempDir()
	sockets := filepath.Join(root, ".X11-unix")
	if err := os.Mkdir(sockets, 0700); err != nil {
		t.Fatal(err)
	}
	lock := filepath.Join(root, ".X0-lock")
	if err := os.WriteFile(lock, []byte("reserved\n"), 0600); err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("unix", filepath.Join(sockets, "X1"))
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	if err := os.Symlink("missing-socket", filepath.Join(sockets, "X2")); err != nil {
		t.Fatal(err)
	}
	abstract, err := net.Listen("unix", "\x00"+filepath.Join(sockets, "X3"))
	if err != nil {
		t.Fatal(err)
	}
	defer abstract.Close()
	if display, err := nextAvailableDisplay(root); err != nil || display != ":4" {
		t.Fatalf("occupied displays were not skipped: %q, %v", display, err)
	}
	abstract.Close()
	if display, err := nextAvailableDisplay(root); err != nil || display != ":3" {
		t.Fatalf("released abstract display: %q, %v", display, err)
	}
	if err := os.Remove(lock); err != nil {
		t.Fatal(err)
	}
	if display, err := nextAvailableDisplay(root); err != nil || display != ":0" {
		t.Fatalf("lowest available display: %q, %v", display, err)
	}
}

func TestSessionLaunchUsesExplicitCommandsAndPrivateAuth(t *testing.T) {
	dir := t.TempDir()
	// Capture the real launchSession exec arguments and environment. This stub
	// models xinit's boundary; the isolated X11 test also exercises real xinit.
	script := `#!/bin/sh
set -eu
printf '%s\n' "$@" > "$GXW_CAPTURE/argv"
printf '%s' "$GXW_SESSION_ARGS" > "$GXW_CAPTURE/client-args"
printf '%s' "$XAUTHORITY" > "$GXW_CAPTURE/auth-path"
/bin/cat "$XAUTHORITY" > "$GXW_CAPTURE/auth"
test -z "${XINITRC-}"
test -z "${XSERVERRC-}"
test -z "${DISPLAY-}"
test "$GXW_XINIT_CHILD" = 1
test "$XDG_SESSION_TYPE" = x11
`
	for name, contents := range map[string]string{"xinit": script, "Xorg": "#!/bin/sh\nexit 99\n"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(contents), 0700); err != nil {
			t.Fatal(err)
		}
	}
	for _, key := range []string{"DISPLAY", "WAYLAND_DISPLAY", "XDG_SESSION_TYPE"} {
		t.Setenv(key, "")
	}
	t.Setenv("PATH", dir)
	t.Setenv("GXW_CAPTURE", dir)
	t.Setenv("XINITRC", "must-not-run")
	t.Setenv("XSERVERRC", "must-not-run")
	t.Setenv("TMPDIR", dir)
	args := []string{"-font-size", "20", "--", "app with spaces", "--", "semi;colon", "世界"}
	if err := launchSession(context.Background(), options{display: ":42", vt: 3}, args); err != nil {
		t.Fatal(err)
	}
	argv := strings.Split(strings.TrimSpace(readValue(filepath.Join(dir, "argv"))), "\n")
	self, _ := os.Executable()
	auth := readValue(filepath.Join(dir, "auth-path"))
	want := []string{self, "--gxw-session", "--", filepath.Join(dir, "Xorg"), ":42", "-nolisten", "tcp", "-auth", auth, "vt3", "-keeptty"}
	if !reflect.DeepEqual(argv, want) {
		t.Fatalf("xinit argv: %q, want %q", argv, want)
	}
	var forwarded []string
	if err := json.Unmarshal([]byte(readValue(filepath.Join(dir, "client-args"))), &forwarded); err != nil || !reflect.DeepEqual(forwarded, args) {
		t.Fatalf("client args: %q, %v", forwarded, err)
	}
	if _, err := os.Stat(filepath.Dir(auth)); !os.IsNotExist(err) {
		t.Fatalf("temporary session directory not removed: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(dir, "auth"))
	if err != nil {
		t.Fatal(err)
	}
	r := bytes.NewReader(data)
	var family uint16
	if err := binary.Read(r, binary.BigEndian, &family); err != nil || family != 256 {
		t.Fatal("bad Xauthority family")
	}
	var fields [][]byte
	for r.Len() > 0 {
		var n uint16
		if err := binary.Read(r, binary.BigEndian, &n); err != nil {
			t.Fatal(err)
		}
		field := make([]byte, n)
		if _, err := r.Read(field); err != nil {
			t.Fatal(err)
		}
		fields = append(fields, field)
	}
	host, _ := os.Hostname()
	if len(fields) != 4 || string(fields[0]) != host || string(fields[1]) != "42" || string(fields[2]) != "MIT-MAGIC-COOKIE-1" || len(fields[3]) != 16 {
		t.Fatalf("bad authorization record: %q", fields)
	}
}
