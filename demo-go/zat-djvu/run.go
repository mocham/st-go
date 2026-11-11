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

func run(options options, input *os.File, output io.Writer) error {
	backend, pages, err := newDjVuBackend(options.file, options.backgroundRGB)
	if err != nil {
		return err
	}
	defer backend.close()

	fd := int(input.Fd())
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
		fmt.Fprint(output, "\x18\x1b[?1000l\x1b[?1006l\x1bPregion reset\x1b\\\x1b[?7h\x1b[?25h\x1b[0m\x1b[?1049l")
		_ = unix.IoctlSetTermios(fd, unix.TCSETS, old)
	}()

	viewer := newViewer(options, backend, pages, output)
	resize := func() {
		if size, err := unix.IoctlGetWinsize(fd, unix.TIOCGWINSZ); err == nil {
			viewer.cols = max(1, int(size.Col))
			viewer.rows = max(1, int(size.Row))
		}
		viewer.lastStatus = ""
		viewer.reflowText()
		viewer.panelDirty = true
	}
	resize()

	signals := make(chan os.Signal, 8)
	signal.Notify(signals, syscall.SIGWINCH, syscall.SIGINT, syscall.SIGTERM, syscall.SIGHUP)
	defer signal.Stop(signals)
	viewer.write("\x1b[?1049h\x1b[?25l\x1b[?7l\x1b[2J\x1bPregion stack 1; region secondary scroll edges; region query\x1b\\")

	var parser inputParser
	ready := false
	deadline := time.Now().Add(2 * time.Second)
	var nextInfo time.Time
	for !viewer.quit && viewer.err == nil {
		select {
		case signal := <-signals:
			if signal == syscall.SIGWINCH {
				resize()
			} else {
				return fmt.Errorf("interrupted by %s", signal)
			}
		default:
		}
		for {
			select {
			case result := <-backend.results:
				viewer.backendReply(result)
			default:
				goto resultsDrained
			}
		}
	resultsDrained:
		now := time.Now()
		parser.expire(now, viewer.key)
		if !ready && now.After(deadline) {
			return fmt.Errorf("no image-region reply; run zat-djvu inside the updated st-go (at least two rows)")
		}
		if ready && viewer.imagePath != "" && !now.Before(nextInfo) {
			viewer.command("info")
			nextInfo = now.Add(250 * time.Millisecond)
		}
		viewer.draw()

		fds := []unix.PollFd{{Fd: int32(fd), Events: unix.POLLIN}}
		_, err := unix.Poll(fds, 25)
		if err == unix.EINTR {
			continue
		}
		if err != nil {
			return err
		}
		if fds[0].Revents&unix.POLLIN != 0 {
			var buffer [4096]byte
			n, err := unix.Read(fd, buffer[:])
			if err == unix.EINTR || err == unix.EAGAIN {
				continue
			}
			if err != nil {
				return err
			}
			if n == 0 {
				return io.EOF
			}
			parser.feed(buffer[:n], time.Now(), func(key string) {
				if ready || key == "q" || key == "\x03" {
					viewer.key(key)
				}
			}, func(payload string) {
				if !ready && isLayoutReply(payload) {
					ready = true
					viewer.requestRender(options.page)
					resize()
				}
				viewer.reply(payload)
			})
		}
		if fds[0].Revents&(unix.POLLHUP|unix.POLLERR|unix.POLLNVAL) != 0 {
			return io.EOF
		}
	}
	return viewer.err
}
