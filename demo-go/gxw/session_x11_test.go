package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jezek/xgb"
	"github.com/jezek/xgb/xproto"
	"github.com/jezek/xgb/xtest"
)

func TestX11DirectSession(t *testing.T) {
	if os.Getenv("GXW_TEST_X11") != "1" {
		t.Skip("run make test-gxw-x11")
	}
	xvfb, err := exec.LookPath("Xvfb")
	if err != nil {
		t.Fatal(err)
	}
	// Occupy the lowest available display before launching gxw automatically.
	occupied := testDisplay(t)
	binary := os.Getenv("GXW_TEST_BINARY")
	if binary == "" {
		binary, err = filepath.Abs("../../BINARIES/gxw")
		if err != nil {
			t.Fatal(err)
		}
	}
	dir := t.TempDir()
	// Substitute only physical Xorg with Xvfb. The built Go supervisor owns
	// readiness, authentication, WM startup, and both children's lifetime.
	server := `#!/bin/sh
set -eu
printf '%s' "$XAUTHORITY" > "$GXW_CAPTURE/auth-path"
printf '%s' "$$" > "$GXW_CAPTURE/server-pid"
# Xvfb has no console/VT; remove the Xorg-only -keeptty option.
test "$1" = -displayfd
test "$7" = -keeptty
set -- "$1" "$2" "$3" "$4" "$5" "$6"
exec "$GXW_TEST_XVFB" "$@" -screen 0 800x600x24 -noreset
`
	if err := os.WriteFile(filepath.Join(dir, "Xorg"), []byte(server), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "xinit"), []byte("#!/bin/sh\necho invoked > \"$GXW_CAPTURE/xinit-ran\"\nexit 99\n"), 0700); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{".xinitrc", ".xserverrc", ".xprofile", ".xsession"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("#!/bin/sh\necho wrongly-executed > \"$GXW_CAPTURE/config-ran\"\nexit 99\n"), 0700); err != nil {
			t.Fatal(err)
		}
	}
	env := sessionEnvironment(os.Environ(), "", "")
	var consoleEnv []string
	for _, s := range env {
		key, _, _ := strings.Cut(s, "=")
		if key == "XDG_SESSION_TYPE" || key == "XAUTHORITY" || key == "PATH" || key == "HOME" {
			continue
		}
		consoleEnv = append(consoleEnv, s)
	}
	consoleEnv = append(consoleEnv, "PATH="+dir+string(os.PathListSeparator)+os.Getenv("PATH"), "HOME="+dir, "GXW_CAPTURE="+dir, "GXW_TEST_XVFB="+xvfb, "XINITRC="+filepath.Join(dir, ".xinitrc"), "XSERVERRC="+filepath.Join(dir, ".xserverrc"), "XDG_SESSION_TYPE=tty")
	logPath := filepath.Join(dir, "session.log")
	log, err := os.Create(logPath)
	if err != nil {
		t.Fatal(err)
	}
	defer log.Close()
	cmd := exec.Command(binary, "-no-autostart", "--", "/bin/sh", "-c", `printf '%s' "$DISPLAY" > "$GXW_CAPTURE/display"; printf '%s\n' "$@" > "$GXW_CAPTURE/initial-args"`, "gxw-initial", "space here", "--", "世界")
	cmd.Env = consoleEnv
	cmd.Stdout, cmd.Stderr = log, log
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	exited := false
	t.Cleanup(func() {
		if !exited {
			cmd.Process.Signal(os.Interrupt)
			select {
			case <-done:
			case <-time.After(20 * time.Second):
				cmd.Process.Kill()
				<-done
			}
		}
		if t.Failed() {
			b, _ := os.ReadFile(logPath)
			t.Log(string(b))
		}
	})
	deadline := time.Now().Add(10 * time.Second)
	for (readValue(filepath.Join(dir, "auth-path")) == "" || readValue(filepath.Join(dir, "display")) == "") && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	auth := readValue(filepath.Join(dir, "auth-path"))
	if auth == "" {
		t.Fatal("launcher did not start Xorg")
	}
	display := readValue(filepath.Join(dir, "display"))
	if display == "" || display == occupied {
		t.Fatalf("automatic display %q did not avoid occupied %q", display, occupied)
	}
	t.Setenv("XAUTHORITY", auth)
	var conn *xgb.Conn
	for time.Now().Before(deadline) {
		conn, err = xgb.NewConnDisplay(display)
		if err == nil {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	h := &xHarness{t: t, conn: conn, root: xproto.Setup(conn).DefaultScreen(conn).Root, atoms: make(map[string]xproto.Atom)}
	h.wait("supervisor starts WM", func() bool { return len(h.words(h.root, "_NET_SUPPORTING_WM_CHECK")) == 1 })
	h.wait("initial command argument forwarding", func() bool { return readValue(filepath.Join(dir, "initial-args")) == "space here\n--\n世界" })
	if _, err := os.Stat(filepath.Join(dir, "config-ran")); !os.IsNotExist(err) {
		t.Fatal("user configuration was executed")
	}
	if _, err := os.Stat(filepath.Join(dir, "xinit-ran")); !os.IsNotExist(err) {
		t.Fatal("xinit was invoked")
	}
	if info, err := os.Stat(auth); err != nil || info.Mode().Perm() != 0600 {
		t.Fatalf("authority permissions: %v %v", info, err)
	}
	if err := xtest.Init(conn); err != nil {
		t.Fatal(err)
	}
	h.key(0xffe9, 0xffe1, 'q') // Alt+Shift+Q exits WM, then supervisor and X server.
	conn.Close()
	select {
	case err := <-done:
		exited = true
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("supervisor did not end session")
	}
	if _, err := os.Stat(auth); !os.IsNotExist(err) {
		t.Fatalf("authority file survived session: %v", err)
	}
	assertSessionReaped(t, dir)
}
