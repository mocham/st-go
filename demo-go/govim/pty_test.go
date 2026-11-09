package main

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"golang.org/x/sys/unix"
	"st-go/ptyutil"
)

func TestMain(m *testing.M) {
	if os.Getenv("GOVIM_TEST_PROCESS") == "1" || (len(os.Args) > 1 && os.Args[1] == "--clipboard-owner") {
		os.Exit(run(os.Args[1:]))
	}
	os.Exit(m.Run())
}

type ptyEditor struct {
	master  *os.File
	cmd     *exec.Cmd
	mu      sync.Mutex
	output  bytes.Buffer
	done    chan struct{}
	err     error
	initial unix.Termios
}

func (p *ptyEditor) text() string { p.mu.Lock(); defer p.mu.Unlock(); return p.output.String() }
func (p *ptyEditor) send(t *testing.T, keys string) {
	t.Helper()
	if _, err := p.master.Write([]byte(keys)); err != nil {
		t.Fatal(err)
	}
}
func (p *ptyEditor) await(t *testing.T, what string, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	text := p.text()
	if len(text) > 8000 {
		text = text[len(text)-8000:]
	}
	t.Fatalf("waiting for %s: %q", what, text)
}
func startPTYEditor(t *testing.T, args ...string) *ptyEditor {
	t.Helper()
	master, slave, err := ptyutil.Open()
	if err != nil {
		t.Fatal(err)
	}
	defer slave.Close()
	if err := ptyutil.SetWinSize(master, 24, 80); err != nil {
		t.Fatal(err)
	}
	termios, err := unix.IoctlGetTermios(int(slave.Fd()), unix.TCGETS)
	if err != nil {
		t.Fatal(err)
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cmd, err := ptyutil.Start(slave, append([]string{executable}, args...), append(os.Environ(), "GOVIM_TEST_PROCESS=1", "DISPLAY=", "HOME="+t.TempDir()))
	if err != nil {
		t.Fatal(err)
	}
	p := &ptyEditor{master: master, cmd: cmd, done: make(chan struct{}), initial: *termios}
	go func() { p.err = cmd.Wait(); close(p.done) }()
	go func() {
		buffer := make([]byte, 8192)
		for {
			n, err := master.Read(buffer)
			if n > 0 {
				p.mu.Lock()
				p.output.Write(buffer[:n])
				p.mu.Unlock()
			}
			if err != nil {
				return
			}
		}
	}()
	t.Cleanup(func() {
		cmd.Process.Kill()
		select {
		case <-p.done:
		case <-time.After(2 * time.Second):
			t.Error("editor child did not exit")
		}
		master.Close()
	})
	p.await(t, "alternate screen", func() bool { return strings.Contains(p.text(), "\x1b[?1049h") })
	return p
}

func TestPTYEditingWriteQuitAndTermios(t *testing.T) {
	path := filepath.Join(t.TempDir(), "a ';$quoted.txt")
	if err := os.WriteFile(path, []byte("one two\n"), 0640); err != nil {
		t.Fatal(err)
	}
	p := startPTYEditor(t, path)
	p.send(t, "cw世界")
	p.send(t, "\x1b")
	time.Sleep(100 * time.Millisecond)
	if err := ptyutil.SetWinSize(p.master, 10, 35); err != nil {
		t.Fatal(err)
	}
	p.cmd.Process.Signal(syscall.SIGWINCH)
	p.send(t, ":wq\r")
	select {
	case <-p.done:
	case <-time.After(5 * time.Second):
		t.Fatal("write/quit did not exit")
	}
	if p.err != nil {
		t.Fatalf("editor exit: %v output=%q", p.err, p.text())
	}
	data, err := os.ReadFile(path)
	if err != nil || string(data) != "世界 two\n" {
		t.Fatalf("saved=%q %v", data, err)
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(path), "."+filepath.Base(path)+".govim.swp")); !os.IsNotExist(err) {
		t.Fatal("clean exit left a swap")
	}
	termios, err := unix.IoctlGetTermios(int(p.master.Fd()), unix.TCGETS)
	if err != nil || *termios != p.initial {
		t.Fatalf("termios was not restored: %+v %v", termios, err)
	}
}

