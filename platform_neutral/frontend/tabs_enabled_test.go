//go:build terminal_tabs

package frontend

import (
	"bytes"
	"image"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"st-go/config"
	"st-go/platform_dependent/web"
	"st-go/platform_neutral/api"
	tabprotocol "st-go/tui/tabs"
	"st-go/widgets"
)

type tabButtonTestHost struct {
	*web.Driver
	newTerminalTab func()
	firstScene     *tabSceneProbe
}

type tabSceneProbe struct {
	api.SceneSurface
	color atomic.Uint32
}

func (p *tabSceneProbe) Apply(u api.SceneUpdate) {
	if u.Reset {
		p.color.Store(u.Background)
	}
	for _, row := range u.Rows {
		if row.Y == 1 {
			color := row.Background
			if len(row.Text) > 0 {
				color = row.Text[0].Background
			}
			p.color.Store(color)
		}
	}
	p.SceneSurface.Apply(u)
}
func (host *tabButtonTestHost) NewSceneSurface(k api.WidgetKind, r image.Rectangle, bg uint32, visible bool) (api.SceneSurface, error) {
	s, err := host.Driver.NewSceneSurface(k, r, bg, visible)
	if err != nil {
		return nil, err
	}
	p := &tabSceneProbe{SceneSurface: s}
	if host.firstScene == nil {
		host.firstScene = p
	}
	return p, nil
}

func (host *tabButtonTestHost) BindNewTerminalTab(handler func()) { host.newTerminalTab = handler }

func tabTestTerminal(t *testing.T) (*Terminal, *terminalTabs, *bytes.Buffer) {
	t.Helper()
	d, err := web.New(api.WindowOptions{Cols: 40, Rows: 10, CellWidth: 4, CellHeight: 8, Border: 1}, web.Options{Port: 0})
	if err != nil {
		t.Fatal(err)
	}
	w, err := widgets.Wrap(&tabButtonTestHost{Driver: d})
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
	id := spawnTestTab(t, root, api.TabSpawnRequest{Command: []string{"/nonexistent/st-go-tab-test"}, Directory: t.TempDir(), Detached: false})
	tabWindow(t, m, func() {
		if m.bar != nil || len(m.entries) != 1 || root.Widget().Bounds() != bounds || m.active != 0 || !root.Widget().Visible() {
			t.Error("failed spawn initialized tabs")
		}
	})
	root.mu.Lock()
	defer root.mu.Unlock()
	if !strings.Contains(replies.String(), "tab-spawn "+id+" error ") {
		t.Fatal("failed exec was not rejected", replies.String())
	}
}

