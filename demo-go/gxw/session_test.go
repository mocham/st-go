package main

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
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

func TestSessionLaunchUsesExplicitCommandsAndPrivateAuth(t *testing.T) {
	dir := t.TempDir()
	self, _ := os.Executable()
	// PATH contains only an Xorg stand-in; there is no xinit executable.
	script := "#!/bin/sh\nexec '" + strings.ReplaceAll(self, "'", "'\\''") + "' \"$@\"\n"
	if err := os.WriteFile(filepath.Join(dir, "Xorg"), []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"DISPLAY", "WAYLAND_DISPLAY", "XDG_SESSION_TYPE"} {
		t.Setenv(key, "")
	}
	t.Setenv("PATH", dir)
	t.Setenv("GXW_CAPTURE", dir)
	t.Setenv("GXW_TEST_SESSION_HELPER", "success")
	t.Setenv("GORACE", "atexit_sleep_ms=0")
	t.Setenv("XINITRC", "must-not-run")
	t.Setenv("XSERVERRC", "must-not-run")
	t.Setenv("TMPDIR", dir)
	args := []string{"-font-size", "20", "--", "app with spaces", "--", "semi;colon", "世界"}
	if err := launchSession(context.Background(), options{display: ":42", vt: 3}, args); err != nil {
		t.Fatal(err)
	}
	var argv []string
	if err := json.Unmarshal([]byte(readValue(filepath.Join(dir, "server-args"))), &argv); err != nil {
		t.Fatal(err)
	}
	auth := readValue(filepath.Join(dir, "auth-path"))
	want := []string{":42", "-displayfd", "3", "-nolisten", "tcp", "-auth", auth, "-keeptty", "vt3"}
	if !reflect.DeepEqual(argv, want) {
		t.Fatalf("Xorg argv: %q, want %q", argv, want)
	}
	var forwarded []string
	if err := json.Unmarshal([]byte(readValue(filepath.Join(dir, "client-args"))), &forwarded); err != nil || !reflect.DeepEqual(forwarded, append([]string{"--gxw-session"}, args...)) {
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
	assertSessionReaped(t, dir)
}

// The test executable stands in for both children, so shutdown tests do not
// depend on shell traps or external process-management utilities.
func TestMain(m *testing.M) {
	mode := os.Getenv("GXW_TEST_SESSION_HELPER")
	if mode == "" {
		os.Exit(m.Run())
	}
	dir := os.Getenv("GXW_CAPTURE")
	client := len(os.Args) > 1 && os.Args[1] == "--gxw-session"
	role := "server"
	if client {
		role = "client"
	}
	write := func(name string, data []byte) {
		if err := os.WriteFile(filepath.Join(dir, name), data, 0600); err != nil {
			os.Exit(90)
		}
	}
	write(role+"-pid", []byte(fmt.Sprint(os.Getpid())))
	argv, _ := json.Marshal(os.Args[1:])
	write(role+"-args", argv)
	if os.Getenv("XINITRC") != "" || os.Getenv("XSERVERRC") != "" {
		os.Exit(91)
	}
	if client {
		if os.Getenv("DISPLAY") == "" || os.Getenv("GXW_SESSION_CHILD") != "1" || readValue(filepath.Join(dir, "ready")) != "1" {
			os.Exit(92)
		}
		data, err := os.ReadFile(os.Getenv("XAUTHORITY"))
		if err != nil {
			os.Exit(93)
		}
		write("auth", data)
		if info, err := os.Stat(os.Getenv("XAUTHORITY")); err != nil || info.Mode().Perm() != 0600 {
			os.Exit(94)
		}
		if mode == "client-fail" {
			os.Exit(8)
		}
		if mode == "success" || mode == "ignore-stop" {
			os.Exit(0)
		}
	} else {
		write("auth-path", []byte(os.Getenv("XAUTHORITY")))
		if mode == "server-fail" {
			os.Exit(7)
		}
		if mode == "ignore-stop" {
			signal.Ignore(syscall.SIGTERM)
		}
		if mode != "timeout" && mode != "cancel-startup" {
			value := "37\n"
			if len(os.Args) > 1 && strings.HasPrefix(os.Args[1], ":") {
				value = strings.TrimPrefix(os.Args[1], ":") + "\n"
			}
			if mode == "bad-ready" {
				value = "invalid\n"
			}
			write("ready", []byte("1"))
			f := os.NewFile(3, "displayfd")
			if _, err := f.WriteString(value); err != nil {
				os.Exit(95)
			}
			f.Close()
			if mode == "server-exit" {
				time.Sleep(200 * time.Millisecond)
				os.Exit(9)
			}
		}
	}
	if mode == "ignore-stop" {
		for {
			time.Sleep(time.Hour)
		}
	}
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGTERM, os.Interrupt)
	<-quit
	write(role+"-stopped", []byte("1"))
	os.Exit(0)
}

func assertSessionReaped(t *testing.T, dir string) {
	t.Helper()
	for _, role := range []string{"server", "client"} {
		pid, err := strconv.Atoi(readValue(filepath.Join(dir, role+"-pid")))
		if err != nil {
			continue
		}
		if err := syscall.Kill(pid, 0); err != syscall.ESRCH {
			t.Errorf("%s process %d survived/requires reaping: %v", role, pid, err)
		}
	}
}

func TestSessionSupervisorFailureAndCancellation(t *testing.T) {
	for _, tc := range []struct {
		mode, want string
		cancel     bool
	}{
		{"server-fail", "readiness", false}, {"bad-ready", "readiness", false},
		{"timeout", "timed out", false}, {"client-fail", "gxw session", false},
		{"server-exit", "Xorg exited", false}, {"ignore-stop", "", false},
		{"cancel-startup", "", true}, {"cancel-running", "", true},
		{"client-start-fail", "start gxw", false},
	} {
		t.Run(tc.mode, func(t *testing.T) {
			dir := t.TempDir()
			auth := filepath.Join(dir, "authority")
			cookie := bytes.Repeat([]byte{42}, 16)
			if err := writeAuthority(auth, "", cookie); err != nil {
				t.Fatal(err)
			}
			self, _ := os.Executable()
			client := self
			if tc.mode == "client-start-fail" {
				client = filepath.Join(dir, "missing")
			}
			env := append(os.Environ(), "GXW_TEST_SESSION_HELPER="+tc.mode, "GXW_CAPTURE="+dir, "GORACE=atexit_sleep_ms=0")
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if tc.cancel {
				go func() { time.Sleep(300 * time.Millisecond); cancel() }()
			}
			timeouts := sessionTimeouts{startup: 2 * time.Second, shutdown: 100 * time.Millisecond}
			if tc.mode == "timeout" {
				timeouts.startup = 150 * time.Millisecond
			}
			err := superviseSession(ctx, self, client, "", auth, cookie, 0, nil, env, timeouts)
			if tc.want == "" && err != nil || tc.want != "" && (err == nil || !strings.Contains(err.Error(), tc.want)) {
				t.Fatalf("error=%v, want %q", err, tc.want)
			}
			assertSessionReaped(t, dir)
			if tc.mode == "cancel-startup" || tc.mode == "server-fail" || tc.mode == "bad-ready" || tc.mode == "timeout" {
				if _, err := os.Stat(filepath.Join(dir, "client-pid")); !os.IsNotExist(err) {
					t.Fatal("WM started before server readiness")
				}
			}
		})
	}
}
