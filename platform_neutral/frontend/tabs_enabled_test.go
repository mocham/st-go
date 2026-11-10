//go:build terminal_tabs

package frontend

import (
	"bytes"
	"image"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"st-go/config"
	"st-go/platform_dependent/web"
	"st-go/platform_neutral/api"
	tabprotocol "st-go/tui/tabs"
	"st-go/widgets"
)

func tabTestTerminal(t *testing.T) (*Terminal, *terminalTabs, *bytes.Buffer) {
	t.Helper()
	d, err := web.New(api.WindowOptions{Cols: 40, Rows: 10, CellWidth: 4, CellHeight: 8, Border: 1}, web.Options{Port: 0})
	if err != nil {
		t.Fatal(err)
	}
	w, err := widgets.Wrap(d)
	if err != nil {
		d.Close()
		t.Fatal(err)
	}
	cfg := config.Default()
	cfg.Cols, cfg.Rows = 40, 10
	cfg.GlyphWidth, cfg.GlyphHeight, cfg.GlyphBaseline = 4, 8, 6
	cfg.Borderpx = 1
	cfg.WebPCachePath = ""
	root, err := NewTerminalWidget(cfg, w, 1)
	if err != nil {
		w.Close()
		t.Fatal(err)
	}
	configureTerminalTabs(root, true)
	m := root.tabs.(*terminalTabs)
	replies := &bytes.Buffer{}
	root.termCore.SetWriter(func(data []byte) { replies.Write(data) })
	t.Cleanup(func() {
		done := make(chan struct{})
		if m.control.PostWindow(func() {
			for _, entry := range append([]terminalTab(nil), m.entries...) {
				entry.terminal.closeSession()
			}
			close(done)
		}) {
			select {
			case <-done:
			case <-time.After(3 * time.Second):
				t.Error("tab cleanup deadlocked")
			}
		}
		root.closeSession()
		d.Close()
	})
	return root, m, replies
}

func tabWindow(t *testing.T, m *terminalTabs, call func()) {
	t.Helper()
	done := make(chan struct{})
	if !m.control.PostWindow(func() { call(); close(done) }) {
		t.Fatal("window already closed")
	}
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("tab policy blocked")
	}
}

func spawnTestTab(t *testing.T, root *Terminal, request api.TabSpawnRequest) string {
	t.Helper()
	id := newGeometryToken()
	frame, err := tabprotocol.RequestFrame(root.cfg.TabSpawnToken, id, request)
	if err != nil {
		t.Fatal(err)
	}
	root.writeTerminalOutput(root.termCore, []byte(frame))
	return id
}

