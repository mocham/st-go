package main

import (
	"bytes"
	"crypto/sha256"
	_ "embed"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
	"st-go/limits"
	"st-go/ptyutil"
)

//go:embed vimtex.vim
var vimNavigationScript []byte

func makeRaw(fd int) (*unix.Termios, error) {
	old, err := unix.IoctlGetTermios(fd, unix.TCGETS)
	if err != nil {
		return nil, fmt.Errorf("stdin must be an interactive terminal: %w", err)
	}
	raw := *old
	raw.Iflag &^= unix.IGNBRK | unix.BRKINT | unix.PARMRK | unix.ISTRIP | unix.INLCR | unix.IGNCR | unix.ICRNL | unix.IXON
	raw.Oflag &^= unix.OPOST
	raw.Lflag &^= unix.ECHO | unix.ECHONL | unix.ICANON | unix.ISIG | unix.IEXTEN
	raw.Cflag &^= unix.CSIZE | unix.PARENB
	raw.Cflag |= unix.CS8
	raw.Cc[unix.VMIN], raw.Cc[unix.VTIME] = 1, 0
	if err := unix.IoctlSetTermios(fd, unix.TCSETS, &raw); err != nil {
		return nil, err
	}
	return old, nil
}

func negotiateRegions(in *os.File, out io.Writer, width int) ([]byte, error) {
	if err := dcs(out, fmt.Sprintf("region split %d%%; region secondary scroll report; region query", width)); err != nil {
		return nil, err
	}
	var input []byte
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		fds := []unix.PollFd{{Fd: int32(in.Fd()), Events: unix.POLLIN}}
		_, err := unix.Poll(fds, 50)
		if err == unix.EINTR {
			continue
		}
		if err != nil {
			return nil, err
		}
		if fds[0].Revents&(unix.POLLHUP|unix.POLLERR|unix.POLLNVAL) != 0 {
			return nil, io.EOF
		}
		if fds[0].Revents&unix.POLLIN == 0 {
			continue
		}
		var buf [1024]byte
		n, err := unix.Read(int(in.Fd()), buf[:])
		if err != nil {
			return nil, err
		}
		input = append(input, buf[:n]...)
		if len(input) > 4096 {
			return nil, fmt.Errorf("oversized region reply")
		}
		start := bytes.Index(input, []byte("\x1bPregion layout "))
		if start < 0 {
			continue
		}
		end := bytes.Index(input[start:], []byte("\x1b\\"))
		if end < 0 {
			continue
		}
		end += start
		fields := strings.Fields(string(input[start+2 : end]))
		if len(fields) != 12 || fields[2] != "main" || fields[7] != "secondary" {
			return nil, fmt.Errorf("st-go did not establish a main/secondary split")
		}
		for _, i := range []int{5, 6, 10, 11} {
			if n, err := strconv.Atoi(fields[i]); err != nil || n < 1 {
				return nil, fmt.Errorf("invalid region dimensions")
			}
		}
		return append(input[:start], input[end+2:]...), nil
	}
	return nil, fmt.Errorf("no region reply; run vimtex inside the current st-go")
}

