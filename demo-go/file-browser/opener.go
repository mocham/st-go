package main

import (
	"fmt"
	"io"
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
	default:
		if !editorTextFile(path) {
			return ""
		}
		name = "govim"
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

// Content classification covers every text syntax preset, dotfiles/startup
// files, extensionless scripts and plain text, without duplicating a lexer list.
// Keep native/binary formats on their existing routes, including empty fixtures.
func editorTextFile(path string) bool {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".pdf", ".png", ".jpg", ".jpeg", ".gif", ".bmp", ".tga", ".webp", ".ico",
		".zip", ".tar", ".tgz", ".gz", ".bz2", ".xz", ".7z", ".rar", ".zst", ".jar", ".war", ".deb", ".rpm",
		".mp3", ".wav", ".flac", ".ogg", ".m4a", ".aac", ".mp4", ".mkv", ".webm", ".avi", ".mov", ".mpeg", ".mpg",
		".sqlite3", ".sqlite", ".db", ".db3", ".s3db", ".epub", ".doc", ".docx", ".xls", ".xlsx", ".ppt", ".pptx", ".odt", ".ods",
		".exe", ".dll", ".so", ".o", ".a", ".bin", ".wasm", ".pyc", ".class", ".mo", ".ttf", ".otf", ".woff", ".woff2":
		return false
	}
	f, err := os.OpenFile(path, os.O_RDONLY|unix.O_NONBLOCK, 0)
	if err != nil {
		return false
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() {
		return false
	}
	if info.Size() == 0 {
		return true
	}
	if fileCommandAvailable {
		mime := fileMime(path)
		if strings.HasPrefix(mime, "text/") || strings.HasSuffix(mime, "+json") || strings.HasSuffix(mime, "+xml") {
			return true
		}
		switch mime {
		case "application/json", "application/xml", "application/x-shellscript", "application/javascript":
			return true
		}
		if strings.HasPrefix(mime, "image/") || strings.HasPrefix(mime, "audio/") || strings.HasPrefix(mime, "video/") || strings.HasPrefix(mime, "font/") {
			return false
		}
		switch mime {
		case "application/pdf", "application/zip", "application/gzip", "application/x-tar", "application/x-7z-compressed", "application/vnd.sqlite3":
			return false
		}
		// Some entirely textual formats (e.g. PEM or uncommon source languages)
		// have application/* MIME types. Sniff a bounded prefix for those too.
	}
	var sample [8192]byte
	n, err := io.ReadFull(f, sample[:])
	if err != nil && err != io.EOF && err != io.ErrUnexpectedEOF {
		return false
	}
	for _, c := range sample[:n] {
		if c == 0 || c < 0x20 && c != '\t' && c != '\n' && c != '\r' && c != '\f' && c != '\b' {
			return false
		}
	}
	return true
}

// The browser must stop reading the PTY while a document tool owns it. Leave
// the alternate screen rather than nesting it: zat and govim use it themselves.
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
