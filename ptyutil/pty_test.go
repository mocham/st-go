package ptyutil

import (
	"strings"
	"testing"
)

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
