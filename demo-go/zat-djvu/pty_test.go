package main

import (
	"bytes"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/sys/unix"
	"st-go/ptyutil"
)

func TestMain(testingMain *testing.M) {
	if os.Getenv("ZAT_DJVU_TEST_PROCESS") == "1" {
		main()
		os.Exit(0)
	}
	os.Exit(testingMain.Run())
}

func TestZatDjVuPTYRenderAndCleanup(t *testing.T) {
	path := generatedDjVu(t)
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
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	command, err := ptyutil.Start(slave, []string{executable, "-zoom", "120", path}, append(os.Environ(), "ZAT_DJVU_TEST_PROCESS=1"))
	if err != nil {
		t.Fatal(err)
	}
	defer command.Process.Kill()
	exit := make(chan error, 1)
	go func() { exit <- command.Wait() }()

	var outputMu sync.Mutex
	var output bytes.Buffer
	var inputMu sync.Mutex
	send := func(value string) {
		inputMu.Lock()
		defer inputMu.Unlock()
		_, _ = master.Write([]byte(value))
	}
	go func() {
		var pending []byte
		var buffer [4096]byte
		for {
			n, err := master.Read(buffer[:])
			if n > 0 {
				outputMu.Lock()
				output.Write(buffer[:n])
				outputMu.Unlock()
				pending = append(pending, buffer[:n]...)
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
					frame := string(pending[start+2 : start+2+end])
					pending = pending[start+4+end:]
					if strings.Contains(frame, "region query") {
						send("\x1bPregion layout main 1 1 80 1 secondary 1 2 80 23\x1b\\")
					}
					if strings.HasPrefix(frame, "region secondary open ") || frame == "region secondary info" {
						send("\x1bPregion secondary image-status 64 48 0 120 zoom 0 0\x1b\\")
					}
				}
			}
			if err != nil {
				return
			}
		}
	}()
	contains := func(value string) bool {
		outputMu.Lock()
		defer outputMu.Unlock()
		return strings.Contains(output.String(), value)
	}
	await := func(value string) {
		t.Helper()
		deadline := time.Now().Add(5 * time.Second)
		for time.Now().Before(deadline) {
			if contains(value) {
				return
			}
			time.Sleep(10 * time.Millisecond)
		}
		outputMu.Lock()
		defer outputMu.Unlock()
		t.Fatalf("waiting for %q; output %q", value, output.String())
	}
	await("viewer zoom 120")
	await("region secondary scroll edges")
	await("1/1")
	send("q")
	select {
	case err := <-exit:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("zat-djvu did not exit")
	}
	await("region reset")
	await("\x1b[?1049l")
	after, err := unix.IoctlGetTermios(int(master.Fd()), unix.TCGETS)
	if err != nil || *before != *after {
		t.Fatalf("termios not restored: %v", err)
	}
}