func TestTabsInitializeLazilyAppendDetachedAndKeepIndependentPTYs(t *testing.T) {
	root, m, replies := tabTestTerminal(t)
	original := root.termCore
	originalBounds := root.Widget().Bounds()
	tabWindow(t, m, func() {
		if m.bar != nil || len(m.entries) != 1 {
			t.Error("tabs initialized at startup")
		}
	})
	marker := filepath.Join(t.TempDir(), "child")
	probeID := spawnTestTab(t, root, api.TabSpawnRequest{Probe: true})
	tabWindow(t, m, func() {
		if m.bar != nil || len(m.entries) != 1 {
			t.Error("read-only probe initialized tabs")
		}
	})
	root.mu.Lock()
	probeReply := replies.String()
	root.mu.Unlock()
	if !strings.Contains(probeReply, "tab-spawn "+probeID+" supported 1") {
		t.Fatal("read-only support probe failed", probeReply)
	}
	request := api.TabSpawnRequest{Command: []string{"/bin/bash", "-c", `printf '%s|%s|%s' "$PWD" "$ST_GO_BACKEND" "$ST_GO_TABS" > "$1"; IFS= read -r key`, "tab", marker}, Directory: t.TempDir(), Title: "background", Detached: true}
	id := spawnTestTab(t, root, request)
	var child *Terminal
	tabWindow(t, m, func() {
		if m.bar == nil || len(m.entries) != 2 || m.active != 0 || m.entries[0].terminal != root || root.termCore != original {
			t.Error("first spawn replaced/reordered/focused the original terminal")
		}
		child = m.entries[1].terminal
		if child.Widget().Visible() || !root.Widget().Visible() || root.Widget().Bounds() == originalBounds {
			t.Error("detached spawn visibility or lazy layout is incorrect")
		}
		if child.cfg.TabSpawnToken == root.cfg.TabSpawnToken {
			t.Error("PTY capabilities were reused")
		}
	})
	deadline := time.Now().Add(3 * time.Second)
	for {
		data, err := os.ReadFile(marker)
		if err == nil && string(data) == request.Directory+"|web|1" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("background PTY did not run with its cwd/environment", err, string(data))
		}
		time.Sleep(time.Millisecond)
	}
	root.mu.Lock()
	reply := replies.String()
	root.mu.Unlock()
	if !strings.Contains(reply, "tab-spawn "+id+" accepted ") {
		t.Fatal("no spawn acknowledgement", reply)
	}
	// Duplicate delivery of a recently handled request must not start another PTY.
	frame, _ := tabprotocol.RequestFrame(root.cfg.TabSpawnToken, id, request)
	root.writeTerminalOutput(root.termCore, []byte(frame))
	tabWindow(t, m, func() {
		if len(m.entries) != 2 {
			t.Error("duplicate request spawned twice")
		}
		m.selectTab(1)
		if root.Widget().Visible() || !child.Widget().Visible() {
			t.Error("tab switch did not route visibility")
		}
	})
	child.writeTerminalOutput(child.termCore, []byte("\x1b]0;renamed\a"))
	tabWindow(t, m, func() {
		if m.entries[1].title != "renamed" {
			t.Error("tab title did not track its owner")
		}
		if !m.keyPress(XKPrior, ControlMask|ShiftMask) || m.active != 0 {
			t.Error("previous-tab shortcut failed")
		}
		m.selectTab(1)
	})
	// Closing tab 1 must not terminate the window or the detached PTY.
	root.closeSession()
	tabWindow(t, m, func() {
		if len(m.entries) != 1 || m.entries[0].terminal != child || !child.Widget().Visible() || child.closed() {
			t.Error("closing the original terminal killed or hid the remaining tab")
		}
	})
}

func TestFailedTabSpawnDoesNotInitializeOrResize(t *testing.T) {
	root, m, replies := tabTestTerminal(t)
	bounds := root.Widget().Bounds()
	id := spawnTestTab(t, root, api.TabSpawnRequest{Command: []string{"/nonexistent/st-go-tab-test"}, Directory: t.TempDir(), Detached: true})
	tabWindow(t, m, func() {
		if m.bar != nil || len(m.entries) != 1 || root.Widget().Bounds() != bounds {
			t.Error("failed spawn initialized tabs")
		}
	})
	root.mu.Lock()
	defer root.mu.Unlock()
	if !strings.Contains(replies.String(), "tab-spawn "+id+" error ") {
		t.Fatal("failed exec was not rejected", replies.String())
	}
}

func TestTabTemporaryFileHandoffAndSynchronizedActivation(t *testing.T) {
	root, m, _ := tabTestTerminal(t)
	root.writeTerminalOutput(root.termCore, []byte("\x1b[?25l\x1b[48;2;17;34;51m\x1b[2J"))
	root.paintTerminalOutput(root.termCore)
	root.writeTerminalOutput(root.termCore, []byte("\x1b[?2026h\x1b[48;2;170;187;204m\x1b[2J"))
	dir := t.TempDir()
	source := filepath.Join(dir, "a '; 世界.txt")
	if err := os.WriteFile(source, []byte("retained"), 0600); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(dir, "copied")
	index := 5
	request := api.TabSpawnRequest{Command: []string{"/bin/bash", "-c", `printf '%s' "$2" > "$1"; IFS= read -r key`, "tab", marker, source}, Directory: dir, Detached: true}
	request.CopyFile = &index
	spawnTestTab(t, root, request)
	var child *Terminal
	tabWindow(t, m, func() { child = m.entries[1].terminal; m.selectTab(1); m.selectTab(0) })
	root.mu.Lock()
	pixel := root.framebuf[root.fbW+1]
	root.mu.Unlock()
	if pixel != 0xff112233 {
		t.Fatalf("tab initialization/activation exposed uncommitted pixels: %#x", pixel)
	}
	deadline := time.Now().Add(3 * time.Second)
	var copied string
	for {
		data, _ := os.ReadFile(marker)
		copied = string(data)
		if copied != "" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("copied argument not delivered")
		}
		time.Sleep(time.Millisecond)
	}
	if copied == source {
		t.Fatal("temporary file was not transferred to terminal ownership")
	}
	if err := os.Remove(source); err != nil {
		t.Fatal(err)
	}
	if data, err := os.ReadFile(copied); err != nil || string(data) != "retained" {
		t.Fatal("browser cleanup invalidated the new tab's file", err)
	}
	child.mu.Lock()
	child.termCore.WriteToTTY([]byte("\n"), false)
	child.mu.Unlock()
	deadline = time.Now().Add(3 * time.Second)
	for {
		_, err := os.Stat(copied)
		if os.IsNotExist(err) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("terminal-owned file outlived its PTY")
		}
		time.Sleep(time.Millisecond)
	}
	tabWindow(t, m, func() {
		m.resize(80, 50)
		if root.Widget().Bounds().Intersect(m.bar.Bounds()) != (image.Rectangle{}) {
			t.Error("tab bar overlaps terminal")
		}
	})
}

