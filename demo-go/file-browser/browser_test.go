package main

import (
	"archive/zip"
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"st-go/ptyutil"
)

// The browser binary is built once in TestMain so each PTY test can exec it.
var browserBin string

func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "fbgo-bin")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	browserBin = filepath.Join(dir, "file-browser")
	// The browser uses cgo + the static ALSA library, so it must be linked
	// with the same extldflags as the Makefile's file-browser target.
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	ldflags := "-linkmode external -extldflags \"-static " +
		filepath.Join(root, "third_party/alsa/snd.o") +
		" -L" + filepath.Join(root, "third_party/alsa/lib") +
		" -l:libasound.a -ldl -lpthread -lm\""
	build := exec.Command("go", "build", "-o", browserBin, "-ldflags", ldflags, "./demo-go/file-browser")
	build.Dir = root
	out, err := build.CombinedOutput()
	if err != nil {
		fmt.Fprintf(os.Stderr, "build browser: %v\n%s", err, out)
		os.RemoveAll(dir)
		os.Exit(1)
	}
	code := m.Run()
	os.RemoveAll(dir)
	os.Exit(code)
}

// fbTest drives a live browser instance through a PTY.
type fbTest struct {
	master *os.File
	cmd    *exec.Cmd
	mu     sync.Mutex
	all    []byte
}

func (f *fbTest) output() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return string(f.all)
}

func (f *fbTest) send(s string) {
	f.master.Write([]byte(s))
}

func (f *fbTest) sendBytes(b []byte) {
	f.master.Write(b)
}

