//go:build linux

package main

import (
	"errors"
	"fmt"
	"runtime"
	"unsafe"

	"golang.org/x/sys/unix"
)

const (
	kdSetMode  = 0x4b3a
	kdGetMode  = 0x4b3b
	kbGetMode  = 0x4b44
	vtGetMode  = 0x5601
	vtSetMode  = 0x5602
	vtGetState = 0x5603
	vtRelease  = 0x5605
)

type vtMode struct {
	Mode, Wait             uint8
	Release, Acquire, Free int16
}
type vtState struct{ Active, Signal, State uint16 }
type consoleOps struct {
	pointer func(uintptr, unsafe.Pointer) error
	value   func(uintptr, int) error
	termios func(*unix.Termios) error
}
type console struct {
	oldKD                                int32
	oldVT                                vtMode
	oldTermios                           unix.Termios
	changedVT, changedKD, changedTermios bool
	ops                                  consoleOps
}

func ioctlPointer(fd int, request uintptr, p unsafe.Pointer) error {
	_, _, errno := unix.Syscall(unix.SYS_IOCTL, uintptr(fd), request, uintptr(p))
	runtime.KeepAlive(p)
	if errno != 0 {
		return errno
	}
	return nil
}
func rawTermios(old unix.Termios) unix.Termios {
	old.Iflag &^= unix.IGNBRK | unix.BRKINT | unix.PARMRK | unix.ISTRIP | unix.INLCR | unix.IGNCR | unix.ICRNL | unix.IXON
	old.Oflag &^= unix.OPOST
	old.Lflag &^= unix.ECHO | unix.ECHONL | unix.ICANON | unix.ISIG | unix.IEXTEN
	old.Cflag = (old.Cflag &^ (unix.CSIZE | unix.PARENB)) | unix.CS8
	old.Cc[unix.VMIN], old.Cc[unix.VTIME] = 1, 0
	return old
}
func openConsole(fd int) (*console, error) {
	device, err := unix.IoctlGetInt(fd, unix.TIOCGDEV)
	number := unix.Minor(uint64(uint32(device)))
	if err != nil || unix.Major(uint64(uint32(device))) != 4 || number < 1 || number > 63 {
		return nil, fmt.Errorf("direct DRM needs a real foreground Linux VT; inside st-fb rebuild the host and use its handoff")
	}
	foreground, err := unix.IoctlGetInt(fd, unix.TIOCGPGRP)
	if err != nil || foreground != unix.Getpgrp() {
		return nil, fmt.Errorf("gov2 must own the console foreground process group")
	}
	c := &console{ops: consoleOps{
		pointer: func(r uintptr, p unsafe.Pointer) error { return ioctlPointer(fd, r, p) },
		value:   func(r uintptr, v int) error { return unix.IoctlSetInt(fd, uint(r), v) },
		termios: func(t *unix.Termios) error { return unix.IoctlSetTermios(fd, unix.TCSETS, t) },
	}}
	var state vtState
	if err := c.ops.pointer(vtGetState, unsafe.Pointer(&state)); err != nil {
		return nil, err
	}
	if uint32(state.Active) != number {
		return nil, fmt.Errorf("console must be the active VT")
	}
	var keyboard int32
	if err := c.ops.pointer(kdGetMode, unsafe.Pointer(&c.oldKD)); err != nil {
		return nil, err
	}
	if err := c.ops.pointer(kbGetMode, unsafe.Pointer(&keyboard)); err != nil {
		return nil, err
	}
	if err := c.ops.pointer(vtGetMode, unsafe.Pointer(&c.oldVT)); err != nil {
		return nil, err
	}
	if c.oldKD != 0 || c.oldVT.Mode != 0 || (keyboard != 1 && keyboard != 3) {
		return nil, fmt.Errorf("console is already owned or is not in text/translated-keyboard mode")
	}
	t, err := unix.IoctlGetTermios(fd, unix.TCGETS)
	if err != nil {
		return nil, err
	}
	c.oldTermios = *t
	return c, nil
}
func (c *console) enter() error {
	mode := vtMode{Mode: 1, Release: int16(unix.SIGUSR1), Acquire: int16(unix.SIGUSR2)}
	c.changedVT = true
	if err := c.ops.pointer(vtSetMode, unsafe.Pointer(&mode)); err != nil {
		return err
	}
	raw := rawTermios(c.oldTermios)
	c.changedTermios = true
	if err := c.ops.termios(&raw); err != nil {
		return err
	}
	c.changedKD = true
	return c.ops.value(kdSetMode, 1)
}
func (c *console) restore() error {
	var errs []error
	if c.changedTermios {
		errs = append(errs, c.ops.termios(&c.oldTermios))
		c.changedTermios = false
	}
	if c.changedKD {
		errs = append(errs, c.ops.value(kdSetMode, int(c.oldKD)))
		c.changedKD = false
	}
	if c.changedVT {
		errs = append(errs, c.ops.pointer(vtSetMode, unsafe.Pointer(&c.oldVT)))
		c.changedVT = false
	}
	return errors.Join(errs...)
}
