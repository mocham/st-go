//go:build linux && cgo && ffmpeg_static && !purego

package main

import (
	"errors"
	"fmt"
	"image"
	"io"
	"os"
	"os/signal"
	"time"

	"golang.org/x/sys/unix"
	"st-go/demo-go/gov2/internal/playback"
)

func run(o options, in *os.File, out io.Writer) (result error) {
	if !o.direct && os.Getenv("ST_GO_BACKEND") == "fb" {
		return handoff(o, in, out)
	}
	fd := int(in.Fd())
	c, err := openConsole(fd)
	if err != nil {
		return err
	}
	signals := make(chan os.Signal, 16)
	signal.Notify(signals, unix.SIGUSR1, unix.SIGUSR2, unix.SIGINT, unix.SIGTERM, unix.SIGHUP, unix.SIGQUIT, unix.SIGTSTP)
	defer signal.Stop(signals)
	defer func() { result = errors.Join(result, c.restore()) }()
	if err := c.enter(); err != nil {
		return err
	}
	device, err := openScanout(o.device, uint32(o.connector))
	if err != nil {
		return err
	}
	d := &display{device: device, active: true}
	defer func() { result = errors.Join(result, d.close()) }()
	w, h, err := d.geometry()
	if err != nil {
		return err
	}
	p := playback.StartOutput(o.file, o.volume, o.muted, d)
	defer func() { p.Close(); <-p.Done() }()
	keys := controls{volume: o.volume, muted: o.muted, speed: 1}
	pointer := pointerControls{position: image.Pt(w/2, h-12)}
	var parser inputParser
	quit, active := false, true
	key := func(key string) {
		status := p.Status()
		for i := len(keys.pending) - 1; i >= 0; i-- {
			if keys.pending[i].Action == "seek" {
				status.PositionMS = keys.pending[i].PositionMS
				break
			}
		}
		r, exit := keys.key(key, status)
		quit = quit || exit
		if !quit {
			keys.queue(r)
		}
	}
	mice := &pointerDevices{emit: func(e pointerEvent) { pointer.event(e, w, h, p.Status(), key) }}
	defer mice.close()
	for !quit && result == nil {
		select {
		case sig := <-signals:
			switch sig {
			case unix.SIGUSR1:
				if active {
					mice.close()
					pointer.reset()
					d.movePointer(pointer.position, false)
					p.Suspend(true)
					if err := d.suspend(); err != nil {
						return err
					}
					if err := c.ops.value(vtRelease, 1); err != nil {
						return err
					}
					active = false
					parser = inputParser{}
				}
			case unix.SIGUSR2:
				if err := c.ops.value(vtRelease, 2); err != nil {
					return err
				}
				if !active {
					if err := d.resume(); err != nil {
						return err
					}
					_ = unix.IoctlSetInt(fd, unix.TCFLSH, unix.TCIFLUSH)
					active = true
					p.Suspend(false)
				}
			default:
				return fmt.Errorf("interrupted by %s", sig)
			}
		case <-p.Done():
			status := p.Status()
			if status.Error != "" {
				return fmt.Errorf("%s", status.Error)
			}
			return nil
		default:
		}
		parser.expire(time.Now(), key)
		if active {
			mice.scan()
			if pointer.pendingSeek != nil {
				keys.queue(*pointer.pendingSeek)
				pointer.pendingSeek = nil
			}
			for len(keys.pending) > 0 {
				err := p.Control(keys.pending[0])
				if errors.Is(err, playback.ErrControlBusy) {
					break
				}
				if err != nil {
					return err
				}
				keys.pending = keys.pending[1:]
			}
			d.movePointer(pointer.position, pointer.visible)
			if err := d.refreshUI(p.Status()); err != nil {
				return err
			}
		}
		var fds []unix.PollFd
		if active {
			fds = []unix.PollFd{{Fd: int32(fd), Events: unix.POLLIN}}
			for _, m := range mice.mice {
				fds = append(fds, unix.PollFd{Fd: int32(m.fd), Events: unix.POLLIN})
			}
		}
		if _, err := unix.Poll(fds, 25); err != nil {
			if err == unix.EINTR {
				continue
			}
			return err
		}
		if !active {
			continue
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
			parser.feed(buf[:n], time.Now(), key, func(string) {})
		}
		if fds[0].Revents&(unix.POLLHUP|unix.POLLERR|unix.POLLNVAL) != 0 {
			return io.EOF
		}
		for i := len(mice.mice) - 1; i >= 0; i-- {
			events := fds[i+1].Revents
			failed := events&(unix.POLLHUP|unix.POLLERR|unix.POLLNVAL) != 0
			if !failed && events&unix.POLLIN != 0 {
				failed = mice.mice[i].read(mice.send) != nil
			}
			if failed {
				mice.remove(i)
			}
		}
	}
	return result
}