// waitFor polls cond until it is true or the deadline passes.
func waitFor(t *testing.T, f *fbTest, cond func() bool, what string) {
	t.Helper()
	deadline := time.Now().Add(4 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	all := f.output()
	tail := all
	if len(tail) > 400 {
		tail = tail[len(tail)-400:]
	}
	t.Fatalf("timeout waiting for %s; output tail: %q", what, tail)
}

func waitContains(t *testing.T, f *fbTest, sub string, what string) {
	t.Helper()
	waitFor(t, f, func() bool { return strings.Contains(f.output(), sub) }, what)
}

// waitContainsAny passes when any of the substrings appears.
func waitContainsAny(t *testing.T, f *fbTest, subs []string, what string) {
	t.Helper()
	waitFor(t, f, func() bool {
		out := f.output()
		for _, s := range subs {
			if strings.Contains(out, s) {
				return true
			}
		}
		return false
	}, what)
}

func waitFileContent(t *testing.T, path, want string) {
	t.Helper()
	deadline := time.Now().Add(4 * time.Second)
	for time.Now().Before(deadline) {
		data, err := os.ReadFile(path)
		if err == nil && string(data) == want {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("%s did not contain %q", path, want)
}

func startBrowser(t *testing.T, env []string, args ...string) *fbTest {
	t.Helper()
	master, slave, err := ptyutil.Open()
	if err != nil {
		t.Fatal(err)
	}
	if err := ptyutil.SetWinSize(master, 24, 80); err != nil {
		t.Fatal(err)
	}
	cmdline := append([]string{browserBin}, args...)
	cmd, err := ptyutil.Start(slave, cmdline, env)
	if err != nil {
		master.Close()
		t.Fatal(err)
	}
	f := &fbTest{master: master, cmd: cmd}
	go func() {
		buf := make([]byte, 8192)
		for {
			n, err := master.Read(buf)
			if n > 0 {
				f.mu.Lock()
				f.all = append(f.all, buf[:n]...)
				f.mu.Unlock()
			}
			if err != nil {
				return
			}
		}
	}()
	t.Cleanup(func() {
		master.Write([]byte("q"))
		done := make(chan error, 1)
		go func() { done <- cmd.Wait() }()
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			cmd.Process.Kill()
			<-done
		}
		master.Close()
	})
	time.Sleep(400 * time.Millisecond)
	return f
}

// lastHeaderPath extracts the most recent header directory path from output.
func lastHeaderPath(s string) string {
	prefix := "st-go files  "
	idx := strings.LastIndex(s, prefix)
	if idx < 0 {
		return ""
	}
	rest := s[idx+len(prefix):]
	end := strings.Index(rest, "\x1b[")
	if end < 0 {
		end = len(rest)
	}
	return rest[:end]
}

// testEnv returns the browser environment with extra vars appended.
func testEnv(extra ...string) []string {
	return append(os.Environ(), extra...)
}

func writeFiles(t *testing.T, dir string, names ...string) {
	t.Helper()
	for _, n := range names {
		if err := os.WriteFile(filepath.Join(dir, n), []byte("content\n"), 0644); err != nil {
			t.Fatal(err)
		}
	}
}

// --- startup / layout ---

func TestStartupLayout(t *testing.T) {
	dir := t.TempDir()
	writeFiles(t, dir, "a.txt", "b.md", "pic.png")
	f := startBrowser(t, testEnv(), dir)
	all := f.output()
	for _, want := range []string{
		"\x1b[?1049h\x1b[?25l\x1b[?1000h\x1b[?1006h",
		"window remember file-browser-",
		"st-go files",
		"items",
		"a.txt",
		"b.md",
		"pic.png",
		"Ready",
	} {
		if !strings.Contains(all, want) {
			t.Fatalf("startup output missing %q", want)
		}
	}
	if n := strings.Count(all, "\x1bPclear\x1b\\"); n != 0 {
		t.Fatalf("initial render issued %d atlas-destroying clears", n)
	}
	// graceful quit runs the cleanup: forget geometry and restore the terminal.
	f.send("q")
	waitContains(t, f, "window forget file-browser-", "cleanup forget geometry")
	waitContains(t, f, "\x1b[?1006l\x1b[?1000l\x1b[?25h\x1b[0m\x1b[?1049l", "cleanup terminal restore")
}

func TestIconsEnvOverride(t *testing.T) {
	dir := t.TempDir()
	writeFiles(t, dir, "a.txt")
	f := startBrowser(t, testEnv(
		"ST_FILE_BROWSER_ICON_PARENT=U",
		"ST_FILE_BROWSER_ICON_TEXT=T"), dir)
	waitContains(t, f, "U  ..", "parent icon")
	waitContains(t, f, "T  a.txt", "text icon")
}

func TestSelectionMovesIncremental(t *testing.T) {
	dir := t.TempDir()
	writeFiles(t, dir, "a.txt", "b.txt", "c.txt")
	f := startBrowser(t, testEnv(), dir)
	clears := strings.Count(f.output(), "\x1b[2J")
	f.send("\x1b[B")
	time.Sleep(150 * time.Millisecond)
	f.send("\x1b[B")
	time.Sleep(150 * time.Millisecond)
	if n := strings.Count(f.output(), "\x1b[2J"); n > clears+1 {
		t.Fatalf("selection moves caused extra full clears: %d -> %d", clears, n)
	}
	waitContains(t, f, "c.txt", "selection moved")
}

func TestHomeEndPageKeys(t *testing.T) {
	dir := t.TempDir()
	for i := 0; i < 20; i++ {
		writeFiles(t, dir, fmt.Sprintf("f%02d.txt", i))
	}
	f := startBrowser(t, testEnv(), dir)
	f.send("\x1b[H") // Home -> first
	time.Sleep(120 * time.Millisecond)
	waitContains(t, f, "FILES  1-15 / 21", "home selection")
	f.send("\x1b[F") // End -> last
	time.Sleep(120 * time.Millisecond)
	waitContains(t, f, "FILES  7-21 / 21", "end selection")
	f.send("\x1b[5~") // PageUp
	time.Sleep(120 * time.Millisecond)
	waitContains(t, f, "FILES  1-15 / 21", "page up")
	f.send("\x1b[6~") // PageDown
	time.Sleep(120 * time.Millisecond)
	waitContains(t, f, "FILES  7-21 / 21", "page down")
}

// --- openers / directories ---

func TestEnterOpensFileWithOpenCmd(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "openme.txt")
	if err := os.WriteFile(file, []byte("x\n"), 0644); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(dir, ".opened")
	opener := `printf '%s|%s|%s' "$NAME" "$FILE" "$BROWSER_DIR" > "$MARKER"`
	f := startBrowser(t, testEnv("MARKER="+marker), "--open", opener, dir)
	f.send("\x1b[B") // select openme.txt (index 1)
	time.Sleep(120 * time.Millisecond)
	f.send("\r") // Enter opens
	waitFileContent(t, marker, "openme.txt|"+file+"|"+dir)
}

func TestEnterEntersDirectory(t *testing.T) {
	dir := t.TempDir()
	sub := filepath.Join(dir, "sub")
	if err := os.MkdirAll(sub, 0755); err != nil {
		t.Fatal(err)
	}
	writeFiles(t, sub, "inner.txt")
	f := startBrowser(t, testEnv(), dir)
	waitContains(t, f, "sub", "sub dir listed")
	f.send("\x1b[B") // select sub
	time.Sleep(120 * time.Millisecond)
	f.send("\r") // enter
	waitContains(t, f, "inner.txt", "entered subdir")
	waitContains(t, f, "Browsing", "browsing status")
}

func TestBackspaceGoesParent(t *testing.T) {
	dir := t.TempDir()
	sub := filepath.Join(dir, "sub")
	if err := os.MkdirAll(sub, 0755); err != nil {
		t.Fatal(err)
	}
	writeFiles(t, sub, "inner.txt")
	f := startBrowser(t, testEnv(), dir)
	f.send("\x1b[B")
	time.Sleep(120 * time.Millisecond)
	f.send("\r")
	waitFor(t, f, func() bool { return lastHeaderPath(f.output()) == sub }, "header shows subdir")
	f.send("\x7f") // Backspace -> parent
	waitFor(t, f, func() bool { return lastHeaderPath(f.output()) == dir }, "header back to parent")
}

func TestDefaultTextOpenerUsesRunningSt(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "openme.md")
	writeFiles(t, dir, "openme.md")
	tools := t.TempDir()
	marker := filepath.Join(tools, "opened")
	fakeSt := filepath.Join(tools, "st")
	script := "#!/bin/sh\nprintf '%s|%s' \"$1\" \"$2\" > \"$MARKER\"\n"
	if err := os.WriteFile(fakeSt, []byte(script), 0755); err != nil {
		t.Fatal(err)
	}
	env := testEnv("ST_GO_EXECUTABLE=" + fakeSt, "MARKER=" + marker)
	env = envWithout(env, "ST_FILE_BROWSER_OPEN")
	f := startBrowser(t, env, dir)
	f.send("\x1b[B") // select openme.md
	time.Sleep(120 * time.Millisecond)
	f.send("o") // open selected
	waitFileContent(t, marker, "vim|"+file)
}

