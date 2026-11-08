package main

import (
	"bytes"
	"crypto/sha256"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"golang.org/x/sys/unix"
	"st-go/ptyutil"
)

func TestMain(m *testing.M) {
	if os.Getenv("VIMTEX_TEST_PROCESS") == "1" {
		main()
		os.Exit(0)
	}
	os.Exit(m.Run())
}

type previewFrame struct {
	command string
	at      time.Time
}

func (f previewFrame) path() string {
	path, _, _ := strings.Cut(strings.TrimPrefix(f.command, "region secondary open '"), "' background ")
	return path
}

type vimtexPTY struct {
	master *os.File
	cmd    *exec.Cmd
	exit   chan error
	mu     sync.Mutex
	frames []previewFrame
	output []byte
}

func startVimtexPTY(t *testing.T, source, ready string) *vimtexPTY {
	t.Helper()
	if _, err := exec.LookPath("vim"); err != nil {
		t.Skip("Vim is not installed")
	}
	master, slave, err := ptyutil.Open()
	if err != nil {
		t.Fatal(err)
	}
	if err := ptyutil.SetWinSizePixels(master, 24, 80, 640, 384); err != nil {
		t.Fatal(err)
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	args := []string{executable, source, "-u", "NONE", "-i", "NONE", "-n", "-N", "--noplugin", "-c",
		fmt.Sprintf("call writefile([string(&columns), string(getpid())], '%s')", ready)}
	cmd, err := ptyutil.Start(slave, args, append(os.Environ(), "VIMTEX_TEST_PROCESS=1", "TERM=xterm-256color"))
	if err != nil {
		master.Close()
		t.Fatal(err)
	}
	f := &vimtexPTY{master: master, cmd: cmd, exit: make(chan error, 1)}
	go func() { f.exit <- cmd.Wait() }()
	t.Cleanup(func() {
		cmd.Process.Signal(syscall.SIGTERM)
		select {
		case <-f.exit:
		case <-time.After(2 * time.Second):
			cmd.Process.Kill()
		}
		master.Close()
	})
	go func() {
		var pending []byte
		continuous := false
		buf := make([]byte, 8192)
		for {
			n, err := master.Read(buf)
			if n > 0 {
				f.mu.Lock()
				f.output = append(f.output, buf[:n]...)
				f.mu.Unlock()
				pending = append(pending, buf[:n]...)
				for {
					start := bytes.Index(pending, []byte("\x1bP"))
					if start < 0 {
						if len(pending) > 1 {
							pending = pending[len(pending)-1:]
						}
						break
					}
					end := bytes.Index(pending[start:], []byte("\x1b\\"))
					if end < 0 {
						break
					}
					end += start
					command := string(pending[start+2 : end])
					pending = pending[end+2:]
					f.mu.Lock()
					f.frames = append(f.frames, previewFrame{command, time.Now()})
					f.mu.Unlock()
					if strings.Contains(command, "region query") {
						// Emulate st-go's split reply and the outer narrowed PTY.
						var previewPercent int
						if _, err := fmt.Sscanf(command, "region split %d%%;", &previewPercent); err != nil || previewPercent < 1 || previewPercent > 99 {
							return
						}
						previewCols := max(1, 80*previewPercent/100)
						mainCols := 80 - previewCols
						ptyutil.SetWinSizePixels(master, 24, mainCols, mainCols*8, 384)
						master.Write([]byte(fmt.Sprintf("\x1bPregion layout main 1 1 %d 24 secondary %d 1 %d 24\x1b\\", mainCols, mainCols+1, previewCols)))
					}
					fields := strings.Fields(command)
					if strings.HasPrefix(command, "region secondary open ") {
						continuous = strings.HasSuffix(command, "continuous")
					}
					if len(fields) == 4 && fields[0] == "region" && fields[1] == "secondary" && (fields[2] == "seek" || fields[2] == "scroll") {
						if _, err := strconv.Atoi(fields[3]); err == nil && (fields[2] == "seek" || !continuous) {
							master.Write([]byte("\x1bPregion secondary event " + fields[2] + " " + fields[3] + "\x1b\\"))
						}
					}
				}
			}
			if err != nil {
				return
			}
		}
	}()
	return f
}

func (f *vimtexPTY) previews() []previewFrame {
	f.mu.Lock()
	defer f.mu.Unlock()
	var frames []previewFrame
	for _, frame := range f.frames {
		if strings.HasPrefix(frame.command, "region secondary open ") {
			frames = append(frames, frame)
		}
	}
	return frames
}

func awaitPTY(t *testing.T, f *vimtexPTY, predicate func() bool, label string) {
	t.Helper()
	deadline := time.Now().Add(7 * time.Second)
	for time.Now().Before(deadline) {
		if predicate() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	f.mu.Lock()
	output := string(f.output)
	f.mu.Unlock()
	if len(output) > 2000 {
		output = output[len(output)-2000:]
	}
	t.Fatalf("timeout waiting for %s; output=%q", label, output)
}

func TestVimtexPTYSaveRefreshResizeAndCleanup(t *testing.T) {
	dir := t.TempDir()
	source, ready := filepath.Join(dir, "source.tex"), filepath.Join(dir, "ready")
	writeSource(t, source, "\\documentclass{article}\n\\begin{document}\nFirst version.\n\\end{document}\n")
	f := startVimtexPTY(t, source, ready)
	awaitPTY(t, f, func() bool { return len(f.previews()) == 1 }, "first preview")
	awaitPTY(t, f, func() bool {
		data, _ := os.ReadFile(ready)
		return len(strings.Fields(string(data))) == 2
	}, "Vim startup")
	data, err := os.ReadFile(ready)
	if err != nil || !strings.HasPrefix(string(data), "32\n") {
		t.Fatalf("Vim did not receive pane width: %q, %v", data, err)
	}
	first := f.previews()[0]
	path := first.path()
	if !strings.Contains(first.command, "background '"+defaultPreviewBG+"'") {
		t.Fatal("initial preview omitted its background color")
	}
	pdf, err := os.ReadFile(path)
	if err != nil || !bytes.HasPrefix(pdf, []byte("%PDF-")) {
		t.Fatalf("missing first PDF: %s, %v", path, err)
	}
	firstHash := sha256.Sum256(pdf)
	// Save twice through real Vim; both saves must coalesce into the latest
	// preview without blocking the editor's normal command processing.
	f.master.Write([]byte(":call setline(3, 'Second version.')\r:w\r"))
	f.master.Write([]byte(":call setline(3, 'Latest saved version.')\r:w\r"))
	awaitPTY(t, f, func() bool { return len(f.previews()) >= 2 }, "saved preview")
	second := f.previews()[1]
	if !strings.Contains(second.command, "background '"+defaultPreviewBG+"'") {
		t.Fatal("save refresh omitted its background color")
	}
	if second.at.Sub(first.at) <= 2*time.Second || !strings.HasSuffix(second.command, "fit-width continuous") {
		t.Fatalf("bad redraw interval/continuous mode: %v, %q", second.at.Sub(first.at), second.command)
	}
	pdf, err = os.ReadFile(path)
	if err != nil || sha256.Sum256(pdf) == firstHash {
		t.Fatal("save did not replace the preview PDF")
	}
	if err := ptyutil.SetWinSizePixels(f.master, 28, 52, 416, 448); err != nil {
		t.Fatal(err)
	}
	awaitPTY(t, f, func() bool {
		f.mu.Lock()
		defer f.mu.Unlock()
		return bytes.Contains(f.output, []byte("\x1b[28;"))
	}, "Vim's resized status line")
	resized := filepath.Join(dir, "resized")
	f.master.Write([]byte(fmt.Sprintf(":call writefile([string(&columns), string(&lines)], '%s')\r", resized)))
	awaitPTY(t, f, func() bool {
		data, _ := os.ReadFile(resized)
		return string(data) == "52\n28\n"
	}, "Vim resize")
	// Exercise bidirectional PTY backpressure with more than a kernel input
	// queue of data, while Vim also generates screen updates.
	pasted := filepath.Join(dir, "pasted")
	f.master.Write([]byte(":set paste\ri" + strings.Repeat("x", 65536) + "\x1b"))
	f.master.Write([]byte(fmt.Sprintf(":call writefile([string(strlen(getline(1)))], '%s')\r", pasted)))
	awaitPTY(t, f, func() bool {
		data, _ := os.ReadFile(pasted)
		n, _ := strconv.Atoi(strings.TrimSpace(string(data)))
		return n >= 65536
	}, "large paste without a PTY deadlock")
	f.master.Write([]byte(":qa!\r"))
	select {
	case err := <-f.exit:
		f.exit <- err // let cleanup consume the already-completed wait
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("vimtex did not exit after Vim")
	}
	if _, err := os.Stat(filepath.Dir(path)); !os.IsNotExist(err) {
		t.Fatal("temporary preview directory survived Vim exit")
	}
	attrs, err := unix.IoctlGetTermios(int(f.master.Fd()), unix.TCGETS)
	if err != nil || attrs.Lflag&unix.ICANON == 0 || attrs.Lflag&unix.ECHO == 0 {
		t.Fatalf("outer terminal mode was not restored: %v", err)
	}
}

func TestVimtexPTYTerminationStopsVim(t *testing.T) {
	dir := t.TempDir()
	source, ready := filepath.Join(dir, "source.tex"), filepath.Join(dir, "ready")
	writeSource(t, source, "\\begin{document}Hello.\\end{document}")
	f := startVimtexPTY(t, source, ready)
	awaitPTY(t, f, func() bool {
		data, _ := os.ReadFile(ready)
		return len(strings.Fields(string(data))) == 2
	}, "Vim startup")
	data, _ := os.ReadFile(ready)
	lines := strings.Fields(string(data))
	if len(lines) != 2 {
		t.Fatalf("invalid Vim PID record: %q", data)
	}
	pid, _ := strconv.Atoi(lines[1])
	if err := f.cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-f.exit:
		f.exit <- err
	case <-time.After(3 * time.Second):
		t.Fatal("vimtex did not terminate")
	}
	if err := syscall.Kill(pid, 0); err != syscall.ESRCH {
		t.Fatalf("Vim survived wrapper termination: pid=%d, err=%v", pid, err)
	}
}

func TestVimtexPTYVimscriptAndWheelNavigation(t *testing.T) {
	dir := t.TempDir()
	source, ready := filepath.Join(dir, "source.tex"), filepath.Join(dir, "ready")
	var document strings.Builder
	document.WriteString("\\documentclass{article}\n\\begin{document}\n")
	for i := 0; i < 100; i++ {
		fmt.Fprintf(&document, "Paragraph %d: surrounding content for an approximate source viewport.\n\n", i)
	}
	document.WriteString("\\end{document}\n")
	writeSource(t, source, document.String())
	f := startVimtexPTY(t, source, ready)
	awaitPTY(t, f, func() bool { return len(f.previews()) == 1 }, "initial source viewport")
	path := f.previews()[0].path()
	first, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	hasFrame := func(prefix string) bool {
		f.mu.Lock()
		defer f.mu.Unlock()
		for _, frame := range f.frames {
			if strings.HasPrefix(frame.command, prefix) {
				return true
			}
		}
		return false
	}
	start := time.Now()
	f.master.Write([]byte(":VimtexPreview 120\r"))
	awaitPTY(t, f, func() bool { return hasFrame("region secondary position ") }, "immediate Vimscript seek")
	f.master.Write([]byte("\x1bPregion secondary event scroll -1\x1b\\"))
	awaitPTY(t, f, func() bool { return hasFrame("region secondary scroll -1") }, "immediate scroll forwarding")
	if time.Since(start) > time.Second {
		t.Fatal("navigation waited for the save-refresh gap")
	}
	modified := filepath.Join(dir, "modified")
	f.master.Write([]byte(fmt.Sprintf(":call writefile([string(&modified)], '%s')\r", modified)))
	awaitPTY(t, f, func() bool {
		data, _ := os.ReadFile(modified)
		return string(data) == "0\n"
	}, "navigation events leaving Vim's buffer untouched")
	// Wait beyond the refresh gap: navigating must not queue a later PDF render.
	time.Sleep(2200 * time.Millisecond)
	current, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(first, current) || len(f.previews()) != 1 {
		t.Fatal("navigation regenerated the saved document")
	}
	f.master.Write([]byte(":qa!\r"))
}

func TestVimtexPTYCursorMarkerFollowsPublishedSaves(t *testing.T) {
	dir := t.TempDir()
	source, include := filepath.Join(dir, "source.tex"), filepath.Join(dir, "part.tex")
	writeSource(t, source, "\\begin{document}\n\\input{part}\n\\end{document}\n")
	original := "First.\n\nSecond.\n\nThird.\n\nFourth."
	writeSource(t, include, original)
	f := startVimtexPTY(t, source, filepath.Join(dir, "ready"))
	positions := func() []previewFrame {
		f.mu.Lock()
		defer f.mu.Unlock()
		var result []previewFrame
		for _, frame := range f.frames {
			if strings.HasPrefix(frame.command, "region secondary position ") {
				result = append(result, frame)
			}
		}
		return result
	}
	awaitPTY(t, f, func() bool { return len(f.previews()) == 1 }, "initial unmarked preview")
	if len(positions()) != 0 {
		t.Fatal("unmarked source caused an automatic seek")
	}
	for i, change := range []struct{ text, position string }{
		{strings.Replace(original, "Second.", "Second. %cursor", 1), "375000"},
		{strings.Replace(original, "Fourth.", "Fourth. %cursor", 1), "875000"},
		{original, ""}, // removing the marker should retain manual scrolling
	} {
		writeSource(t, include+".next", change.text)
		if err := os.Rename(include+".next", include); err != nil {
			t.Fatal(err)
		}
		awaitPTY(t, f, func() bool { return len(f.previews()) == i+2 }, "saved include preview")
		if change.position != "" {
			awaitPTY(t, f, func() bool { return len(positions()) == i+1 }, "saved cursor marker navigation")
			frame := positions()[i]
			if frame.command != "region secondary position "+change.position || frame.at.Before(f.previews()[i+1].at) {
				t.Fatalf("marker navigation did not follow its published revision: %#v", frame)
			}
		}
	}
	// Multiple watcher polls must not keep seeking, including after removal.
	time.Sleep(600 * time.Millisecond)
	if len(positions()) != 2 {
		t.Fatalf("unexpected repeated/removed marker navigation: %v", positions())
	}
	f.master.Write([]byte(":qa!\r"))
}
