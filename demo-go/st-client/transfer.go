package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"
	"unicode"

	term "st-go/platform_neutral/stterminal"
)

type transferPrompt struct {
	upload bool
	source string
	text   string
}

type transferResult struct {
	upload bool
	err    error
}

func (a *app) beginTransfer(upload bool) {
	if a.remote.ssh == nil {
		return
	}
	if a.transferCancel != nil {
		a.note("A transfer is running · Ctrl-] k cancels it")
		return
	}
	a.prompt = &transferPrompt{upload: upload}
	a.prefix = false
	a.statusDirty = true
}

func (p *transferPrompt) label() string {
	if p.source == "" {
		if p.upload {
			return "Upload local path: "
		}
		return "Download remote path: "
	}
	if p.upload {
		return "Remote destination: "
	}
	return "Local destination: "
}

func (p *transferPrompt) display(cols int) string {
	text := []rune(p.label() + p.text + "_")
	start, width := len(text), 0
	for start > 0 {
		w := term.CellWidth(text[start-1])
		if w < 0 {
			w = 1
		}
		if width+w > cols {
			break
		}
		width += w
		start--
	}
	return string(text[start:])
}

func validTransferPath(path string) bool {
	return path != "" && len(path) <= 4096 && !strings.ContainsFunc(path, unicode.IsControl)
}

// Modal local input never reaches the remote PTY, including bracketed paste,
// mouse releases and command-prefix bytes arriving in the same input batch.
func (a *app) promptInput(e localEvent) {
	p := a.prompt
	m := e.input
	if m.Key == "Escape" || m.Modifiers&4 != 0 && strings.EqualFold(m.Key, "c") {
		a.prompt = nil
		a.note("Transfer cancelled")
		return
	}
	if m.Key == "Enter" {
		if !validTransferPath(p.text) {
			return
		}
		if p.source == "" {
			p.source, p.text = p.text, "."
		} else {
			a.prompt = nil
			a.startTransfer(p.upload, p.source, p.text)
		}
	} else if m.Key == "Backspace" {
		runes := []rune(p.text)
		if len(runes) > 0 {
			p.text = string(runes[:len(runes)-1])
		}
	} else if m.Modifiers&4 != 0 && strings.EqualFold(m.Key, "u") {
		p.text = ""
	} else {
		text := ""
		if m.Type == "text" || m.Type == "paste" {
			text = m.Text
		} else if m.Type == "key" && m.Modifiers & ^uint16(1) == 0 && len([]rune(m.Key)) == 1 {
			text = m.Key
		}
		if len(p.text)+len(text) <= 4096 && !strings.ContainsFunc(text, unicode.IsControl) {
			p.text += text
		}
	}
	a.statusDirty = true
}

func (a *app) startTransfer(upload bool, source, destination string) {
	ctx, cancel := context.WithCancel(a.ctx)
	a.transferCancel = cancel
	if upload {
		a.note("Uploading… · Ctrl-] k cancel")
	} else {
		a.note("Downloading… · Ctrl-] k cancel")
	}
	tunnel := a.remote.ssh
	a.transferWG.Add(1)
	go func() {
		defer a.transferWG.Done()
		err := tunnel.transfer(ctx, upload, source, destination)
		select {
		case a.transfers <- transferResult{upload, err}:
		case <-a.ctx.Done():
		}
	}()
}

func (a *app) finishTransfer(result transferResult) {
	a.transferCancel()
	a.transferCancel = nil
	name := "Download"
	if result.upload {
		name = "Upload"
	}
	if result.err != nil {
		a.note(name + ": " + result.err.Error())
	} else {
		a.note(name + " complete")
	}
}

// The status row is independent of service tabs, so standalone SSH views have
// the same buttons. Short labels keep both actions available at eight columns.
func (a *app) transferBar() (string, int, int) {
	if a.remote == nil || a.remote.ssh == nil {
		return "", 0, 0
	}
	if a.screen.cols < 24 {
		return "[U] [D] ", 4, 8
	}
	return "[Upload] [Download] ", 9, 20
}

func (a *app) transferInput(m inputMessage) bool {
	_, split, end := a.transferBar()
	row := a.screen.rows + 1
	if a.tabs != nil {
		row++
	}
	if end == 0 || m.Y != row || (m.Type != "press" && m.Type != "release" && m.Type != "motion") {
		return false
	}
	if m.Type == "press" && m.Button == 1 && m.X >= 1 && m.X < end {
		a.beginTransfer(m.X < split)
	}
	return true
}

func localTransferPath(path string) (string, error) {
	if path == "~" || strings.HasPrefix(path, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		path = filepath.Join(home, strings.TrimPrefix(path, "~"))
	}
	return filepath.Abs(path)
}

// SFTP batch quoting is deliberately separate from shell and terminal quoting.
// OpenSSH escapes glob characters inside quotes itself; escaping them twice
// would instead select a filename containing literal backslashes.
func sftpPath(path string) string {
	var b strings.Builder
	b.WriteByte('"')
	for _, r := range path {
		if strings.ContainsRune("\\\"", r) {
			b.WriteByte('\\')
		}
		b.WriteRune(r)
	}
	b.WriteByte('"')
	return b.String()
}

func transferBatch(upload bool, source, destination string) (string, error) {
	if !validTransferPath(source) || !validTransferPath(destination) {
		return "", fmt.Errorf("paths must be nonempty and contain no control characters")
	}
	local, remote := destination, source
	command := "get"
	if upload {
		local, remote, command = source, destination, "put"
	}
	var err error
	local, err = localTransferPath(local)
	if err != nil {
		return "", err
	}
	if !strings.HasPrefix(remote, "/") {
		if remote == "~" {
			remote = "."
		} else {
			remote = strings.TrimPrefix(remote, "~/")
		}
		remote = "./" + remote
	}
	if upload {
		source, destination = local, remote
	} else {
		source, destination = remote, local
	}
	batch := command + " -R " + sftpPath(source) + " " + sftpPath(destination) + "\n"
	if len(batch) > 8000 {
		return "", fmt.Errorf("combined paths exceed the SFTP command length limit")
	}
	return batch, nil
}

func (t *sshTunnel) transfer(ctx context.Context, upload bool, source, destination string) error {
	batch, err := transferBatch(upload, source, destination)
	if err != nil {
		return err
	}
	startup, cancel := context.WithTimeout(ctx, 15*time.Second)
	err = t.ensure(startup, nil, nil)
	cancel()
	if err != nil {
		return err
	}
	// Multiplex on our authenticated master. If it disappears, fail rather than
	// silently opening a second connection or prompting over the terminal UI.
	cmd := exec.CommandContext(ctx, "sftp", "-b", "-", "-o", "ControlPath="+t.socket("control"),
		"-o", "ControlMaster=no", "-o", "BatchMode=yes", "-o", "ProxyCommand=false", "--", t.target)
	cmd.Stdin = strings.NewReader(batch)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
	cmd.WaitDelay = time.Second
	var diagnostic limitedOutput
	cmd.Stdout, cmd.Stderr = &diagnostic, &diagnostic
	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return fmt.Errorf("SFTP: %w %s", err, strings.TrimSpace(diagnostic.String()))
	}
	return nil
}