func envWithout(env []string, drop string) []string {
	var out []string
	for _, kv := range env {
		if strings.HasPrefix(kv, drop+"=") {
			continue
		}
		out = append(out, kv)
	}
	return out
}

// --- path prompt ---

func TestPathPromptRelativeEditing(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "sub"), 0755); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(dir, "sub", "file.txt")
	writeFiles(t, filepath.Join(dir, "sub"), "file.txt")
	marker := filepath.Join(dir, ".path-opened")
	opener := `printf '%s' "$FILE" > "$MARKER"`
	f := startBrowser(t, testEnv("MARKER="+marker), "--open", opener, dir)
	// Enter prompt, remove the initial '/', build "ub/file.tx", then use
	// Ctrl+A/Ctrl+E insertion to produce "sub/file.txt".
	f.send("/\x7fub/file.tx\x01s\x05t\r")
	waitFileContent(t, marker, file)
}

func TestPathPromptWildcardPopup(t *testing.T) {
	dir := t.TempDir()
	first := filepath.Join(dir, "alpha-one.txt")
	writeFiles(t, dir, "alpha-one.txt", "alpha-two.txt")
	marker := filepath.Join(dir, ".path-opened")
	opener := `printf '%s' "$FILE" > "$MARKER"`
	f := startBrowser(t, testEnv("MARKER="+marker), "--open", opener, dir)
	// Remove the initial slash, enter a wildcard, select the first match.
	f.send("/\x7falpha-*\x1b[B\r")
	waitFileContent(t, marker, first)
}

func TestPathPromptAbort(t *testing.T) {
	dir := t.TempDir()
	writeFiles(t, dir, "a.txt")
	marker := filepath.Join(dir, ".path-opened")
	opener := `printf '%s' "$FILE" > "$MARKER"`
	f := startBrowser(t, testEnv("MARKER="+marker), "--open", opener, dir)
	for _, seq := range []string{
		"/abc\x1b",      // ESC
		"/abc\x03",      // Ctrl+C byte
		"/abc\x1b[<0;10;8M", // mouse report
	} {
		before := len(f.output())
		f.send(seq)
		time.Sleep(120 * time.Millisecond)
		f.send("r")
		waitFor(t, f, func() bool {
			return strings.Contains(f.output()[before:], "Refreshed")
		}, "return to normal mode after abort")
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("aborted prompt unexpectedly opened a file: %v", err)
	}
}

// --- command prompt ---

func TestCommandPromptShellCmd(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "cmd.txt")
	writeFiles(t, dir, "cmd.txt")
	marker := filepath.Join(dir, ".cmd-run")
	f := startBrowser(t, testEnv(), dir)
	f.send("\x1b[B") // select cmd.txt
	time.Sleep(120 * time.Millisecond)
	f.send(":printf '%s|%s' \"$D\" \"$F\" > " + marker + "\r")
	waitFileContent(t, marker, dir+"|"+file)
}

func TestCommandPromptAbort(t *testing.T) {
	dir := t.TempDir()
	writeFiles(t, dir, "a.txt")
	f := startBrowser(t, testEnv(), dir)
	before := len(f.output())
	f.send(":oops\x1b")
	time.Sleep(150 * time.Millisecond)
	f.send("r")
	waitFor(t, f, func() bool {
		return strings.Contains(f.output()[before:], "Refreshed")
	}, "return to normal mode after command abort")
}

// --- rename :s/ ---

