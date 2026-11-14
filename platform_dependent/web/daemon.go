package web

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"syscall"
	"time"
)

const daemonChildEnv = "ST_WEB_DAEMON_CHILD"

var daemonReady *os.File

type daemonResult struct {
	URL   string `json:"url,omitempty"`
	Error string `json:"error,omitempty"`
}

// Re-exec instead of forking a live Go runtime. FD 3 is a one-shot readiness
// pipe; the child owns a new session and has no inherited terminal stdio.
func launchDaemon(args []string) (int, string, error) {
	executable, err := os.Executable()
	if err != nil {
		return 0, "", err
	}
	reader, writer, err := os.Pipe()
	if err != nil {
		return 0, "", err
	}
	defer reader.Close()
	defer writer.Close()
	null, err := os.OpenFile(os.DevNull, os.O_RDWR, 0)
	if err != nil {
		return 0, "", err
	}
	defer null.Close()
	cmd := exec.Command(executable, args...)
	cmd.Env = append(os.Environ(), daemonChildEnv+"=1")
	cmd.ExtraFiles = []*os.File{writer}
	cmd.Stdin, cmd.Stdout, cmd.Stderr = null, null, null
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		return 0, "", err
	}
	writer.Close()
	reader.SetReadDeadline(time.Now().Add(30 * time.Second))
	var result daemonResult
	err = json.NewDecoder(io.LimitReader(reader, 8192)).Decode(&result)
	if err != nil || result.Error != "" || result.URL == "" {
		cmd.Process.Kill()
		cmd.Wait()
		if result.Error != "" {
			return 0, "", fmt.Errorf("daemon: %s", result.Error)
		}
		return 0, "", fmt.Errorf("daemon failed before readiness: %v", err)
	}
	pid := cmd.Process.Pid
	cmd.Process.Release()
	return pid, result.URL, nil
}

func initDaemonChild() bool {
	if os.Getenv(daemonChildEnv) != "1" {
		return false
	}
	os.Unsetenv(daemonChildEnv)
	syscall.CloseOnExec(3) // never let PTYs inherit the startup pipe
	daemonReady = os.NewFile(3, "daemon-ready")
	return true
}

func reportDaemon(url string, err error) {
	if daemonReady == nil {
		return
	}
	result := daemonResult{URL: url}
	if err != nil {
		result.Error = err.Error()
	}
	json.NewEncoder(daemonReady).Encode(result)
	daemonReady.Close()
	daemonReady = nil
}
