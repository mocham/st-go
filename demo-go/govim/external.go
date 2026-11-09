package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"

	"golang.org/x/sys/unix"
)

type externalRunner struct {
	editor   *Editor
	fd       int
	old, raw *unix.Termios
	signals  <-chan os.Signal
}

func (r *externalRunner) run(args []string, dir string) error {
	for _, b := range r.editor.buffers {
		if b.journal != nil {
			if err := b.journal.Sync(); err != nil {
				return err
			}
		}
	}
	fmt.Fprint(os.Stdout, "\x1b[?2026l\x1b[?2004l\x1b[?1002l\x1b[?1000l\x1b[?1006l\x1b[?7h\x1b[?25h\x1b[0m\x1b[?1049l")
	unix.IoctlSetTermios(r.fd, unix.TCSETS, r.old)
	defer func() {
		unix.IoctlSetTermios(r.fd, unix.TCSETS, r.raw)
		fmt.Fprint(os.Stdout, "\x1b[?1049h\x1b[?7l\x1b[?2004h\x1b[?1000h\x1b[?1002h\x1b[?1006h")
		r.editor.dirty, r.editor.fullRedraw = true, true
		if size, err := unix.IoctlGetWinsize(r.fd, unix.TIOCGWINSZ); err == nil {
			r.editor.rows, r.editor.cols = clamp(int(size.Row), 2, 500), clamp(int(size.Col), 8, 1000)
		}
	}()
	command := exec.Command(args[0], args[1:]...)
	command.Dir = dir
	command.Stdin, command.Stdout, command.Stderr = os.Stdin, os.Stdout, os.Stderr
	if err := command.Start(); err != nil {
		return err
	}
	wait := make(chan error, 1)
	go func() { wait <- command.Wait() }()
	for {
		select {
		case err := <-wait:
			return err
		case signal := <-r.signals:
			if signal == syscall.SIGWINCH {
				continue
			}
			command.Process.Signal(signal)
			if signal == syscall.SIGTERM || signal == syscall.SIGHUP {
				r.editor.quit = true
				r.editor.discard = false
			}
		}
	}
}
func (r *externalRunner) shell(command string) error {
	return r.run([]string{"/bin/sh", "-c", command}, "")
}

var mainDirective = regexp.MustCompile(`(?m)^\s*%\s*main\s*=\s*([^\r\n]+)`)
var documentBegin = regexp.MustCompile(`(?m)^[^%\n]*\\begin\s*\{document\}`)

func texMain(path string) (string, error) {
	visited := make(map[string]bool)
	for depth := 0; depth < 16; depth++ {
		if _, err := os.Stat(path); os.IsNotExist(err) {
			path += ".tex"
		}
		b, err := loadBuffer(path)
		if err != nil {
			return "", err
		}
		if !b.meta.Existed {
			return "", fmt.Errorf("TeX file does not exist: %s", path)
		}
		if visited[b.meta.Path] {
			return "", fmt.Errorf("cycle in %%main= directives")
		}
		visited[b.meta.Path] = true
		text := string(b.text)
		if documentBegin.MatchString(text) {
			return b.meta.Path, nil
		}
		match := mainDirective.FindStringSubmatch(text)
		if match == nil {
			return "", fmt.Errorf("no \\begin{document} or %%main= in %s", path)
		}
		path = strings.TrimSpace(match[1])
		if !filepath.IsAbs(path) {
			path = filepath.Join(filepath.Dir(b.meta.Path), path)
		}
	}
	return "", fmt.Errorf("too many %%main= redirects")
}
func (r *externalRunner) tex(path string, force bool) error {
	if path == "" {
		return fmt.Errorf("TeX filename required")
	}
	main, err := texMain(path)
	if err != nil {
		return err
	}
	dir, base := filepath.Dir(main), filepath.Base(main)
	command := []string{"pdflatex"}
	if force {
		command = append(command, "-interaction=nonstopmode")
	}
	command = append(command, base)
	if err := r.run(command, dir); err != nil {
		return err
	}
	if force {
		stem := strings.TrimSuffix(base, filepath.Ext(base))
		if _, err := os.Stat(filepath.Join(dir, stem+".aux")); err == nil {
			if err := r.run([]string{"bibtex", stem}, dir); err != nil {
				return err
			}
		}
		for i := 0; i < 3; i++ {
			if err := r.run(command, dir); err != nil {
				return err
			}
		}
	}
	return nil
}