func TestRenameSubstitution(t *testing.T) {
	dir := t.TempDir()
	writeFiles(t, dir, "report.txt", "notes.txt", "other.log")
	f := startBrowser(t, testEnv(), dir)
	f.send(":s/txt/md/\r")
	deadline := time.Now().Add(4 * time.Second)
	for time.Now().Before(deadline) {
		_, e1 := os.Stat(filepath.Join(dir, "report.md"))
		_, e2 := os.Stat(filepath.Join(dir, "notes.md"))
		_, e3 := os.Stat(filepath.Join(dir, "other.log"))
		_, e4 := os.Stat(filepath.Join(dir, "report.txt"))
		if e1 == nil && e2 == nil && e3 == nil && os.IsNotExist(e4) {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal(":s/txt/md/ did not rename the matching entries")
}

func TestRenameGlobal(t *testing.T) {
	dir := t.TempDir()
	writeFiles(t, dir, "a-b-c.txt")
	f := startBrowser(t, testEnv(), dir)
	f.send(":s/-/X/g\r")
	deadline := time.Now().Add(4 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(filepath.Join(dir, "aXbXc.txt")); err == nil {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal(":s/-/X/g did not replace every occurrence")
}

func TestRenameFirstOccurrenceOnly(t *testing.T) {
	dir := t.TempDir()
	writeFiles(t, dir, "a-b-c.txt")
	f := startBrowser(t, testEnv(), dir)
	f.send(":s/-/X/\r")
	deadline := time.Now().Add(4 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(filepath.Join(dir, "aXb-c.txt")); err == nil {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal(":s/-/X/ (no g) should replace only the first occurrence")
}

func TestRenamePreviewHighlight(t *testing.T) {
	dir := t.TempDir()
	writeFiles(t, dir, "report.txt", "notes.txt", "other.log")
	f := startBrowser(t, testEnv(), dir)
	// Type the pattern but do NOT press Enter: affected rows must be painted.
	f.send(":s/txt/md/")
	waitContains(t, f, "\x1b[38;5;16m\x1b[48;5;220m", "rename highlight")
}

func TestRenameCollisionSkip(t *testing.T) {
	dir := t.TempDir()
	writeFiles(t, dir, "foo.txt", "foo.md")
	f := startBrowser(t, testEnv(), dir)
	f.send(":s/txt/md/\r")
	deadline := time.Now().Add(4 * time.Second)
	for time.Now().Before(deadline) {
		if strings.Contains(f.output(), "Renamed 0") {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if _, err := os.Stat(filepath.Join(dir, "foo.txt")); err != nil {
		t.Fatalf("collision target was overwritten: %v", err)
	}
}

func TestRenameInvalidPattern(t *testing.T) {
	dir := t.TempDir()
	writeFiles(t, dir, "a.txt")
	f := startBrowser(t, testEnv(), dir)
	f.send(":s/bad\r")
	waitContains(t, f, "Invalid substi", "invalid pattern status")
}

func TestRenameNoMatch(t *testing.T) {
	dir := t.TempDir()
	writeFiles(t, dir, "a.txt")
	f := startBrowser(t, testEnv(), dir)
	f.send(":s/nomatch/xx/\r")
	waitContains(t, f, "No filenames m", "no match status")
}

// --- manual / help ---

func TestHelpManualRendersAndNavigates(t *testing.T) {
	dir := t.TempDir()
	writeFiles(t, dir, "a.txt")
	f := startBrowser(t, testEnv(), dir)
	f.send(":help\r")
	waitContains(t, f, "USER MANUAL  1/", "manual page 1")
	f.send("]")
	waitContains(t, f, "USER MANUAL  2/", "manual page 2")
	f.send("[")
	waitContains(t, f, "USER MANUAL  1/", "manual page 1 back")
	f.send("q")
	waitContains(t, f, "Manual closed", "manual closed")
}

func TestHelpManualWheel(t *testing.T) {
	dir := t.TempDir()
	writeFiles(t, dir, "a.txt")
	f := startBrowser(t, testEnv(), dir)
	f.send(":help\r")
	waitContains(t, f, "USER MANUAL  1/", "manual page 1")
	f.send("\x1b[<65;10;8M") // wheel down -> next page
	waitContains(t, f, "USER MANUAL  2/", "manual page 2")
	f.send("\x1b[<64;10;8M") // wheel up -> previous page
	waitContains(t, f, "USER MANUAL  1/", "manual page 1")
}

// --- PDF pages ---

func TestPDFPageChangeKeys(t *testing.T) {
	dir := t.TempDir()
	writeFiles(t, dir, "doc.pdf")
	f := startBrowser(t, testEnv(), dir)
	waitContains(t, f, "doc.pdf", "pdf listed")
	f.send("\x1b[B") // select doc.pdf
	time.Sleep(120 * time.Millisecond)
	waitContains(t, f, "PDF document, page 1", "pdf page 1")
	f.send("]")
	waitContains(t, f, "fit-contain page 2", "pdf page 2 dcs")
	waitContains(t, f, "PDF document, page 2", "pdf page 2 info")
	f.send("[")
	waitContains(t, f, "fit-contain page 1", "pdf page 1 dcs")
}

func TestPDFWheelOverPreview(t *testing.T) {
	dir := t.TempDir()
	writeFiles(t, dir, "doc.pdf")
	f := startBrowser(t, testEnv(), dir)
	f.send("\x1b[B")
	time.Sleep(120 * time.Millisecond)
	waitContains(t, f, "PDF document, page 1", "pdf page 1")
	// wheel down (button 1) at x < LIST_X flips to page 2
	f.send("\x1b[<65;20;9M")
	waitContains(t, f, "fit-contain page 2", "pdf wheel page 2")
}

// --- hidden / refresh ---

func TestToggleHidden(t *testing.T) {
	dir := t.TempDir()
	writeFiles(t, dir, "a.txt", ".secret")
	f := startBrowser(t, testEnv(), dir)
	waitContains(t, f, "a.txt", "file listed")
	if strings.Contains(f.output(), ".secret") {
		t.Fatal("hidden file shown before toggle")
	}
	f.send(".")
	waitContains(t, f, ".secret", "hidden file after toggle")
	// toggle_hidden ends in refresh_list, so the status is 'Refreshed'
	// (matching the shell browser).
	if !strings.Contains(f.output(), ".secret") || !strings.Contains(f.output(), "a.txt") {
		t.Fatal("hidden toggle did not list both files")
	}
}

func TestRefreshList(t *testing.T) {
	dir := t.TempDir()
	writeFiles(t, dir, "a.txt")
	f := startBrowser(t, testEnv(), dir)
	waitContains(t, f, "a.txt", "file listed")
	os.Remove(filepath.Join(dir, "a.txt"))
	f.send("r")
	waitContains(t, f, "Refreshed", "refresh status")
}

// --- mouse ---

func TestMouseClickSelects(t *testing.T) {
	dir := t.TempDir()
	writeFiles(t, dir, "a.txt", "b.txt")
	f := startBrowser(t, testEnv(), dir)
	// click row 10 (second file after parent) at column 60 -> selects b.txt
	f.send("\x1b[<0;60;10M")
	waitContains(t, f, "TEXT  b.txt", "click selected b.txt")
}

func TestMouseWheelScrollsList(t *testing.T) {
	dir := t.TempDir()
	writeFiles(t, dir, "a.txt", "b.txt")
	f := startBrowser(t, testEnv(), dir)
	// wheel down (button 1) over the list scrolls selection by 3 -> clamps to b.txt
	f.send("\x1b[<65;60;9M")
	waitContains(t, f, "TEXT  b.txt", "wheel scroll moved selection")
}

func TestMouseMiddleClickGoesParent(t *testing.T) {
	dir := t.TempDir()
	sub := filepath.Join(dir, "sub")
	if err := os.MkdirAll(sub, 0755); err != nil {
		t.Fatal(err)
	}
	writeFiles(t, sub, "inner.txt")
	f := startBrowser(t, testEnv(), dir)
	f.send("\x1b[B")
	time.Sleep(120 * time.Millisecond)
	f.send("\r")
	waitFor(t, f, func() bool { return lastHeaderPath(f.output()) == sub }, "in subdir")
	f.send("\x1b[<2;60;9M") // middle click
	waitFor(t, f, func() bool { return lastHeaderPath(f.output()) == dir }, "middle click parent")
}

func TestMouseDoubleClickOpensAndMinimizes(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "openme.txt")
	writeFiles(t, dir, "openme.txt")
	marker := filepath.Join(dir, ".opened")
	opener := `printf '%s' "$FILE" > "$MARKER"`
	f := startBrowser(t, testEnv("MARKER="+marker), "--open", opener, dir)
	click := "\x1b[<0;60;9M" // first real file row
	f.send(click)
	time.Sleep(80 * time.Millisecond)
	f.send(click)
	waitFileContent(t, marker, file)
	waitContains(t, f, "place bottom-left 0px 0px 24% 8% restore file-browser-", "minimize geometry")
}

// --- preview ---

func TestTextPreviewEmitsDCS(t *testing.T) {
	dir := t.TempDir()
	writeFiles(t, dir, "note.txt")
	f := startBrowser(t, testEnv(), dir)
	f.send("\x1b[B")
	time.Sleep(120 * time.Millisecond)
	waitContains(t, f, "TEXT  note.txt", "text preview title")
	waitContains(t, f, "open '", "text preview dcs")
	waitContains(t, f, "rect 2 3", "text rect placement")
}

func TestBinaryPreviewMessage(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "blob.bin"), []byte{0x00, 0x01, 0x02, 0xff}, 0644); err != nil {
		t.Fatal(err)
	}
	f := startBrowser(t, testEnv(), dir)
	f.send("\x1b[B")
	time.Sleep(120 * time.Millisecond)
	waitContains(t, f, "BINARY FILE", "binary preview message")
}

func TestDirectoryPreviewMessage(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "sub"), 0755); err != nil {
		t.Fatal(err)
	}
	f := startBrowser(t, testEnv(), dir)
	f.send("\x1b[B")
	time.Sleep(120 * time.Millisecond)
	waitContains(t, f, "Double-click or press Enter to browse.", "directory preview message")
}

// --- resize ---

func TestResizeRedraws(t *testing.T) {
	dir := t.TempDir()
	writeFiles(t, dir, "a.txt")
	f := startBrowser(t, testEnv(), dir)
	clears := strings.Count(f.output(), "\x1b[2J")
	if err := ptyutil.SetWinSize(f.master, 30, 100); err != nil {
		t.Fatal(err)
	}
	waitFor(t, f, func() bool {
		return strings.Count(f.output(), "\x1b[2J") > clears
	}, "redraw after resize")
	waitContains(t, f, "Resized", "resized status")
}

// --- mp3 (in-browser open) ---

// writeTestMp3 writes an mp3 with an ID3v2 tag; returns false (callers skip)
// when ffmpeg is unavailable.
func writeTestMp3(t *testing.T, path, title string) bool {
	t.Helper()
	src, ok := makeMp3Bytes(t)
	if !ok {
		return false
	}
	tag := makeID3v2(title, "Test Artist", "Test Album")
	if err := os.WriteFile(path, append(tag, src...), 0644); err != nil {
		t.Fatal(err)
	}
	return true
}

func TestMp3PreviewShowsMetadata(t *testing.T) {
	dir := t.TempDir()
	mp3 := filepath.Join(dir, "song.mp3")
	if !writeTestMp3(t, mp3, "My Song") {
		t.Skip("ffmpeg not available")
	}
	f := startBrowser(t, testEnv(), dir)
	waitContains(t, f, "song.mp3", "mp3 listed")
	f.send("\x1b[B") // select song.mp3
	time.Sleep(150 * time.Millisecond)
	waitContains(t, f, "MP3 AUDIO", "mp3 preview card")
	waitContains(t, f, "My Song", "mp3 title shown")
	waitContains(t, f, "Test Artist", "mp3 artist shown")
}

func TestMp3OpensInBrowser(t *testing.T) {
	dir := t.TempDir()
	mp3 := filepath.Join(dir, "song.mp3")
	if !writeTestMp3(t, mp3, "My Song") {
		t.Skip("ffmpeg not available")
	}
	marker := filepath.Join(dir, ".opened")
	opener := `printf '%s' "$FILE" > "$MARKER"`
	f := startBrowser(t, testEnv("MARKER="+marker), "--open", opener, dir)
	f.send("\x1b[B") // select song.mp3
	time.Sleep(150 * time.Millisecond)
	f.send("o") // open in the browser itself
	waitContainsAny(t, f, []string{"MP3 viewer", "Playing", "No audio"}, "in-browser mp3 viewer opened")
	time.Sleep(200 * time.Millisecond)
	if _, err := os.Stat(marker); err == nil {
		t.Fatal("mp3 was sent to the external opener instead of opened in-browser")
	}
	// Nothing may be written to stderr onto the terminal (a missing audio
	// device must degrade silently, not corrupt the UI). ALSA's dmix config
	// parse (snd1_pcm_direct_parse_open_conf / ipc_gid) must also not leak.
	if strings.Contains(f.output(), "alsa:") ||
		strings.Contains(f.output(), "panic:") ||
		strings.Contains(f.output(), "snd1_") ||
		strings.Contains(f.output(), "ipc_gid") {
		t.Fatalf("opening mp3 leaked an error onto the terminal: %q", f.output())
	}
	f.send("q")
	waitContains(t, f, "Closed mp3", "mp3 viewer closed")
}

func TestMp3InArchiveOpensInBrowser(t *testing.T) {
	dir := t.TempDir()
	src, ok := makeMp3Bytes(t)
	if !ok {
		t.Skip("ffmpeg not available")
	}
	zpath := filepath.Join(dir, "music.zip")
	makeTestZip(t, zpath, map[string]string{"track.mp3": string(src)})
	f := startBrowser(t, testEnv(), dir)
	waitContains(t, f, "music.zip", "zip listed")
	f.send("\x1b[B") // music.zip
	time.Sleep(120 * time.Millisecond)
	f.send("\r") // enter archive
	waitContains(t, f, "track.mp3", "mp3 listed inside archive")
	f.send("\x1b[B") // track.mp3
	time.Sleep(120 * time.Millisecond)
	f.send("o") // open: materializes the single entry, then in-browser
	waitContainsAny(t, f, []string{"MP3 viewer", "Playing", "No audio"}, "in-browser mp3 viewer inside archive")
	f.send("q")
	waitContains(t, f, "Closed mp3", "mp3 viewer closed")
}

// --- animated webp (terminal plays it via the open DSL `anim` option) ---

func TestAnimatedWebpPreviewEmitsAnim(t *testing.T) {
	dir := t.TempDir()
	data, ok := makeAnimatedWebpBytes(t)
	if !ok {
		t.Skip("ffmpeg not available")
	}
	if err := os.WriteFile(filepath.Join(dir, "anim.webp"), data, 0644); err != nil {
		t.Fatal(err)
	}
	f := startBrowser(t, testEnv(), dir)
	waitContains(t, f, "anim.webp", "webp listed")
	f.send("\x1b[B") // select anim.webp
	time.Sleep(250 * time.Millisecond)
	waitContains(t, f, "ANIMATED", "animated preview title")
	waitContains(t, f, "fit-contain anim", "preview sent the anim DSL option")
}

func TestAnimatedWebpHeaderDetect(t *testing.T) {
	// a static webp must NOT get the anim option
	dir := t.TempDir()
	static := filepath.Join(dir, "static.webp")
	if err := os.WriteFile(static, []byte("RIFF....WEBPVP8 \x00"), 0644); err != nil {
		t.Fatal(err)
	}
	f := startBrowser(t, testEnv(), dir)
	waitContains(t, f, "static.webp", "static webp listed")
	f.send("\x1b[B")
	time.Sleep(250 * time.Millisecond)
	if strings.Contains(f.output(), "fit-contain anim") {
		t.Fatal("static webp was sent with the anim option")
	}
	if strings.Contains(f.output(), "ANIMATED") {
		t.Fatal("static webp got the ANIMATED title")
	}
}

// --- CLI errors ---

func TestCLIUsage(t *testing.T) {
	out, err := exec.Command(browserBin, "-h").CombinedOutput()
	if err != nil {
		t.Fatalf("-h failed: %v", err)
	}
	if !strings.Contains(string(out), "Usage: file-browser") {
		t.Fatalf("-h output: %q", out)
	}
}

func TestCLIUnknownOption(t *testing.T) {
	cmd := exec.Command(browserBin, "--bogus")
	err := cmd.Run()
	if err == nil {
		t.Fatal("--bogus should fail")
	}
	if ee, ok := err.(*exec.ExitError); ok && ee.ExitCode() != 2 {
		t.Fatalf("exit code = %d, want 2", ee.ExitCode())
	}
}

func TestCLINoOpenArg(t *testing.T) {
	cmd := exec.Command(browserBin, "--open")
	err := cmd.Run()
	if err == nil {
		t.Fatal("--open without code should fail")
	}
	if ee, ok := err.(*exec.ExitError); ok && ee.ExitCode() != 2 {
		t.Fatalf("exit code = %d, want 2", ee.ExitCode())
	}
}

func TestCLIMissingDir(t *testing.T) {
	cmd := exec.Command(browserBin, "/nonexistent/path/xyz")
	err := cmd.Run()
	if err == nil {
		t.Fatal("missing dir should fail")
	}
	if ee, ok := err.(*exec.ExitError); ok && ee.ExitCode() != 1 {
		t.Fatalf("exit code = %d, want 1", ee.ExitCode())
	}
}

func TestCLIOpenEquals(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "x.txt")
	writeFiles(t, dir, "x.txt")
	marker := filepath.Join(dir, ".opened")
	opener := `printf '%s' "$FILE" > "$MARKER"`
	f := startBrowser(t, testEnv("MARKER="+marker), "--open="+opener, dir)
	f.send("\x1b[B")
	time.Sleep(120 * time.Millisecond)
	f.send("o")
	waitFileContent(t, marker, file)
}

func TestHiddenFlagCLI(t *testing.T) {
	dir := t.TempDir()
	writeFiles(t, dir, "a.txt", ".secret")
	f := startBrowser(t, testEnv(), "--hidden", dir)
	waitContains(t, f, ".secret", "hidden file listed with --hidden")
}

func TestSymlinkAndExecutableIcons(t *testing.T) {
	dir := t.TempDir()
	writeFiles(t, dir, "script.sh")
	if err := os.Symlink(filepath.Join(dir, "script.sh"), filepath.Join(dir, "alias")); err != nil {
		t.Fatal(err)
	}
	runme := filepath.Join(dir, "runme")
	if err := os.WriteFile(runme, []byte("#!/bin/sh\n"), 0755); err != nil {
		t.Fatal(err)
	}
	f := startBrowser(t, testEnv(
		"ST_FILE_BROWSER_ICON_SYMLINK=S",
		"ST_FILE_BROWSER_ICON_EXECUTABLE=E",
		"ST_FILE_BROWSER_ICON_CODE=C"), dir)
	waitContains(t, f, "S  alias", "symlink icon")
	waitContains(t, f, "E  runme", "executable icon")
	waitContains(t, f, "C  script.sh", "code icon")
}

// TestPromptEditingKeys exercises Left-arrow and Delete (CSI 3~) in the
// command prompt: typing s/txt/mdd/ then removing the extra 'd' with editing
// must still produce a valid :s/txt/md/ rename.
func TestPromptEditingKeys(t *testing.T) {
	dir := t.TempDir()
	writeFiles(t, dir, "abc.txt")
	f := startBrowser(t, testEnv(), dir)
	// ":s/txt/mdd/" then Left Left Left and Delete yields ":s/txt/md/"
	f.send(":s/txt/mdd/\x1b[D\x1b[D\x1b[D\x1b[3~\r")
	deadline := time.Now().Add(4 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(filepath.Join(dir, "abc.md")); err == nil {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("prompt editing keys did not produce the :s/txt/md/ rename")
}

func TestCLIHelpAndDoubleDash(t *testing.T) {
	// "--" separator: a directory after -- is still accepted.
	dir := t.TempDir()
	writeFiles(t, dir, "a.txt")
	f := startBrowser(t, testEnv(), "--", dir)
	waitContains(t, f, "a.txt", "directory after -- listed")
}

// makeTestZip writes a zip archive containing the given name->content map.
func makeTestZip(t *testing.T, path string, files map[string]string) {
	t.Helper()
	fh, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	zw := zip.NewWriter(fh)
	for name, content := range files {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write([]byte(content)); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := fh.Close(); err != nil {
		t.Fatal(err)
	}
}

// --- archive browsing ---

func TestArchiveZipBrowseAndBack(t *testing.T) {
	dir := t.TempDir()
	zipPath := filepath.Join(dir, "book.zip")
	makeTestZip(t, zipPath, map[string]string{"readme.txt": "hi\n", "main.go": "package main\n"})
	f := startBrowser(t, testEnv(), dir)
	waitContains(t, f, "book.zip", "zip listed")
	f.send("\x1b[B") // select book.zip
	time.Sleep(120 * time.Millisecond)
	f.send("\r") // Enter enters the archive
	waitContains(t, f, "readme.txt", "zip contents listed")
	waitContains(t, f, "main.go", "nested file listed")
	// Backspace returns to the containing directory and re-selects the archive.
	f.send("\x7f")
	waitFor(t, f, func() bool { return lastHeaderPath(f.output()) == dir }, "back to containing dir")
}

func TestArchiveCbz(t *testing.T) {
	dir := t.TempDir()
	cbzPath := filepath.Join(dir, "comic.cbz")
	makeTestZip(t, cbzPath, map[string]string{"page01.jpg": "x"})
	f := startBrowser(t, testEnv(), dir)
	waitContains(t, f, "comic.cbz", "cbz listed")
	f.send("\x1b[B")
	time.Sleep(120 * time.Millisecond)
	f.send("\r")
	waitContains(t, f, "page01.jpg", "cbz contents listed")
}

func TestArchiveCbrViaTool(t *testing.T) {
	if _, err := exec.LookPath("7z"); err != nil {
		if _, err := exec.LookPath("bsdtar"); err != nil {
			if _, err := exec.LookPath("unrar"); err != nil {
				t.Skip("no rar extraction tool (7z/bsdtar/unrar)")
			}
		}
	}
	dir := t.TempDir()
	// A zip-by-content container named .cbr exercises the rar/cbr branch, which
	// dispatches to 7z/bsdtar; those tools sniff the container signature.
	cbrPath := filepath.Join(dir, "comic.cbr")
	makeTestZip(t, cbrPath, map[string]string{"page01.jpg": "x"})
	f := startBrowser(t, testEnv(), dir)
	waitContains(t, f, "comic.cbr", "cbr listed")
	f.send("\x1b[B")
	time.Sleep(120 * time.Millisecond)
	f.send("\r")
	waitContains(t, f, "page01.jpg", "cbr contents listed")
}

func TestArchiveSubdirNavigation(t *testing.T) {
	dir := t.TempDir()
	zipPath := filepath.Join(dir, "tree.zip")
	makeTestZip(t, zipPath, map[string]string{"alpha/beta/gamma.txt": "x\n"})
	f := startBrowser(t, testEnv(), dir)
	f.send("\x1b[B") // tree.zip
	time.Sleep(120 * time.Millisecond)
	f.send("\r")
	waitContains(t, f, "alpha", "archive root lists alpha")
	f.send("\x1b[B") // alpha
	time.Sleep(120 * time.Millisecond)
	f.send("\r")
	waitContains(t, f, "beta", "alpha lists beta")
	f.send("\x1b[B") // beta
	time.Sleep(120 * time.Millisecond)
	f.send("\r")
	waitContains(t, f, "gamma.txt", "beta lists gamma.txt")
	f.send("\x7f")
	waitContains(t, f, "beta", "back to beta")
	f.send("\x7f")
	waitContains(t, f, "alpha", "back to alpha")
	f.send("\x7f")
	waitFor(t, f, func() bool { return lastHeaderPath(f.output()) == dir }, "back to containing dir")
}

func TestArchiveZipOpenFile(t *testing.T) {
	dir := t.TempDir()
	zipPath := filepath.Join(dir, "arc.zip")
	makeTestZip(t, zipPath, map[string]string{"inner.txt": "hello\n"})
	marker := filepath.Join(dir, ".opened")
	opener := `printf '%s' "$FILE" > "$MARKER"`
	f := startBrowser(t, testEnv("MARKER="+marker), "--open", opener, dir)
	f.send("\x1b[B") // arc.zip
	time.Sleep(120 * time.Millisecond)
	f.send("\r") // enter archive
	waitContains(t, f, "inner.txt", "inner file listed")
	f.send("\x1b[B") // inner.txt
	time.Sleep(120 * time.Millisecond)
	f.send("o") // open (extracted copy under /tmp)
	waitFor(t, f, func() bool {
		data, err := os.ReadFile(marker)
		if err != nil {
			return false
		}
		path := string(data)
		if !strings.HasPrefix(path, "/tmp/") || !strings.HasSuffix(path, "/inner.txt") {
			return false
		}
		_, err = os.Stat(path)
		return err == nil
	}, "opener received an existing extracted path under /tmp")
}

func TestArchiveRarFailure(t *testing.T) {
	dir := t.TempDir()
	bogus := filepath.Join(dir, "bad.rar")
	if err := os.WriteFile(bogus, []byte("not a rar"), 0644); err != nil {
		t.Fatal(err)
	}
	f := startBrowser(t, testEnv(), dir)
	f.send("\x1b[B") // bad.rar
	time.Sleep(120 * time.Millisecond)
	f.send("\r")
	waitContains(t, f, "Cannot open", "invalid rar fails gracefully")
}

// TestArchiveLazyCorruptEntry proves extraction is lazy: entering a zip whose
// stored data is corrupt succeeds (only the central directory is read), while
// opening the corrupt entry fails on demand.
func TestArchiveLazyCorruptEntry(t *testing.T) {
	dir := t.TempDir()
	zpath := filepath.Join(dir, "corrupt.zip")
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	w, err := zw.CreateHeader(&zip.FileHeader{Name: "data.bin", Method: zip.Store})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write([]byte("ABCDEFGH")); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	data := buf.Bytes()
	idx := bytes.Index(data, []byte("ABCDEFGH"))
	if idx < 0 {
		t.Fatal("stored payload not found")
	}
	data[idx] = 'X' // corrupt the stored data (CRC mismatch), central dir intact
	if err := os.WriteFile(zpath, data, 0644); err != nil {
		t.Fatal(err)
	}

	f := startBrowser(t, testEnv(), dir)
	waitContains(t, f, "corrupt.zip", "corrupt zip listed")
	f.send("\x1b[B")
	time.Sleep(120 * time.Millisecond)
	f.send("\r") // entering must succeed without extracting anything
	waitContains(t, f, "data.bin", "corrupt entry listed after entering")
	// opening the entry extracts on demand and fails
	f.send("\x1b[B")
	time.Sleep(120 * time.Millisecond)
	f.send("o")
	waitContains(t, f, "Cannot extract", "corrupt entry failed to extract on open")
}
