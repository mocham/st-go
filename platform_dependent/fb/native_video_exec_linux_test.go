//go:build linux

package fb

import (
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"golang.org/x/sys/unix"
	"st-go/platform_neutral/api"
	"st-go/ptyutil"
)

// The execution test uses a PRIVATE PTY as a mock physical console. It never
// opens a numbered VT or framebuffer, but exercises the real fork/exec pgrp,
// /dev/tty inheritance, stopped-child detection and foreground reclamation.
func TestNativeVideoExecHelper(t *testing.T) {
	kind := os.Getenv("ST_TEST_NATIVE_KIND")
	if kind == "" {
		return
	}
	if kind == "player" {
		foreground, err := unix.IoctlGetInt(0, unix.TIOCGPGRP)
		sid, _ := unix.Getsid(0)
		if err != nil || foreground != unix.Getpgrp() || strconv.Itoa(sid) != os.Getenv("ST_TEST_NATIVE_SESSION") {
			t.Fatalf("player lacks the real foreground tty/session: %d %d %v", foreground, sid, err)
		}
		if os.Getenv("ST_TEST_NATIVE_STOP") == "1" {
			unix.Kill(os.Getpid(), unix.SIGSTOP)
			for {
				time.Sleep(time.Hour)
			}
		}
		return
	}
	sid, _ := unix.Getsid(0)
	os.Setenv("ST_TEST_NATIVE_SESSION", strconv.Itoa(sid))
	tty, err := os.OpenFile("/dev/tty", os.O_RDWR|unix.O_NOCTTY, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer tty.Close()
	d := &Driver{done: make(chan struct{}), signals: make(chan os.Signal, 8)}
	timer := time.AfterFunc(3*time.Second, func() { close(d.done) })
	defer timer.Stop()
	err = d.runNativePlayer(api.NativeVideoRequest{File: "/synthetic/movie.mp4", ALSAConfig: "/synthetic/alsa.conf", AudioDevice: "alsa/plughw:CARD=1,DEV=0", Volume: 33}, tty)
	signal.Ignore(unix.SIGTTOU)
	if restore := unix.IoctlSetPointerInt(int(tty.Fd()), unix.TIOCSPGRP, unix.Getpgrp()); restore != nil {
		t.Fatal(restore)
	}
	if (err != nil) != (os.Getenv("ST_TEST_NATIVE_STOP") == "1") {
		t.Fatal("incorrect native child completion", err)
	}
}

func TestNativeVideoExecForegroundAndStoppedChild(t *testing.T) {
	for _, stopped := range []bool{false, true} {
		t.Run(fmt.Sprint(stopped), func(t *testing.T) {
			dir := t.TempDir()
			mpv := filepath.Join(dir, "mpv")
			if err := os.WriteFile(mpv, []byte("#!/bin/sh\nST_TEST_NATIVE_KIND=player exec \"$ST_TEST_NATIVE_BINARY\" -test.run=^TestNativeVideoExecHelper$\n"), 0700); err != nil {
				t.Fatal(err)
			}
			master, slave, err := ptyutil.Open()
			if err != nil {
				t.Fatal(err)
			}
			defer master.Close()
			env := append(os.Environ(), "ST_TEST_NATIVE_KIND=parent", "ST_TEST_NATIVE_BINARY="+os.Args[0], "PATH="+dir+":"+os.Getenv("PATH"))
			if stopped {
				env = append(env, "ST_TEST_NATIVE_STOP=1")
			}
			child, err := ptyutil.Start(slave, []string{os.Args[0], "-test.run=^TestNativeVideoExecHelper$"}, env)
			if err != nil {
				slave.Close()
				t.Fatal(err)
			}
			go io.Copy(io.Discard, master)
			done := make(chan error, 1)
			go func() { done <- child.Wait() }()
			select {
			case err := <-done:
				if err != nil {
					t.Fatal("native foreground execution failed", err)
				}
			case <-time.After(8 * time.Second):
				child.Process.Kill()
				master.Close()
				<-done
				t.Fatal("native player/foreground recovery stalled")
			}
		})
	}
}
