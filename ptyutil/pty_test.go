package ptyutil

import (
	"strings"
	"testing"

	"golang.org/x/sys/unix"
)

func TestSetWinSizePixels(t *testing.T) {
	master, slave, err := Open()
	if err != nil {
		t.Fatal(err)
	}
	defer master.Close()
	defer slave.Close()
	if err := SetWinSizePixels(master, 31, 97, 873, 620); err != nil {
		t.Fatal(err)
	}
	ws, err := unix.IoctlGetWinsize(int(slave.Fd()), unix.TIOCGWINSZ)
	if err != nil {
		t.Fatal(err)
	}
	if ws.Row != 31 || ws.Col != 97 || ws.Xpixel != 873 || ws.Ypixel != 620 {
		t.Fatalf("winsize = %+v", ws)
	}
}

func TestStartDir(t *testing.T) {
	dir := t.TempDir()
	master, slave, err := Open()
	if err != nil {
		t.Fatal(err)
	}
	defer master.Close()
	cmd, err := StartDir(slave, []string{"/bin/pwd"}, nil, dir)
	if err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 1024)
	n, err := master.Read(buf)
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Wait(); err != nil {
		t.Fatal(err)
	}
	if got := strings.TrimSpace(string(buf[:n])); got != dir {
		t.Fatalf("pwd=%q want %q", got, dir)
	}
}
