package main

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"golang.org/x/sys/unix"
	"st-go/pdfmeta"
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
			var inputMu sync.Mutex
			send := func(s string) { inputMu.Lock(); defer inputMu.Unlock(); master.Write([]byte(s)) }
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
								send("\x1bPregion layout main 1 1 80 1 secondary 1 2 80 23\x1b\\")
							}
							if strings.HasPrefix(command, "region split ") {
								// Match the terminal's actual split: its percentage is
								// the right PDF width, leaving the PTY on the left.
								percent, _ := strconv.Atoi(strings.TrimSuffix(strings.TrimPrefix(command, "region split "), "%"))
								cols := 80 - 80*percent/100
								ptyutil.SetWinSizePixels(master, 24, cols, cols*8, 384)
							}
							if command == "region secondary info" {
								send("\x1bPregion secondary status 2 5 250000 100 0 0\x1b\\")
							}
							if strings.HasPrefix(command, "region secondary find") {
								send("\x1bPregion secondary found 3\x1b\\")
							}
							fields := strings.Fields(command)
							if len(fields) >= 5 && fields[0] == "region" && fields[1] == "secondary" && (fields[2] == "toc" || (fields[2] == "text" && len(fields) == 6)) {
								page, offset := 0, 0
								if fields[2] == "text" {
									page, _ = strconv.Atoi(fields[4])
									offset, _ = strconv.Atoi(fields[5])
								} else {
									offset, _ = strconv.Atoi(fields[4])
								}
								payload := pdfmeta.Payload{TOC: []pdfmeta.Entry{{Title: "First chapter", Page: 1}, {Title: "Second chapter", Page: 3}}}
								if fields[2] == "text" {
									payload = pdfmeta.Payload{Text: fmt.Sprintf("Selectable text for page %d\n", page) + strings.Repeat("More page text.\n", 300)}
								}
								data, _ := json.Marshal(payload)
								end := min(len(data), offset+pdfmeta.ChunkBytes)
								next := end
								if end == len(data) {
									next = -1
								}
								send(fmt.Sprintf("\x1bPregion secondary data %s %s %d %d %d %s\x1b\\", fields[3], fields[2], page, offset, next, base64.StdEncoding.EncodeToString(data[offset:end])))
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
				send("3j7G+/needle\r")
				await("region secondary scroll 3")
				await("region secondary page 7")
				await("region secondary zoom 110")
				await("Text found on page 3")
				send("\t")
				await("CONTENTS")
				await("Second chapter")
				send("\x1b[B\r")
				await("region secondary page 3")
				send("\x1b[Z")
				await("region split 40%")
				await("Selectable text for page 2")
				await("Ctrl+Shift+C copies")
				// Resize reflows the real text pane without another extraction.
				ptyutil.SetWinSizePixels(master, 30, 36, 288, 480)
				send("\x1b[6~\x1b[5~")
				if mode == "quit" {
					send(":q\r")
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
			await("\x1b[?1000l\x1b[?1006l")
			await("\x1b[?1049l")
			after, err := unix.IoctlGetTermios(int(master.Fd()), unix.TCGETS)
			if err != nil || *before != *after {
				t.Fatalf("termios not restored: %v", err)
			}
		})
	}
}
