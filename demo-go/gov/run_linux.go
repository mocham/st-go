//go:build linux

package main

import (
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
	"st-go/platform_neutral/api"
	"st-go/tui/video"
)

func run(o options, in *os.File, out io.Writer) error {
	client, err := video.New(out, os.Getenv("ST_GO_NATIVE_VIDEO_TOKEN"))
	if err != nil {
		return err
	}
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
		fmt.Fprint(out, "\x18\x1b[?1000l\x1b[?1006l")
		_ = client.Send(api.VideoRequest{Action: "close"})
		// Consume the ordered close acknowledgement (and preceding status
		// replies) before returning the PTY to the shell.
		drainCloseReply(fd, client)
		fmt.Fprint(out, "\x1b[?7h\x1b[?25h\x1b[0m\x1b[?1049l")
		_ = unix.IoctlSetTermios(fd, unix.TCSETS, old)
	}()
	v := &viewer{out: out, client: client, options: o, status: api.VideoStatus{State: "connecting", Volume: o.volume, Muted: o.muted, Speed: 1}, cols: 80, rows: 24}
	resize := func() {
		if ws, err := unix.IoctlGetWinsize(fd, unix.TIOCGWINSZ); err == nil {
			v.cols = max(1, int(ws.Col))
			v.rows = max(1, int(ws.Row))
		}
		v.lastTop, v.lastBottom, v.lastProgress = "", "", ""
		if v.ready {
			v.send(api.VideoRequest{Action: "resize", Bounds: v.bounds()})
		}
	}
	resize()
	signals := make(chan os.Signal, 8)
	signal.Notify(signals, syscall.SIGWINCH, syscall.SIGINT, syscall.SIGTERM, syscall.SIGHUP)
	defer signal.Stop(signals)
	if _, err := fmt.Fprint(out, "\x1b[?1049h\x1b[?25l\x1b[?7l\x1b[?1000h\x1b[?1006h\x1b[2J"); err != nil {
		return err
	}
	v.send(api.VideoRequest{Action: "query"})
	parser := inputParser{mouse: v.mouse}
	lastReply := time.Now()
	var nextQuery time.Time
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
		parser.expire(now, v.key)
		if now.Sub(lastReply) > 3*time.Second {
			return fmt.Errorf("no video reply; run gov inside the updated full st-fb")
		}
		if v.ready && !now.Before(nextQuery) {
			v.send(api.VideoRequest{Action: "status"})
			nextQuery = now.Add(250 * time.Millisecond)
		}
		v.draw()
		fds := []unix.PollFd{{Fd: int32(fd), Events: unix.POLLIN}}
		if _, err := unix.Poll(fds, 25); err != nil {
			if err == unix.EINTR {
				continue
			}
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
			parser.feed(buf[:n], time.Now(), v.key, func(s string) {
				if v.reply(s) {
					lastReply = time.Now()
				}
			})
		}
		if fds[0].Revents&(unix.POLLHUP|unix.POLLERR|unix.POLLNVAL) != 0 {
			return io.EOF
		}
	}
	return v.err
}

func drainCloseReply(fd int, client *video.Client) {
	deadline := time.Now().Add(250 * time.Millisecond)
	var parser inputParser
	closed := false
	for !closed && time.Now().Before(deadline) {
		fds := []unix.PollFd{{Fd: int32(fd), Events: unix.POLLIN}}
		if _, err := unix.Poll(fds, 20); err != nil && err != unix.EINTR {
			return
		}
		if fds[0].Revents&unix.POLLIN != 0 {
			var buf [4096]byte
			n, err := unix.Read(fd, buf[:])
			if err != nil || n == 0 {
				return
			}
			parser.feed(buf[:n], time.Now(), func(string) {}, func(payload string) {
				if s, ok := client.Reply(payload); ok && (s.State == "closed" || s.State == "error") {
					closed = true
				}
			})
		}
		if fds[0].Revents&(unix.POLLHUP|unix.POLLERR|unix.POLLNVAL) != 0 {
			return
		}
	}
}
