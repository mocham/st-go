//go:build linux

package fb

import (
	"errors"
	"fmt"
	"unsafe"

	"golang.org/x/sys/unix"
)

const (
	kdSetMode    = 0x4b3a
	kdGetMode    = 0x4b3b
	kbGetMode    = 0x4b44
	kbSetMode    = 0x4b45
	kbGetEntry   = 0x4b46
	kbGetLED     = 0x4b64
	kbGetAccents = 0x4bfa
	vtGetMode    = 0x5601
	vtSetMode    = 0x5602
	vtGetState   = 0x5603
	vtRelease    = 0x5605
	vtActivate   = 0x5606
)

type vtMode struct {
	Mode, Wait             uint8
	Release, Acquire, Free int16
}
type vtState struct{ Active, Signal, State uint16 }
type kbEntry struct {
	Table, Index uint8
	Value        uint16
}
type accentEntry struct{ Accent, Base, Result uint32 }
type accentTable struct {
	Count   uint32
	Entries [256]accentEntry
}

// Separate reversible console operations from device opening so startup
// rollback, including failure halfway through takeover, is testable without a VT.
type consoleOps struct {
	pointer func(uintptr, unsafe.Pointer) error
	value   func(uintptr, uintptr) error
	termios func(*unix.Termios) error
}
type console struct {
	fd, number                                      int
	oldKD, oldKB                                    int32
	oldVT                                           vtMode
	oldTermios                                      unix.Termios
	changedVT, changedKD, changedKB, changedTermios bool
	ops                                             consoleOps
}

func openConsole(path string) (*console, error) {
	fd, err := unix.Open(path, unix.O_RDWR|unix.O_NONBLOCK|unix.O_NOCTTY|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	c := &console{fd: fd}
	good := false
	defer func() {
		if !good {
			unix.Close(fd)
		}
	}()
	c.ops = consoleOps{
		pointer: func(r uintptr, p unsafe.Pointer) error { return ioctlPointer(fd, r, p) },
		value:   func(r, v uintptr) error { return ioctlValue(fd, r, v) },
		termios: func(t *unix.Termios) error { return unix.IoctlSetTermios(fd, unix.TCSETS, t) },
	}
	device, err := unix.IoctlGetInt(fd, unix.TIOCGDEV)
	if err != nil {
		return nil, fmt.Errorf("need a real Linux VT, not a PTY: %w", err)
	}
	if unix.Major(uint64(uint32(device))) != 4 {
		return nil, fmt.Errorf("tty is not a Linux virtual console")
	}
	c.number = int(unix.Minor(uint64(uint32(device))))
	if c.number < 1 || c.number > 63 {
		return nil, fmt.Errorf("tty must name a numbered virtual console")
	}
	foreground, err := unix.IoctlGetInt(fd, unix.TIOCGPGRP)
	if err != nil || foreground != unix.Getpgrp() {
		return nil, fmt.Errorf("run in the selected console's foreground process group, not a background job or SSH session")
	}
	active, err := c.active()
	if err != nil || !active {
		return nil, fmt.Errorf("tty must be the active virtual console")
	}
	if err = c.ops.pointer(kdGetMode, unsafe.Pointer(&c.oldKD)); err != nil {
		return nil, err
	}
	if err = c.ops.pointer(kbGetMode, unsafe.Pointer(&c.oldKB)); err != nil {
		return nil, err
	}
	if err = c.ops.pointer(vtGetMode, unsafe.Pointer(&c.oldVT)); err != nil {
		return nil, err
	}
	if c.oldKD != 0 || (c.oldKB != 1 && c.oldKB != 3) || c.oldVT.Mode != 0 {
		return nil, fmt.Errorf("console is already owned or not in text/translated keyboard mode")
	}
	t, err := unix.IoctlGetTermios(fd, unix.TCGETS)
	if err != nil {
		return nil, err
	}
	c.oldTermios = *t
	good = true
	return c, nil
}

func (c *console) active() (bool, error) {
	var state vtState
	if err := c.ops.pointer(vtGetState, unsafe.Pointer(&state)); err != nil {
		return false, err
	}
	return int(state.Active) == c.number, nil
}

func (c *console) enter() error {
	mode := vtMode{Mode: 1, Release: int16(unix.SIGUSR1), Acquire: int16(unix.SIGUSR2)}
	c.changedVT = true
	if err := c.ops.pointer(vtSetMode, unsafe.Pointer(&mode)); err != nil {
		return err
	}
	t := c.oldTermios
	t.Iflag &^= unix.IGNBRK | unix.BRKINT | unix.PARMRK | unix.ISTRIP | unix.INLCR | unix.IGNCR | unix.ICRNL | unix.IXON
	t.Oflag &^= unix.OPOST
	t.Lflag &^= unix.ECHO | unix.ECHONL | unix.ICANON | unix.ISIG | unix.IEXTEN
	t.Cflag = (t.Cflag &^ (unix.CSIZE | unix.PARENB)) | unix.CS8
	t.Cc[unix.VMIN], t.Cc[unix.VTIME] = 1, 0
	c.changedTermios = true
	if err := c.ops.termios(&t); err != nil {
		return err
	}
	c.changedKB = true
	if err := c.ops.value(kbSetMode, 2); err != nil {
		return err
	} // K_MEDIUMRAW
	c.changedKD = true
	return c.ops.value(kdSetMode, 1) // KD_GRAPHICS: stop fbcon overwriting our pixels.
}

func (c *console) restore() error {
	var errs []error
	// Always attempt every restoration, even after one ioctl has failed.
	if c.changedKB {
		errs = append(errs, c.ops.value(kbSetMode, uintptr(c.oldKB)))
		c.changedKB = false
	}
	if c.changedTermios {
		errs = append(errs, c.ops.termios(&c.oldTermios))
		c.changedTermios = false
	}
	if c.changedKD {
		errs = append(errs, c.ops.value(kdSetMode, uintptr(c.oldKD)))
		c.changedKD = false
	}
	if c.changedVT {
		errs = append(errs, c.ops.pointer(vtSetMode, unsafe.Pointer(&c.oldVT)))
		c.changedVT = false
	}
	return errors.Join(errs...)
}

func (c *console) keyboard() (*keyboard, error) {
	k := &keyboard{accents: make(map[[2]rune]rune)}
	for table := range 4 {
		for code := range 256 {
			entry := kbEntry{Table: uint8(table), Index: uint8(code)}
			if err := c.ops.pointer(kbGetEntry, unsafe.Pointer(&entry)); err != nil {
				return nil, fmt.Errorf("read console keymap: %w", err)
			}
			k.maps[table][code] = entry.Value
		}
	}
	var flags int32
	if c.ops.pointer(kbGetLED, unsafe.Pointer(&flags)) == nil {
		k.caps = flags&4 != 0
		k.num = flags&2 != 0
	}
	var accents accentTable
	if c.ops.pointer(kbGetAccents, unsafe.Pointer(&accents)) == nil && accents.Count <= 256 {
		for _, a := range accents.Entries[:accents.Count] {
			k.accents[[2]rune{rune(a.Accent), rune(a.Base)}] = rune(a.Result)
		}
	}
	return k, nil
}
