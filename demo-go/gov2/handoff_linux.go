//go:build linux

package main

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"time"

	"golang.org/x/sys/unix"
)

func handoffReply(payload, id string) (accepted, done bool, err error) {
	fields := strings.Fields(payload)
	if len(fields) < 3 || fields[0] != "native-video" || fields[1] != id {
		return
	}
	switch fields[2] {
	case "accepted":
		accepted = true
	case "done":
		done = true
	case "error":
		err = fmt.Errorf("native DRM handoff failed")
		if len(fields) == 4 {
			if message, decodeErr := base64.RawStdEncoding.DecodeString(fields[3]); decodeErr == nil {
				err = fmt.Errorf("%s", message)
			}
		}
	}
	return
}

func handoff(o options, in *os.File, out io.Writer) (result error) {
	token := os.Getenv("ST_GO_NATIVE_VIDEO_TOKEN")
	if os.Getenv("ST_GO_NATIVE_VIDEO") != "1" || token == "" {
		return fmt.Errorf("DRM handoff unavailable; rebuild st-fb and put gov2 beside it")
	}
	if _, err := hex.DecodeString(token); err != nil || len(token) > 128 {
		return fmt.Errorf("invalid native video capability")
	}
	data, err := json.Marshal(o.request())
	if err != nil {
		return err
	}
	var nonce [16]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return err
	}
	id := hex.EncodeToString(nonce[:])
	fd := int(in.Fd())
	old, err := unix.IoctlGetTermios(fd, unix.TCGETS)
	if err != nil {
		return err
	}
	signals := make(chan os.Signal, 4)
	signal.Notify(signals, unix.SIGINT, unix.SIGTERM, unix.SIGHUP)
	defer signal.Stop(signals)
	raw := rawTermios(*old)
	if err := unix.IoctlSetTermios(fd, unix.TCSETS, &raw); err != nil {
		return err
	}
	defer func() { result = errors.Join(result, unix.IoctlSetTermios(fd, unix.TCSETS, old)) }()
	if _, err := fmt.Fprintf(out, "\x1bPnative-video auth %s %s %s\x1b\\", token, id, base64.RawStdEncoding.EncodeToString(data)); err != nil {
		return err
	}
	deadline := time.Now().Add(5 * time.Second)
	accepted, done := false, false
	var parser inputParser
	for !done && result == nil {
		select {
		case sig := <-signals:
			return fmt.Errorf("interrupted by %s", sig)
		default:
		}
		if !accepted && time.Now().After(deadline) {
			return fmt.Errorf("no gov2 handoff acknowledgement; rebuild st-fb")
		}
		parser.expire(time.Now(), func(string) {})
		fds := []unix.PollFd{{Fd: int32(fd), Events: unix.POLLIN}}
		if _, err := unix.Poll(fds, 100); err != nil && err != unix.EINTR {
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
			parser.feed(buf[:n], time.Now(), func(string) {}, func(payload string) {
				a, d, err := handoffReply(payload, id)
				accepted, done = accepted || a, done || d
				if err != nil {
					result = err
				}
			})
		}
		if fds[0].Revents&(unix.POLLHUP|unix.POLLERR|unix.POLLNVAL) != 0 && !done {
			return io.EOF
		}
	}
	return result
}
