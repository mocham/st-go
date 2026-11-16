// Package sshtest supplies a real subprocess/TCP-forward fixture for SSH clients.
package sshtest

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"testing"
	"time"
)

type Record struct {
	PID     int
	Args    []string
	Address string
}

// Process must be called by a test named TestSSHTunnelProcess. Install invokes
// that test as a child executable; ordinary test runs return immediately.
func Process() {
	if os.Getenv("SSHTUNNEL_TEST_PROCESS") != "1" {
		return
	}
	args := os.Args[slices.Index(os.Args, "--")+1:]
	index := slices.Index(args, "-L")
	if index < 0 || index+1 >= len(args) {
		os.Exit(2)
	}
	parts := strings.SplitN(args[index+1], ":", 3)
	if len(parts) != 3 {
		os.Exit(2)
	}
	address := parts[0] + ":" + parts[1]
	record, _ := json.Marshal(Record{PID: os.Getpid(), Args: args, Address: address})
	if err := os.WriteFile(os.Getenv("SSHTUNNEL_TEST_RECORD"), record, 0600); err != nil {
		os.Exit(2)
	}
	switch os.Getenv("SSHTUNNEL_TEST_MODE") {
	case "fail":
		fmt.Fprintln(os.Stderr, "Permission denied (publickey).")
		os.Exit(255)
	case "stall":
		time.Sleep(time.Hour)
		os.Exit(2)
	}
	listener, err := net.Listen("tcp4", address)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	for {
		local, err := listener.Accept()
		if err != nil {
			os.Exit(2)
		}
		go func() {
			defer local.Close()
			remote, err := net.Dial("tcp", parts[2])
			if err != nil {
				return
			}
			defer remote.Close()
			connections, err := os.OpenFile(os.Getenv("SSHTUNNEL_TEST_RECORD")+".connections", os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0600)
			if err != nil {
				os.Exit(2)
			}
			fmt.Fprintln(connections, remote.LocalAddr().String())
			connections.Close()
			go func() {
				io.Copy(remote, local)
				remote.(*net.TCPConn).CloseWrite()
			}()
			io.Copy(local, remote)
		}()
	}
}

// Install puts a fake ssh first in PATH. It forwards TCP, fails authentication
// (mode "fail"), or stalls during startup (mode "stall"). No SSH account is used.
func Install(t *testing.T, mode string) string {
	t.Helper()
	dir := t.TempDir()
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("SSHTUNNEL_TEST_EXECUTABLE", executable)
	t.Setenv("SSHTUNNEL_TEST_PROCESS", "1")
	t.Setenv("SSHTUNNEL_TEST_MODE", mode)
	record := filepath.Join(dir, "record.json")
	t.Setenv("SSHTUNNEL_TEST_RECORD", record)
	script := "#!/bin/sh\nexec \"$SSHTUNNEL_TEST_EXECUTABLE\" -test.run=^TestSSHTunnelProcess$ -- \"$@\"\n"
	if err := os.WriteFile(filepath.Join(dir, "ssh"), []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return record
}

func ReadProcess(t *testing.T, path string) Record {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		var record Record
		data, err := os.ReadFile(path)
		if err == nil && json.Unmarshal(data, &record) == nil {
			return record
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("SSH process did not record its arguments")
	return Record{}
}

func AssertStopped(t *testing.T, record Record) {
	t.Helper()
	if err := syscall.Kill(record.PID, 0); !errors.Is(err, syscall.ESRCH) {
		t.Fatalf("SSH process %d was not reaped: %v", record.PID, err)
	}
	if connection, err := net.DialTimeout("tcp", record.Address, 100*time.Millisecond); err == nil {
		connection.Close()
		t.Fatal("SSH listener survived client cleanup")
	}
}

func AssertForwarded(t *testing.T, path, remoteAddress string) {
	t.Helper()
	connections, err := os.ReadFile(path + ".connections")
	if err != nil || !strings.Contains("\n"+string(connections), "\n"+remoteAddress+"\n") {
		t.Error("HTTP request bypassed the SSH forward")
	}
}
