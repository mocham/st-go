package frontend

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/sys/unix"
	"st-go/ptyutil"
	"st-go/tui/appbus"
)

// Child mode exercises the public codec through a real raw PTY, including
// loopback, fragmented controls and payloads larger than a single input frame.
func TestAppBusPTYHelper(t *testing.T) {
	result := os.Getenv("ST_TEST_APPBUS_RESULT")
	if result == "" {
		t.Skip("PTY subprocess only")
	}
	settings, err := unix.IoctlGetTermios(0, unix.TCGETS)
	if err != nil {
		t.Fatal(err)
	}
	settings.Lflag &^= unix.ECHO | unix.ICANON | unix.ISIG | unix.IEXTEN
	settings.Iflag &^= unix.ICRNL | unix.IXON
	settings.Oflag &^= unix.OPOST
	settings.Cc[unix.VMIN] = 1
	settings.Cc[unix.VTIME] = 0
	if err := unix.IoctlSetTermios(0, unix.TCSETS, settings); err != nil {
		t.Fatal(err)
	}
	client, err := appbus.New(os.Stdout, os.Getenv("ST_GO_APPBUS_TOKEN"), "pty-test")
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	chunks := make(chan []byte, 8)
	go func() {
		for {
			buffer := make([]byte, 317)
			n, err := os.Stdin.Read(buffer)
			if n > 0 {
				chunks <- buffer[:n]
			}
			if err != nil {
				close(chunks)
				return
			}
		}
	}()
	ticker := time.NewTicker(25 * time.Millisecond)
	defer ticker.Stop()
	deadline := time.After(10 * time.Second)
	var pending []byte
	self, peer := false, false
	lastPublish := time.Time{}
	for {
		select {
		case now := <-ticker.C:
			if err := client.Tick(now); err != nil {
				t.Fatal(err)
			}
			if now.Sub(lastPublish) > 250*time.Millisecond {
				lastPublish = now
				if err := client.Publish(strings.Repeat("chunked 世界 ", 1200)); err != nil {
					t.Fatal(err)
				}
			}
		case data, ok := <-chunks:
			if !ok {
				t.Fatal("PTY closed")
			}
			pending = append(pending, data...)
			for {
				end := bytes.Index(pending, []byte("\x1b\\"))
				if end < 0 {
					break
				}
				frame := pending[:end]
				pending = pending[end+2:]
				start := bytes.Index(frame, []byte("\x1bP"))
				if start < 0 {
					continue
				}
				message, err := client.Receive(string(frame[start+2:]))
				if err != nil {
					t.Fatal(err)
				}
				if message != nil {
					var value string
					if err := json.Unmarshal(message.Data, &value); err != nil || value != strings.Repeat("chunked 世界 ", 1200) {
						t.Fatal("corrupted payload")
					}
					if message.Sender == client.ID {
						self = true
					} else {
						peer = true
					}
				}
			}
			if self && peer {
				if err := client.Publish(strings.Repeat("chunked 世界 ", 1200)); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(result, []byte("ok"), 0600); err != nil {
					t.Fatal(err)
				}
				return
			}
		case <-deadline:
			t.Fatal("timed out waiting for peer and loopback")
		}
	}
}

func TestAppBusAcrossRealPTYs(t *testing.T) {
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	broker := newAppBroker()
	dir := t.TempDir()
	var outputMu sync.Mutex
	outputs := make(map[string]string)
	for _, name := range []string{"first", "second"} {
		terminal := regionTestTerminal(t)
		terminal.appBroker = broker
		terminal.cfg.AppBusToken = appbus.ID()
		master, slave, err := ptyutil.Open()
		if err != nil {
			t.Fatal(err)
		}
		if err := ptyutil.SetWinSizePixels(master, 24, 80, 320, 192); err != nil {
			t.Fatal(err)
		}
		env := append(envWithout(os.Environ(), "ST_TEST_APPBUS_RESULT", "ST_GO_APPBUS_TOKEN"), "ST_TEST_APPBUS_RESULT="+filepath.Join(dir, name), "ST_GO_APPBUS_TOKEN="+terminal.cfg.AppBusToken)
		terminal.termCore.SetWriter(func(data []byte) { master.Write(data) })
		child, err := ptyutil.Start(slave, []string{executable, "-test.run=^TestAppBusPTYHelper$"}, env)
		slave.Close()
		if err != nil {
			t.Fatal(err)
		}
		done := make(chan struct{})
		go func() {
			defer close(done)
			var pending []byte
			buffer := make([]byte, 719)
			for {
				n, err := master.Read(buffer)
				if n > 0 {
					outputMu.Lock()
					outputs[name] += string(buffer[:n])
					if len(outputs[name]) > 4096 {
						outputs[name] = outputs[name][len(outputs[name])-4096:]
					}
					outputMu.Unlock()
					pending = append(pending, buffer[:n]...)
					consumed := terminal.writeTerminalOutput(terminal.termCore, pending)
					pending = pending[consumed:]
				}
				if err != nil {
					return
				}
			}
		}()
		t.Cleanup(func() { child.Process.Kill(); master.Close(); <-done; child.Wait() })
	}
	deadline := time.Now().Add(12 * time.Second)
	for time.Now().Before(deadline) {
		a, e1 := os.ReadFile(filepath.Join(dir, "first"))
		b, e2 := os.ReadFile(filepath.Join(dir, "second"))
		if e1 == nil && e2 == nil && string(a) == "ok" && string(b) == "ok" {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	outputMu.Lock()
	t.Logf("child output tails: %q", outputs)
	outputMu.Unlock()
	broker.mu.Lock()
	for id, p := range broker.peers {
		t.Logf("peer %s queued=%d overflow=%v", id, len(p.queue), p.overflow)
	}
	broker.mu.Unlock()
	t.Fatal("terminal channels did not carry complete messages between PTYs")
}
