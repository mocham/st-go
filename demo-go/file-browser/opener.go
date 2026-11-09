package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"

	"golang.org/x/sys/unix"
)

func terminalDocumentTool(path string) string {
	var name string
	switch strings.ToLower(filepath.Ext(path)) {
	case ".pdf":
		name = "zat"
	case ".tex", ".md":
		name = "vimtex"
	default:
		return ""
	}
	tool, err := exec.LookPath(name)
	if err != nil {
		return ""
	}
	// Resolve relative PATH entries before setting the child's working directory.
	tool, err = filepath.Abs(tool)
	if err != nil {
		return ""
	}
	return tool
}

// The browser must stop reading the PTY while a document tool owns it. Leave
// the alternate screen rather than nesting it: zat and Vim use it themselves.
func (b *Browser) runTerminalTool(tool, path string) {
	selected := ""
	if b.idx >= 0 && b.idx < len(b.files) {
		selected = b.files[b.idx]
	}
	raw, _ := unix.IoctlGetTermios(b.fd, unix.TCGETS)
	fmt.Fprint(b.out, "\x1b[?1006l\x1b[?1000l\x1b[?25h\x1b[0m\x1b[?1049l")
	b.flush()
	if savedTermios != nil {
		_ = unix.IoctlSetTermios(b.fd, unix.TCSETS, savedTermios)
	}
	cmd := exec.Command(tool, path)
	cmd.Dir = b.dir
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	err := cmd.Start()
	if err == nil {
		wait := make(chan error, 1)
		go func() { wait <- cmd.Wait() }()
		// The child shares the foreground process group and receives terminal
		// signals directly. Consume the browser's copies without quitting on
		// Ctrl+C; also forward signals sent specifically to the browser PID.
		handleSignal := func(sig os.Signal) {
			if sig == syscall.SIGWINCH {
				b.resized = true
			} else {
				_ = cmd.Process.Signal(sig)
				if sig != syscall.SIGINT {
					b.running, b.exitCode = false, 130
				}
			}
		}
		waiting := true
		for waiting {
			select {
			case err = <-wait:
				waiting = false
			case sig := <-b.sigCh:
				handleSignal(sig)
			}
		}
		// A signal and Wait may become ready together.
		for draining := true; draining; {
			select {
			case sig := <-b.sigCh:
				handleSignal(sig)
			default:
				draining = false
			}
		}
	}
	if raw != nil {
		_ = unix.IoctlSetTermios(b.fd, unix.TCSETS, raw)
	}
	// Recover even if a tool failed before its own cleanup. Reset only retained
	// regions/modes, not the image atlas belonging to the normal screen.
	fmt.Fprint(b.out, "\x18\x1b[?2026l\x1b[?1000l\x1b[?1002l\x1b[?1003l\x1b[?1006l\x1b[?2004l\x1bPregion reset\x1b\\\x1b[?1049l\x1b[?7h\x1b[r\x1b[0m\x1b[?1049h\x1b[?25l\x1b[?1000h\x1b[?1006h")
	b.resetClick()
	b.resized = false
	b.buildList()
	b.idx = min(b.idx, len(b.files)-1)
	for i, file := range b.files {
		if file == selected {
			b.idx = i
			break
		}
	}
	b.status = filepath.Base(tool) + " finished"
	if err != nil {
		b.status = filepath.Base(tool) + ": " + displayText(err.Error())
	}
	b.renderAll()
}