func TestPTYKillInInsertAndRecover(t *testing.T) {
	for _, trigger := range []string{"interval", "threshold"} {
		t.Run(trigger, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "document.txt")
			os.WriteFile(path, []byte("original\n"), 0600)
			interval, threshold := "20ms", "1048576"
			if trigger == "threshold" {
				interval, threshold = "1m", "1"
			}
			p := startPTYEditor(t, "--sync-interval", interval, "--sync-bytes", threshold, path)
			p.send(t, "A café 世界")
			p.await(t, "inserted text", func() bool { return strings.Contains(p.text(), "café") })
			time.Sleep(80 * time.Millisecond)
			p.cmd.Process.Kill()
			<-p.done
			original, _ := os.ReadFile(path)
			if string(original) != "original\n" {
				t.Fatal("editing wrote the source file")
			}
			recovery := startPTYEditor(t, "-r", path)
			recovery.await(t, "recovered data", func() bool {
				return strings.Contains(recovery.text(), "Recovered") && strings.Contains(recovery.text(), "café")
			})
			recovery.send(t, ":wq\r")
			select {
			case <-recovery.done:
			case <-time.After(5 * time.Second):
				t.Fatal("recovery did not save")
			}
			data, err := os.ReadFile(path)
			if err != nil || string(data) != "original café 世界\n" {
				t.Fatalf("recovered=%q %v output=%q", data, err, recovery.text())
			}
		})
	}
}

func TestPTYSignalPreservesAndForceQuitDiscards(t *testing.T) {
	path := filepath.Join(t.TempDir(), "document.txt")
	os.WriteFile(path, []byte("original\n"), 0600)
	p := startPTYEditor(t, "--sync-interval", "1m", path)
	p.send(t, "A changed")
	p.await(t, "edit", func() bool { return strings.Contains(p.text(), "changed") })
	p.cmd.Process.Signal(syscall.SIGTERM)
	select {
	case <-p.done:
	case <-time.After(3 * time.Second):
		t.Fatal("TERM did not exit")
	}
	swap := filepath.Join(filepath.Dir(path), ".document.txt.govim.swp")
	if _, err := os.Stat(swap); err != nil {
		t.Fatal("TERM removed recovery journal")
	}
	recovery := startPTYEditor(t, "-r", path)
	recovery.send(t, ":q!\r")
	select {
	case <-recovery.done:
	case <-time.After(3 * time.Second):
		t.Fatal("q! did not exit")
	}
	data, _ := os.ReadFile(path)
	if string(data) != "original\n" {
		t.Fatal("q! wrote unsaved data")
	}
	if _, err := os.Stat(swap); !os.IsNotExist(err) {
		t.Fatal("explicit discard kept swap")
	}
}

func TestPTYBracketedPasteAndMouse(t *testing.T) {
	path := filepath.Join(t.TempDir(), "paste.txt")
	os.WriteFile(path, []byte("abc\n"), 0600)
	p := startPTYEditor(t, path)
	p.send(t, "\x1b[200~literal :q!\n世界\x1b[201~")
	p.await(t, "literal paste", func() bool { return strings.Contains(p.text(), "literal :q!") })
	p.send(t, "\x1b[<0;6;1M\x1b[<32;8;1M\x1b[<0;8;1m")
	p.send(t, "\x1b")
	time.Sleep(100 * time.Millisecond)
	p.send(t, ":wq\r")
	select {
	case <-p.done:
	case <-time.After(5 * time.Second):
		t.Fatal("paste editor did not exit")
	}
	data, _ := os.ReadFile(path)
	if string(data) != "literal :q!\n世界abc\n" {
		t.Fatalf("paste interpreted command or damaged UTF-8: %q", data)
	}
}
