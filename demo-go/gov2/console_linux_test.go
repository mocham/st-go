//go:build linux

package main

import (
	"errors"
	"reflect"
	"testing"
	"unsafe"

	"golang.org/x/sys/unix"
)

func TestConsoleRollbackAtEveryTakeoverStage(t *testing.T) {
	for fail := 0; fail <= 3; fail++ {
		var events []string
		calls := 0
		operation := func(name string) error {
			events = append(events, name)
			calls++
			if calls == fail {
				return errors.New("injected ioctl failure")
			}
			return nil
		}
		c := &console{}
		c.ops = consoleOps{
			pointer: func(_ uintptr, p unsafe.Pointer) error {
				if (*vtMode)(p).Mode == 1 {
					return operation("process")
				}
				return operation("auto")
			},
			value: func(_ uintptr, value int) error {
				if value == 1 {
					return operation("graphics")
				}
				return operation("text")
			},
			termios: func(termios *unix.Termios) error {
				if termios.Cflag&unix.CS8 != 0 {
					return operation("raw")
				}
				return operation("termios")
			},
		}
		if err := c.enter(); (err != nil) != (fail != 0) {
			t.Fatal(fail, err)
		}
		if err := c.restore(); err != nil {
			t.Fatal(err)
		}
		want := []string{"process", "raw", "graphics", "termios", "text", "auto"}
		if fail == 1 {
			want = []string{"process", "auto"}
		}
		if fail == 2 {
			want = []string{"process", "raw", "termios", "auto"}
		}
		if !reflect.DeepEqual(events, want) {
			t.Fatal(fail, events)
		}
		if err := c.restore(); err != nil || !reflect.DeepEqual(events, want) {
			t.Fatal("cleanup was not idempotent")
		}
	}
}

func TestConsoleRestoresAllStateAfterCleanupError(t *testing.T) {
	calls := 0
	failure := errors.New("ioctl failure")
	op := func() error { calls++; return failure }
	c := &console{changedVT: true, changedKD: true, changedTermios: true, ops: consoleOps{
		pointer: func(uintptr, unsafe.Pointer) error { return op() },
		value:   func(uintptr, int) error { return op() },
		termios: func(*unix.Termios) error { return op() },
	}}
	if err := c.restore(); !errors.Is(err, failure) || calls != 3 {
		t.Fatal("cleanup stopped early", calls, err)
	}
}
