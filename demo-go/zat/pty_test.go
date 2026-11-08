package main

import (
	"bytes"
	"os"
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
	if os.Getenv("ZAT_TEST_PROCESS") == "1" {
		main()
		os.Exit(0)
	}
	os.Exit(m.Run())
}

func TestZatPTYNavigationAndCleanup(t *testing.T) {
	for _, mode := range []string{"quit", "signal", "unsupported"} {
		t.Run(mode, func(t *testing.T) {
			master, slave, err := ptyutil.Open()
			if err != nil {
				t.Fatal(err)
			}
			defer master.Close()
			defer slave.Close()
			if err := ptyutil.SetWinSizePixels(master, 24, 80, 640, 384); err != nil {
				t.Fatal(err)
			}
			before, err := unix.IoctlGetTermios(int(slave.Fd()), unix.TCGETS)
			if err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(t.TempDir(), "a;'quoted'.pdf")
			if err := os.WriteFile(path, []byte("%PDF-1.4\n"), 0600); err != nil {
				t.Fatal(err)
			}
			exe, _ := os.Executable()
			cmd, err := ptyutil.Start(slave, []string{exe, "-page", "2", path}, append(os.Environ(), "ZAT_TEST_PROCESS=1"))
			if err != nil {
				t.Fatal(err)
			}
			defer cmd.Process.Kill()
			exit := make(chan error, 1)
			go func() { exit <- cmd.Wait() }()
			var mu sync.Mutex
			var output bytes.Buffer
			go func() {
				var pending []byte
				var buf [4096]byte
				for {
					n, err := master.Read(buf[:])
					if n > 0 {
						mu.Lock()
						output.Write(buf[:n])
						mu.Unlock()
						pending = append(pending, buf[:n]...)
						for {
							start := bytes.Index(pending, []byte("\x1bP"))
							if start < 0 {
								if len(pending) > 1 {
									pending = pending[len(pending)-1:]
								}
								break
							}
							end := bytes.Index(pending[start+2:], []byte("\x1b\\"))
							if end < 0 {
								pending = pending[start:]
								break
							}
							command := string(pending[start+2 : start+2+end])
							pending = pending[start+4+end:]
							if mode == "unsupported" {
								continue
							}
							if strings.Contains(command, "region query") {
								ptyutil.SetWinSizePixels(master, 1, 80, 640, 16)
								master.Write([]byte("\x1bPregion layout main 1 1 80 1 secondary 1 2 80 23\x1b\\"))
							}
							if command == "region secondary info" {
								master.Write([]byte("\x1bPregion secondary status 2 5 250000 100 0 0\x1b\\"))
							}
							if strings.HasPrefix(command, "region secondary find") {
								master.Write([]byte("\x1bPregion secondary found 3\x1b\\"))
							}
						}
					}
					if err != nil {
						return
					}
				}
			}()
			contains := func(s string) bool { mu.Lock(); defer mu.Unlock(); return strings.Contains(output.String(), s) }
			await := func(s string) {
				t.Helper()
				deadline := time.Now().Add(4 * time.Second)
				for time.Now().Before(deadline) {
					if contains(s) {
						return
					}
					time.Sleep(5 * time.Millisecond)
				}
				mu.Lock()
				defer mu.Unlock()
				t.Fatalf("waiting for %q; output %q", s, output.String())
			}
			if mode != "unsupported" {
				await("2/5")
				master.Write([]byte("3j7G+/needle\r"))
				await("region secondary scroll 3")
				await("region secondary page 7")
				await("region secondary zoom 110")
				await("Text found on page 3")
				if mode == "quit" {
					master.Write([]byte(":q\r"))
				} else {
					cmd.Process.Signal(syscall.SIGTERM)
				}
			}
			select {
			case err := <-exit:
				if mode == "quit" && err != nil {
					t.Fatal(err)
				}
				if mode == "unsupported" && err == nil {
					t.Fatal("unsupported terminal unexpectedly succeeded")
				}
			case <-time.After(5 * time.Second):
				t.Fatal("zat did not exit")
			}
			await("region reset")
			await("\x1b[?1049l")
			after, err := unix.IoctlGetTermios(int(master.Fd()), unix.TCGETS)
			if err != nil || *before != *after {
				t.Fatalf("termios not restored: %v", err)
			}
		})
	}
}
