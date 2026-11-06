package main

import (
	"regexp"
	"strconv"
	"time"

	"golang.org/x/sys/unix"
)

const (
	inData = iota
	inEOF
	inTimeout
)

type input struct{ fd int }

func (in *input) readByte(timeout time.Duration) (byte, int) {
	milliseconds := -1
	if timeout > 0 {
		milliseconds = int(timeout / time.Millisecond)
	}
	fds := []unix.PollFd{{Fd: int32(in.fd), Events: unix.POLLIN}}
	for {
		count, err := unix.Poll(fds, milliseconds)
		if err == unix.EINTR {
			continue
		}
		if count <= 0 {
			return 0, inTimeout
		}
		if fds[0].Revents&unix.POLLIN == 0 {
			if fds[0].Revents&unix.POLLHUP != 0 {
				return 0, inEOF
			}
			continue
		}
		var buffer [1]byte
		read, err := unix.Read(in.fd, buffer[:])
		if err != nil || read == 0 {
			return 0, inEOF
		}
		return buffer[0], inData
	}
}

var sgrMousePattern = regexp.MustCompile(`^([0-9]+);([0-9]+);([0-9]+)([Mm])$`)

func atoi(value string) int {
	number, _ := strconv.Atoi(value)
	return number
}

func (browser *Browser) readCSI(first byte) string {
	sequence := string(first)
	for len(sequence) < 64 {
		character, status := browser.in.readByte(50 * time.Millisecond)
		if status != inData {
			break
		}
		sequence += string(character)
		if character == '~' || character == 'M' || character == 'm' ||
			(character >= 'A' && character <= 'Z') || (character >= 'a' && character <= 'z') {
			break
		}
	}
	return sequence
}

var savedTermios *unix.Termios

func (browser *Browser) makeRaw() {
	if browser.fd < 0 {
		return
	}
	old, err := unix.IoctlGetTermios(browser.fd, unix.TCGETS)
	if err != nil {
		return
	}
	raw := *old
	raw.Iflag &^= unix.IGNBRK | unix.BRKINT | unix.PARMRK | unix.ISTRIP |
		unix.INLCR | unix.IGNCR | unix.ICRNL | unix.IXON
	raw.Oflag &^= unix.OPOST
	raw.Lflag &^= unix.ECHO | unix.ECHONL | unix.ICANON | unix.ISIG | unix.IEXTEN
	raw.Cflag &^= unix.CSIZE | unix.PARENB
	raw.Cflag |= unix.CS8
	raw.Cc[unix.VMIN] = 0
	raw.Cc[unix.VTIME] = 0
	if err := unix.IoctlSetTermios(browser.fd, unix.TCSETS, &raw); err == nil && savedTermios == nil {
		savedTermios = old
	}
}

func (browser *Browser) restoreTermios() {
	if savedTermios != nil && browser.fd >= 0 {
		_ = unix.IoctlSetTermios(browser.fd, unix.TCSETS, savedTermios)
		savedTermios = nil
	}
}
