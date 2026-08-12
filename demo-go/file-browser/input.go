package main

import (
	"regexp"
	"strconv"
	"time"

	"golang.org/x/sys/unix"
)

// input read status codes returned by readByte.
const (
	inData = iota
	inEOF
	inTimeout
)

// input reads single bytes from the terminal with a timeout. It mirrors the
// shell's `read -rsn1 [-t SEC]`: a blocking read of one byte that also lets
// the caller notice resize/signal state between reads.
type input struct {
	fd int
}

func (in *input) readByte(timeout time.Duration) (byte, int) {
	ms := -1
	if timeout > 0 {
		ms = int(timeout / time.Millisecond)
	}
	fds := []unix.PollFd{{Fd: int32(in.fd), Events: unix.POLLIN}}
	for {
		n, err := unix.Poll(fds, ms)
		if err == unix.EINTR {
			continue
		}
		if n <= 0 {
			return 0, inTimeout
		}
		if fds[0].Revents&unix.POLLIN == 0 {
			if fds[0].Revents&unix.POLLHUP != 0 {
				return 0, inEOF
			}
			continue
		}
		var buf [1]byte
		m, err := unix.Read(in.fd, buf[:])
		if err != nil || m == 0 {
			return 0, inEOF
		}
		return buf[0], inData
	}
}

// readSGRPayload reads the remainder of an SGR mouse report (`<Cb;X;YM`).
func (b *Browser) readSGRPayload() string {
	payload := ""
	for len(payload) < 64 {
		c, st := b.in.readByte(50 * time.Millisecond)
		if st != inData {
			break
		}
		payload += string(c)
		if c == 'M' || c == 'm' {
			break
		}
	}
	return payload
}

// readNumericCSI reads a numeric CSI sequence starting with first (e.g. "5~").
func (b *Browser) readNumericCSI(first byte) string {
	rest := string(first)
	for len(rest) < 16 {
		c, st := b.in.readByte(50 * time.Millisecond)
		if st != inData {
			break
		}
		rest += string(c)
		if c == '~' || (c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z') {
			break
		}
	}
	return rest
}

var sgrRe = regexp.MustCompile(`^([0-9]+);([0-9]+);([0-9]+)([Mm])$`)

func atoi(s string) int {
	n, _ := strconv.Atoi(s)
	return n
}

// handleKey dispatches one key byte in the normal mode.
func (b *Browser) handleKey(k byte) {
	switch k {
	case 'q', 'Q':
		b.running = false
	case '/':
		if len(b.archiveStack) > 0 {
			b.status = "Path entry is unavailable inside an archive"
			b.drawStatus()
			b.flush()
			return
		}
		b.resetClick()
		b.pathPrompt()
	case ':':
		b.resetClick()
		b.commandPrompt()
	case '.':
		b.resetClick()
		b.toggleHidden()
	case 'r', 'R':
		b.resetClick()
		b.refreshList()
	case 'o', 'O':
		b.resetClick()
		b.openSelected(0)
	case '[':
		b.resetClick()
		b.changePDFPage(-1)
	case ']':
		b.resetClick()
		b.changePDFPage(1)
	case '\r', '\n':
		b.resetClick()
		b.enterSelected()
	case '\x7f', '\b':
		b.resetClick()
		b.goParent()
	case 0x1b:
		next, st := b.in.readByte(50 * time.Millisecond)
		if st == inData {
			if next == '[' {
				first, st2 := b.in.readByte(50 * time.Millisecond)
				if st2 == inData {
					b.handleCSI(first)
				}
			}
			// ESC followed by another byte is otherwise ignored.
		} else {
			b.running = false
		}
	default:
		// other control bytes are ignored, like the shell's case fallthrough
	}
}

func (b *Browser) handleCSI(first byte) {
	switch first {
	case 'A':
		b.resetClick()
		b.moveSelection(-1)
	case 'B':
		b.resetClick()
		b.moveSelection(1)
	case 'C':
		b.resetClick()
		b.enterSelected()
	case 'D':
		b.resetClick()
		b.goParent()
	case 'H':
		b.resetClick()
		b.selectIndex(0)
	case 'F':
		b.resetClick()
		b.selectIndex(len(b.files) - 1)
	case '<':
		payload := b.readSGRPayload()
		if m := sgrRe.FindStringSubmatch(payload); m != nil {
			b.mouseEvent(atoi(m[1]), atoi(m[2]), atoi(m[3]), m[4][0])
		}
	default:
		if first >= '0' && first <= '9' {
			b.resetClick()
			rest := b.readNumericCSI(first)
			switch rest {
			case "1~", "7~":
				b.selectIndex(0)
			case "4~", "8~":
				b.selectIndex(len(b.files) - 1)
			case "5~":
				b.moveSelection(-b.visible)
			case "6~":
				b.moveSelection(b.visible)
			}
		}
	}
}

// mouseEvent mirrors the SGR report decoding in the shell browser.
func (b *Browser) mouseEvent(cb, x, y int, final byte) {
	button := cb & 3
	if final != 'M' {
		return
	}
	if cb&64 != 0 { // wheel
		b.resetClick()
		if x < b.listX && b.idx >= 0 && b.isPDF(b.files[b.idx]) {
			if button == 0 {
				b.changePDFPage(-1)
			} else if button == 1 {
				b.changePDFPage(1)
			}
		} else {
			if button == 0 {
				b.moveSelection(-3)
			} else if button == 1 {
				b.moveSelection(3)
			}
		}
		return
	}
	if cb&32 != 0 { // motion
		return
	}
	if button == 2 {
		b.resetClick()
		b.goParent()
		return
	}
	if !(button == 0 && x >= b.listX && y >= b.listTop && y <= b.listBottom) {
		return
	}
	index := b.viewTop + y - b.listTop
	if index >= len(b.files) {
		return
	}
	now := nowMS()
	if index == b.lastClickIdx && now-b.lastClickMs <= b.doubleClickMS {
		b.selectIndex(index)
		b.lastClickIdx = -1
		b.doubleClickSelected()
	} else {
		b.selectIndex(index)
		b.lastClickIdx = index
		b.lastClickMs = now
	}
}

// termios helpers

var savedTermios *unix.Termios

// makeRaw puts the terminal into raw mode (like st does for its PTY) so single
// key bytes are delivered immediately.
func (b *Browser) makeRaw() {
	if b.fd < 0 {
		return
	}
	old, err := unix.IoctlGetTermios(b.fd, unix.TCGETS)
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
	if err := unix.IoctlSetTermios(b.fd, unix.TCSETS, &raw); err == nil && savedTermios == nil {
		savedTermios = old
	}
}

func (b *Browser) restoreTermios() {
	if savedTermios != nil && b.fd >= 0 {
		_ = unix.IoctlSetTermios(b.fd, unix.TCSETS, savedTermios)
		savedTermios = nil
	}
}