func run(o options, in *os.File, out io.Writer) error {
	// Validate readability before changing the terminal or launching Vim.
	if _, err := limits.ReadFile(o.file); err != nil {
		return err
	}
	cache, err := os.MkdirTemp("", "vimtex-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(cache)
	previewPath := filepath.Join(cache, "preview.pdf")
	scriptPath := filepath.Join(cache, "vimtex.vim")
	if err := os.WriteFile(scriptPath, vimNavigationScript, 0600); err != nil {
		return err
	}
	old, err := makeRaw(int(in.Fd()))
	if err != nil {
		return err
	}
	defer func() {
		// CAN also closes an incomplete escape sequence if Vim was terminated.
		fmt.Fprint(out, "\x18\x1b[?2026l\x1b[?1000l\x1b[?1002l\x1b[?1003l\x1b[?1006l\x1b[?2004l\x1b[?25h\x1b[0m\x1b[?1049l")
		_ = dcs(out, "region reset")
		_ = unix.IoctlSetTermios(int(in.Fd()), unix.TCSETS, old)
	}()
	signals := make(chan os.Signal, 8)
	signal.Notify(signals, syscall.SIGWINCH, syscall.SIGINT, syscall.SIGTERM, syscall.SIGHUP)
	defer signal.Stop(signals)
	pendingInput, err := negotiateRegions(in, out, o.previewWidth)
	if err != nil {
		return err
	}
	master, slave, err := ptyutil.Open()
	if err != nil {
		return err
	}
	defer master.Close()
	defer slave.Close()
	masterFD := int(master.Fd())
	resize := func() error {
		ws, err := unix.IoctlGetWinsize(int(in.Fd()), unix.TIOCGWINSZ)
		if err != nil {
			return err
		}
		return unix.IoctlSetWinsize(masterFD, unix.TIOCSWINSZ, ws)
	}
	if err := resize(); err != nil {
		return err
	}
	// The inner PTY has its own ordinary cooked initial mode. Vim takes it raw
	// itself, and :shell / :! commands keep standard terminal semantics.
	args := append([]string{o.vim, "-S", scriptPath}, o.vimArgs...)
	args = append(args, "--", o.file)
	var env []string
	for _, value := range os.Environ() {
		if !strings.HasPrefix(value, "COLUMNS=") && !strings.HasPrefix(value, "LINES=") {
			env = append(env, value)
		}
	}
	child, err := ptyutil.StartDir(slave, args, env, filepath.Dir(o.file))
	if err != nil {
		return fmt.Errorf("start Vim: %w", err)
	}
	wait := make(chan error, 1)
	go func() { wait <- child.Wait() }()
	waited := false
	defer func() {
		if !waited {
			_ = unix.Kill(-child.Process.Pid, unix.SIGHUP)
			master.Close()
			select {
			case <-wait:
			case <-time.After(time.Second):
				_ = unix.Kill(-child.Process.Pid, unix.SIGKILL)
				<-wait
			}
		}
	}()
	if err := unix.SetNonblock(masterFD, true); err != nil {
		return err
	}
	// A blocking write of a large paste can deadlock if Vim fills its output
	// PTY while processing it. Poll both directions with bounded backpressure.
	var cursor previewNavigation
	var navigation navigationInput
	inputQueue := navigation.feed(pendingInput, time.Now(), cursor.navigate)
	results := make(chan renderResult, 1)
	done := make(chan struct{})
	defer close(done)
	clock := refreshClock{gap: o.gap}
	var source *sourceSnapshot
	var boundary outputBoundary
	var nextScan time.Time
	var pending *renderResult
	var exitErr error
	exited := false
	ptyClosed := false
	var lastSourceError string
	for {
		select {
		case sig := <-signals:
			if sig == syscall.SIGWINCH {
				if err := resize(); err != nil {
					return err
				}
			} else {
				_ = unix.Kill(-child.Process.Pid, sig.(syscall.Signal))
				return fmt.Errorf("interrupted by %s", sig)
			}
		default:
		}
		select {
		case exitErr = <-wait:
			waited, exited = true, true
		default:
		}
		if exited && ptyClosed {
			return exitErr
		}
		now := time.Now()
		inputQueue = append(inputQueue, navigation.expire(now)...)
		if !exited && !now.Before(nextScan) {
			nextScan = now.Add(250 * time.Millisecond)
			if source == nil || source.changed() {
				source = loadSource(o.file)
				if source.err != nil {
					if source.err.Error() != lastSourceError {
						limits.Report("vimtex", "%v", source.err)
						lastSourceError = source.err.Error()
					}
				} else {
					lastSourceError = ""
				}
			}
		}
		var viewKey [sha256.Size]byte
		if source != nil {
			viewKey = source.hash
		}
		select {
		case result := <-results:
			clock.busy = false
			if result.err != nil {
				limits.Report("vimtex render", "%v", result.err)
			} else {
				pending = &result
			}
		default:
		}
		if pending != nil && (source == nil || source.err != nil || pending.hash != viewKey) {
			clock.hasAttempt = false // a discarded revision was never displayed
			pending = nil
		}
		if !exited && pending != nil && boundary.safe() && now.Sub(clock.lastPublished) >= o.gap {
			if err := publishPDF(previewPath, pending.pdf); err != nil {
				limits.Report("vimtex publish", "%v", err)
			} else {
				if err := dcs(out, "region secondary open "+quoteDSL(previewPath)+" background "+quoteDSL(o.previewBG)+" fit-width continuous"); err != nil {
					return err
				}
				clock.lastPublished = time.Now()
				clock.published, clock.hasPublished = viewKey, true
				clock.attempted, clock.hasAttempt = viewKey, true
			}
			pending = nil
		}
		if !exited && clock.hasPublished && source != nil && source.err == nil && boundary.safe() {
			if command := cursor.command(source); command != "" {
				if err := dcs(out, command); err != nil {
					return err
				}
				cursor = previewNavigation{}
			}
		}
		if !exited && pending == nil && source != nil && source.err == nil && clock.ready(now, viewKey) {
			clock.start(now, viewKey)
			go func(snapshot *sourceSnapshot) {
				result := renderSource(o.file, snapshot)
				select {
				case results <- result:
				case <-done:
				}
			}(source)
		}
		fds := []unix.PollFd{{Fd: int32(in.Fd())}, {Fd: int32(masterFD), Events: unix.POLLIN}}
		if len(inputQueue) <= limits.ClipboardBytes-8192-128 {
			fds[0].Events = unix.POLLIN
		}
		if len(inputQueue) > 0 {
			fds[1].Events |= unix.POLLOUT
		}
		if ptyClosed {
			fds[1].Fd = -1
		}
		timeout := 50
		if len(navigation.pending) > 0 {
			timeout = 10
		}
		_, err := unix.Poll(fds, timeout)
		if err == unix.EINTR {
			continue
		}
		if err != nil {
			return err
		}
		var buf [8192]byte
		if !exited && !ptyClosed && fds[0].Revents&unix.POLLIN != 0 {
			n, err := unix.Read(int(in.Fd()), buf[:])
			if err == unix.EINTR || err == unix.EAGAIN {
				continue
			}
			if err != nil || n == 0 {
				return io.EOF
			}
			inputQueue = append(inputQueue, navigation.feed(buf[:n], time.Now(), cursor.navigate)...)
		}
		if fds[0].Revents&(unix.POLLHUP|unix.POLLERR|unix.POLLNVAL) != 0 {
			return io.EOF
		}
		if fds[1].Revents&(unix.POLLIN|unix.POLLHUP|unix.POLLERR) != 0 {
			n, readErr := unix.Read(masterFD, buf[:])
			if n > 0 {
				if _, err := out.Write(buf[:n]); err != nil {
					return err
				}
				boundary.observe(buf[:n])
			}
			if (readErr != nil && readErr != unix.EAGAIN && readErr != unix.EINTR) || n == 0 {
				ptyClosed = true
			}
		} else if exited {
			return exitErr
		}
		if !ptyClosed && fds[1].Revents&unix.POLLOUT != 0 && len(inputQueue) > 0 {
			n, err := unix.Write(masterFD, inputQueue[:min(len(inputQueue), 8192)])
			if n > 0 {
				inputQueue = inputQueue[n:]
				if len(inputQueue) == 0 {
					inputQueue = nil
				}
			}
			if err != nil && err != unix.EAGAIN && err != unix.EINTR {
				return err
			}
		}
	}
}
