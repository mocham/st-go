package main

import (
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

func run(o options, in *os.File, out io.Writer) error {
	fd := int(in.Fd())
	old, err := unix.IoctlGetTermios(fd, unix.TCGETS)
	if err != nil {
		return fmt.Errorf("stdin must be an interactive terminal: %w", err)
	}
	raw := *old
	raw.Iflag &^= unix.IGNBRK | unix.BRKINT | unix.PARMRK | unix.ISTRIP | unix.INLCR | unix.IGNCR | unix.ICRNL | unix.IXON
	raw.Oflag &^= unix.OPOST
	raw.Lflag &^= unix.ECHO | unix.ECHONL | unix.ICANON | unix.ISIG | unix.IEXTEN
	raw.Cflag &^= unix.CSIZE | unix.PARENB
	raw.Cflag |= unix.CS8
	raw.Cc[unix.VMIN], raw.Cc[unix.VTIME] = 1, 0
	if err := unix.IoctlSetTermios(fd, unix.TCSETS, &raw); err != nil {
		return err
	}
	defer func() {
		fmt.Fprint(out, "\x18\x1bPregion reset\x1b\\\x1b[?7h\x1b[?25h\x1b[0m\x1b[?1049l")
		_ = unix.IoctlSetTermios(fd, unix.TCSETS, old)
	}()
	signals := make(chan os.Signal, 8)
	signal.Notify(signals, syscall.SIGWINCH, syscall.SIGINT, syscall.SIGTERM, syscall.SIGHUP)
	defer signal.Stop(signals)
	v := &viewer{options: o, out: out, cols: 80, marks: make(map[string]int)}
	resize := func() {
		if ws, err := unix.IoctlGetWinsize(fd, unix.TIOCGWINSZ); err == nil {
			v.cols = max(1, int(ws.Col))
		}
		v.lastStatus = ""
	}
	resize()
	v.write("\x1b[?1049h\x1b[?25l\x1b[?7l\x1b[2J\x1bPregion stack 1; region query\x1b\\")
	var parser inputParser
	ready := false
	deadline := time.Now().Add(2 * time.Second)
	var nextInfo time.Time
	reply := func(payload string) {
		if !ready && isLayoutReply(payload) {
			ready = true
			v.open(o.file)
			v.navigate("zoom", o.zoom)
			v.navigate("page", o.page)
			resize()
		}
		v.reply(payload)
	}
	key := func(key string) {
		if ready || key == "q" || key == "\x03" {
			v.key(key)
		}
	}
	for !v.quit && v.err == nil {
		select {
		case sig := <-signals:
			if sig == syscall.SIGWINCH {
				resize()
			} else {
				return fmt.Errorf("interrupted by %s", sig)
			}
		default:
		}
		now := time.Now()
		parser.expire(now, key)
		if !ready && now.After(deadline) {
			return fmt.Errorf("no stacked-region reply; run zat inside the updated st-go with PDF support (at least two rows)")
		}
		if ready && !now.Before(nextInfo) {
			v.command("info")
			nextInfo = now.Add(250 * time.Millisecond)
		}
		v.draw()
		fds := []unix.PollFd{{Fd: int32(fd), Events: unix.POLLIN}}
		_, err := unix.Poll(fds, 25)
		if err == unix.EINTR {
			continue
		}
		if err != nil {
			return err
		}
		if fds[0].Revents&unix.POLLIN != 0 {
			var buf [4096]byte
			n, err := unix.Read(fd, buf[:])
			if err == unix.EINTR || err == unix.EAGAIN {
				continue
			}
			if err != nil {
				return err
			}
			if n == 0 {
				return io.EOF
			}
			parser.feed(buf[:n], time.Now(), key, reply)
		}
		if fds[0].Revents&(unix.POLLHUP|unix.POLLERR|unix.POLLNVAL) != 0 {
			return io.EOF
		}
	}
	return v.err
}