func TestNewTerminalButtonInitializesTabsAndOpensIndependentShells(t *testing.T) {
	root, m, replies := tabTestTerminal(t)
	button := widgets.Unwrap(root.host).(*tabButtonTestHost).newTerminalTab
	if button == nil {
		t.Fatal("native new-terminal button was not bound")
	}
	original, bounds := root.termCore, root.Widget().Bounds()
	t.Setenv("SHELL", "/nonexistent/st-go-shell-test")
	button()
	tabWindow(t, m, func() {
		if m.bar != nil || len(m.entries) != 1 || root.Widget().Bounds() != bounds {
			t.Error("failed shell launch changed the original terminal")
		}
	})
	directory := t.TempDir()
	shell, marker := filepath.Join(directory, "shell"), filepath.Join(directory, "started")
	if err := os.WriteFile(shell, []byte("#!/bin/sh\nprintf '%s|%s\\n' \"$PWD\" \"$ST_GO_TABS\" >> \"$ST_GO_TEST_SHELL_MARKER\"\nIFS= read -r line\n"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SHELL", shell)
	t.Setenv("ST_GO_TEST_SHELL_MARKER", marker)
	pids := make(map[int]bool)
	for count := 2; count <= 3; count++ {
		button()
		tabWindow(t, m, func() {
			if m.bar == nil || len(m.entries) != count || m.active != count-1 || root.termCore != original {
				t.Error("button did not append and select a tab while preserving the original PTY")
				return
			}
			child := m.entries[m.active].terminal
			if !child.Widget().Visible() || root.Widget().Visible() || child.session.child.Path != shell || pids[child.session.child.Process.Pid] {
				t.Error("new tab did not get an independent, focused shell")
			}
			pids[child.session.child.Process.Pid] = true
		})
	}
	// The window-owned button must still work after the original tab closes.
	root.closeSession()
	button()
	tabWindow(t, m, func() {
		if len(m.entries) != 3 || m.active != 2 || m.entries[m.active].terminal.closed() {
			t.Error("closing tab 1 disabled the native new-terminal button")
		}
	})
	root.mu.Lock()
	reply := replies.String()
	root.mu.Unlock()
	if reply != "" {
		t.Fatal("native button wrote a protocol reply into the original PTY", reply)
	}
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	want := strings.Repeat(cwd+"|1\n", 3)
	deadline := time.Now().Add(3 * time.Second)
	for {
		data, err := os.ReadFile(marker)
		if err == nil && string(data) == want {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("shells did not start with the normal cwd and tab environment", string(data), err)
		}
		time.Sleep(time.Millisecond)
	}
}

func TestTabsFocusSuccessfulLaunchAndKeepFixedWidthLabels(t *testing.T) {
	root, m, _ := tabTestTerminal(t)
	request := api.TabSpawnRequest{Command: []string{"/bin/bash", "-c", "IFS= read -r key"}, Directory: t.TempDir(), Title: "document"}
	spawnTestTab(t, root, request)
	tabWindow(t, m, func() {
		if m.active != 1 || root.Widget().Visible() || !m.entries[1].widget.Visible() {
			t.Error("successful launch did not select its new PTY")
		}
		m.resize(500, 100)
		width := terminalTabWidthCells * m.renderer.cw
		if len(m.hits) != 2 || m.hits[0].right != width || m.hits[1].left != width || m.hits[1].right != 2*width {
			t.Error("tab labels stretched to fill the window", m.hits)
		}
	})
	spawnTestTab(t, root, request)
	tabWindow(t, m, func() {
		m.resize(500, 100)
		width := terminalTabWidthCells * m.renderer.cw
		if m.active != 2 || len(m.hits) != 3 {
			t.Error("new tab not appended and selected", m.active, m.hits)
			return
		}
		for i, hit := range m.hits {
			if hit.left != i*width || hit.right != (i+1)*width {
				t.Error("appending a tab changed previous tab widths", m.hits)
			}
		}
		m.resize(50, 50)
		if len(m.hits) != 1 || m.hits[0].index != m.active || m.hits[0].right > 50 {
			t.Error("narrow bar lost or overflowed the active tab", m.hits)
		}
		m.resize(500, 100)
		m.click(1, 499)
		if m.active != 2 {
			t.Error("unused bar space selected a nonexistent tab")
		}
		m.click(4, 499)
		if m.active != 1 {
			t.Error("wheel navigation ignored unused bar space")
		}
	})
}

func TestTabsShareBottomBandWithPassiveSystemBar(t *testing.T) {
	root, m, _ := tabTestTerminal(t)
	var passive api.Widget
	tabWindow(t, m, func() {
		bounds := m.control.WindowBounds()
		var err error
		passive, err = m.container.NewWidget(api.WidgetOptions{Kind: api.BarWidget, Bounds: image.Rect(bounds.Dx()-60, bounds.Dy()-24, bounds.Dx(), bounds.Dy()), Passive: true})
		if err != nil {
			t.Error(err)
		}
	})
	if passive == nil {
		t.Fatal("passive bar creation failed")
	}
	spawnTestTab(t, root, api.TabSpawnRequest{Command: []string{"/bin/bash", "-c", "IFS= read -r key"}, Directory: t.TempDir()})
	check := func() {
		t.Helper()
		if m.bar.Bounds().Overlaps(passive.Bounds()) || m.bar.Bounds().Max.X != passive.Bounds().Min.X || m.bar.Bounds().Min.Y != passive.Bounds().Min.Y {
			t.Error("tabs did not share the system bar's band", m.bar.Bounds(), passive.Bounds())
		}
		if m.entries[m.active].widget.Bounds().Max.Y != m.bar.Bounds().Min.Y {
			t.Error("terminal content overlaps bottom chrome")
		}
	}
	tabWindow(t, m, check)
	tabWindow(t, m, func() { b := passive.Bounds(); b.Min.X -= 20; passive.SetBounds(b) })
	tabWindow(t, m, check) // the passive bar change must relayout in the event domain.
	tabWindow(t, m, func() { passive.SetVisible(false) })
	tabWindow(t, m, func() {
		if m.bar.Bounds().Max.X != m.control.WindowBounds().Dx() || m.bar.Bounds().Dy() != m.renderer.ch+4 {
			t.Error("hidden system bar still reserved tab space", m.bar.Bounds())
		}
	})
}

func TestShrunkTabKeepsAttachedServiceAndRestoresNumberedIcon(t *testing.T) {
	root, m, _ := tabTestTerminal(t)
	marker := filepath.Join(t.TempDir(), "service-output")
	spawnTestTab(t, root, api.TabSpawnRequest{
		Command:   []string{"/bin/bash", "-c", `while IFS= read -r line; do printf 'service:%s\n' "$line"; printf '%s\n' "$line" >> "$1"; done`, "service", marker},
		Directory: t.TempDir(), Title: "jupyterlab",
	})
	var child *Terminal
	var pid int
	tabWindow(t, m, func() {
		child = m.entries[1].terminal
		pid = child.session.child.Process.Pid
		m.resize(500, 100)
		hit := m.hits[1]
		m.click(1, hit.shrinkLeft+1)
		if !m.entries[1].compact || m.active != 0 || child.Widget().Visible() || child.closed() {
			t.Error("shrinking did not background the existing service tab")
		}
		if m.hits[1].right-m.hits[1].left != 3*m.renderer.cw || m.hits[1].shrinkLeft != -1 {
			t.Error("shrunk tab is not a compact numbered icon", m.hits)
		}
	})
	child.mu.Lock()
	child.termCore.WriteToTTY([]byte("still-running\n"), false)
	child.mu.Unlock()
	deadline := time.Now().Add(3 * time.Second)
	for {
		data, _ := os.ReadFile(marker)
		child.mu.Lock()
		var screen strings.Builder
		for y := 0; y < child.rows; y++ {
			screen.WriteString(child.termCore.LineText(y))
		}
		child.mu.Unlock()
		if string(data) == "still-running\n" && strings.Contains(screen.String(), "service:still-running") {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("shrunk service stopped running or parsing PTY output", string(data), screen.String())
		}
		time.Sleep(time.Millisecond)
	}
	child.writeTerminalOutput(child.termCore, []byte("\x1b]0;updated service\a"))
	tabWindow(t, m, func() {
		if !m.entries[1].compact || m.entries[1].title != "updated service" {
			t.Error("title update lost compact state")
		}
		m.selectTab(1)
		m.resize(30, 50)
		if len(m.hits) != 1 || m.hits[0].index != 1 || !m.entries[1].compact {
			t.Error("narrow layout or keyboard selection lost the compact tab", m.hits)
		}
		m.resize(500, 100)
		m.click(1, m.hits[1].left+1)
		if m.entries[1].compact || m.active != 1 || child.session.child.Process.Pid != pid || child.closed() || !child.Widget().Visible() {
			t.Error("icon restore replaced or failed to activate the attached PTY")
		}
		m.shrinkTab(1)
	})
	root.closeSession()
	tabWindow(t, m, func() {
		if len(m.entries) != 1 || !m.entries[0].compact || m.hits[0].index != 0 || child.closed() {
			t.Error("closing an earlier tab lost the service icon/numbering")
		}
		m.click(1, m.hits[0].left+1)
		m.shrinkTab(0)
		if !m.entries[0].compact || !child.Widget().Visible() {
			t.Error("shrinking the last tab left no accessible active terminal")
		}
	})
}

func TestNewTerminalButtonKeepsTabsUsableBesideFullWidthSystemBar(t *testing.T) {
	root, m, _ := tabTestTerminal(t)
	t.Setenv("SHELL", "/bin/sh")
	var passive api.Widget
	tabWindow(t, m, func() {
		bounds := m.control.WindowBounds()
		var err error
		passive, err = m.container.NewWidget(api.WidgetOptions{Kind: api.BarWidget, Bounds: image.Rect(0, bounds.Dy()-24, bounds.Dx(), bounds.Dy()), Passive: true})
		if err != nil {
			t.Error(err)
		}
	})
	widgets.Unwrap(root.host).(*tabButtonTestHost).newTerminalTab()
	tabWindow(t, m, func() {
		if m.bar == nil || len(m.entries) != 2 || m.active != 1 {
			t.Error("full-width system bar prevented a new terminal")
			return
		}
		if m.bar.Bounds().Max.Y != passive.Bounds().Min.Y || m.bar.Bounds().Dx() != passive.Bounds().Dx() || m.entries[1].widget.Bounds().Max.Y != m.bar.Bounds().Min.Y || len(m.hits) == 0 {
			t.Error("narrow layout did not put usable tab controls above the system bar")
		}
	})
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
	pixel := widgets.Unwrap(root.host).(*tabButtonTestHost).firstScene.color.Load()
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