func TestActiveAndBackgroundPTYCrashesAreTabLocal(t *testing.T) {
	for _, active := range []bool{false, true} {
		t.Run(map[bool]string{false: "background", true: "active"}[active], func(t *testing.T) {
			root, m, _ := tabTestTerminal(t)
			spawnTestTab(t, root, api.TabSpawnRequest{Command: []string{"/bin/bash", "-c", "IFS= read -r key"}, Directory: t.TempDir(), Detached: true})
			var child *Terminal
			tabWindow(t, m, func() {
				child = m.entries[1].terminal
				if active {
					m.selectTab(1)
				}
			})
			if err := child.session.child.Process.Kill(); err != nil {
				t.Fatal(err)
			}
			deadline := time.Now().Add(3 * time.Second)
			for {
				remaining := 0
				tabWindow(t, m, func() { remaining = len(m.entries) })
				if remaining == 1 {
					break
				}
				if time.Now().After(deadline) {
					t.Fatal("crashed child was not removed")
				}
				time.Sleep(time.Millisecond)
			}
			tabWindow(t, m, func() {
				if root.closed() || m.active != 0 || !root.Widget().Visible() {
					t.Error("PTY crash closed or unfocused the surviving terminal")
				}
			})
		})
	}
}

func TestAbnormalTabExitRetainsOwnedFileForRecovery(t *testing.T) {
	checkOwnedTabFileRetention(t, false)
}

func TestEditedTabExitRetainsOwnedFileForRecovery(t *testing.T) {
	checkOwnedTabFileRetention(t, true)
}

func checkOwnedTabFileRetention(t *testing.T, edited bool) {
	t.Helper()
	root, m, _ := tabTestTerminal(t)
	dir := t.TempDir()
	source, marker := filepath.Join(dir, "document.txt"), filepath.Join(dir, "copied")
	if err := os.WriteFile(source, []byte("recoverable data"), 0600); err != nil {
		t.Fatal(err)
	}
	index := 5
	code := `printf '%s' "$2" > "$1"; IFS= read -r key`
	if edited {
		code = `printf 'edited data' > "$2"; ` + code
	}
	spawnTestTab(t, root, api.TabSpawnRequest{Command: []string{"/bin/bash", "-c", code, "tab", marker, source}, Directory: dir, Detached: true, CopyFile: &index})
	var child *Terminal
	tabWindow(t, m, func() { child = m.entries[1].terminal })
	deadline := time.Now().Add(3 * time.Second)
	var copied string
	for {
		data, _ := os.ReadFile(marker)
		copied = string(data)
		if copied != "" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("copied argument not delivered")
		}
		time.Sleep(time.Millisecond)
	}
	t.Cleanup(func() { _ = os.RemoveAll(filepath.Dir(copied)) })
	if edited {
		child.mu.Lock()
		child.termCore.WriteToTTY([]byte("\n"), false)
		child.mu.Unlock()
	} else if err := child.session.child.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-child.session.exited:
	case <-time.After(3 * time.Second):
		t.Fatal("crashed child was not reaped")
	}
	want := "recoverable data"
	if edited {
		want = "edited data"
	}
	if data, err := os.ReadFile(copied); err != nil || string(data) != want {
		t.Fatal("crash removed the editor's recovery backing file", err)
	}
}
