//go:build terminal_tabs

package frontend

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"golang.org/x/net/websocket"
)

func TestRuntimeTabFlagsRemoveCapabilities(t *testing.T) {
	terminal, _ := filepath.Abs("BINARIES/st-web")
	if _, err := os.Stat(terminal); err != nil {
		t.Skip("build st-web")
	}
	for _, flag := range []string{"-no-tabs", "-tabs=false"} {
		t.Run(flag, func(t *testing.T) {
			marker := filepath.Join(t.TempDir(), "capability")
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, terminal, "-port", "0", flag, "-e", "/bin/sh", "-c", `printf '%s|%s' "${ST_GO_TABS-}" "${ST_GO_TAB_TOKEN-}" > "$1"`, "tab-test", marker)
			cmd.Env = append(os.Environ(), "ST_GO_TABS=1", "ST_GO_TAB_TOKEN=inherited")
			if output, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("disabled tabs did not exit cleanly: %v\n%s", err, output)
			}
			if data, err := os.ReadFile(marker); err != nil || string(data) != "|" {
				t.Fatalf("disabled tabs advertised stale support: %q, %v", data, err)
			}
		})
	}
}

func TestWebFileBrowserFocusedTabsAndOriginalPTYExit(t *testing.T) {
	terminal, _ := filepath.Abs("BINARIES/st-web")
	browser, _ := filepath.Abs("BINARIES/file-browser")
	for _, file := range []string{terminal, browser} {
		if _, err := os.Stat(file); err != nil {
			t.Skip("build st-web and file-browser")
		}
	}
	dir, tools := t.TempDir(), t.TempDir()
	file := filepath.Join(dir, "a '; 世界.txt")
	if err := os.WriteFile(file, []byte("document"), 0600); err != nil {
		t.Fatal(err)
	}
	ready, input, command := filepath.Join(tools, "ready"), filepath.Join(tools, "input"), filepath.Join(tools, "browser-command")
	pidFile := filepath.Join(tools, "browser-pid")
	script := `#!/bin/bash
stty -echo -icanon min 1 time 0
printf '%s|%s|%s' "$1" "$PWD" "$ST_GO_TABS" > "$TAB_READY"
while IFS= read -r -n 1 key; do
  printf '%s' "$key" >> "$TAB_INPUT"
  [[ $key == Z ]] && break
done
`
	if err := os.WriteFile(filepath.Join(tools, "govim"), []byte(script), 0755); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(terminal, "-port", "0", "-dir", dir, "-e", browser, dir)
	cmd.Env = append(envWithout(os.Environ(), "ST_FILE_BROWSER_OPEN", "ST_FILE_BROWSER_OPEN_ALL", "ST_FILE_BROWSER_OPEN_WEB", "PATH"), "PATH="+tools+":/usr/bin:/bin", "TAB_READY="+ready, "TAB_INPUT="+input)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	exited := make(chan struct{})
	var exitErr error
	go func() { exitErr = cmd.Wait(); close(exited) }()
	t.Cleanup(func() { _ = cmd.Process.Kill(); <-exited })
	urls := make(chan string, 1)
	go func() {
		scanner := bufio.NewScanner(stdout)
		for scanner.Scan() {
			if strings.HasPrefix(scanner.Text(), "Web terminal: ") {
				select {
				case urls <- strings.TrimPrefix(scanner.Text(), "Web terminal: "):
				default:
				}
			}
		}
	}()
	var address string
	select {
	case address = <-urls:
	case <-time.After(5 * time.Second):
		t.Fatal("web server did not start")
	}
	origin := address[:strings.Index(address[7:], "/")+7]
	conn, err := websocket.Dial("ws"+strings.TrimPrefix(address, "http")+"ws", "", origin)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	conn.SetReadDeadline(time.Now().Add(15 * time.Second))
	var hello struct {
		Type      string
		CellWidth int
	}
	if err := websocket.JSON.Receive(conn, &hello); err != nil || hello.Type != "hello" {
		t.Fatal("web hello", err)
	}
	go func() {
		for {
			var frame []byte
			if websocket.Message.Receive(conn, &frame) != nil {
				return
			}
		}
	}()
	send := func(message map[string]any) {
		t.Helper()
		if err := websocket.JSON.Send(conn, message); err != nil {
			t.Fatal("web input", err)
		}
	}
	key := func(value string, mods uint16) { send(map[string]any{"type": "key", "key": value, "modifiers": mods}) }
	text := func(value string) { send(map[string]any{"type": "text", "text": value}) }
	await := func(path, want string) {
		t.Helper()
		deadline := time.Now().Add(5 * time.Second)
		for {
			data, err := os.ReadFile(path)
			if err == nil && string(data) == want {
				return
			}
			select {
			case <-exited:
				data, _ = os.ReadFile(path)
				if string(data) == want {
					return
				}
				t.Fatalf("terminal exited early: %v\n%s", exitErr, stderr.String())
			default:
			}
			if time.Now().After(deadline) {
				t.Fatalf("%s: got %q, expected %q", path, data, want)
			}
			time.Sleep(10 * time.Millisecond)
		}
	}
	// An explicit refresh acts as an input-loop-ready barrier before selection.
	send(map[string]any{"type": "resize", "width": 800, "height": 600})
	text(":printf ready > '" + filepath.Join(tools, "browser-ready") + "'")
	key("Enter", 0)
	await(filepath.Join(tools, "browser-ready"), "ready")
	key("ArrowDown", 0)
	key("Enter", 0)
	await(ready, file+"|"+dir+"|1")
	// A successful browser launch must focus the new PTY without sharing input.
	text("C")
	await(input, "C")
	key("PageUp", ControlMask|ShiftMask)
	text(":printf active > '" + command + "'; printf '%s' \"$PPID\" > '" + pidFile + "'")
	key("Enter", 0)
	await(command, "active")
	// The bottom bar routes native clicks to the new tab; the shortcut returns.
	x := (terminalTabWidthCells + 1) * hello.CellWidth
	send(map[string]any{"type": "press", "button": 1, "x": x, "y": 590})
	send(map[string]any{"type": "release", "button": 1, "x": x, "y": 590})
	text("D")
	await(input, "CD")
	key("PageUp", ControlMask|ShiftMask)
	pidBytes, err := os.ReadFile(pidFile)
	if err != nil {
		t.Fatal(err)
	}
	pid, err := strconv.Atoi(string(pidBytes))
	if err != nil || pid < 1 {
		t.Fatal("invalid browser PID", string(pidBytes))
	}
	text("q") // original browser exits, but its detached PTY must remain alive.
	deadline := time.Now().Add(5 * time.Second)
	for syscall.Kill(pid, 0) != syscall.ESRCH {
		if time.Now().After(deadline) {
			t.Fatal("original browser did not exit")
		}
		time.Sleep(10 * time.Millisecond)
	}
	key("PageDown", ControlMask|ShiftMask)
	text("Z")
	await(input, "CDZ")
	select {
	case <-exited:
		if exitErr != nil {
			t.Fatal(fmt.Errorf("terminal exit: %w", exitErr))
		}
	case <-time.After(5 * time.Second):
		t.Fatal("last tab did not close the terminal")
	}
}
