//go:build linux

package main

import (
	"encoding/binary"
	"fmt"
	"testing"
	"time"
	"unsafe"

	"golang.org/x/sys/unix"
)

func probeTouchpad(t *testing.T, multi, direct bool) mouseQuery {
	t.Helper()
	set := func(p unsafe.Pointer, bit int) {
		word := int(unsafe.Sizeof(uintptr(0)))
		at := bit / (word * 8) * word
		b := unsafe.Slice((*byte)(p), at+word)
		if word == 8 {
			binary.NativeEndian.PutUint64(b[at:], binary.NativeEndian.Uint64(b[at:])|1<<uint(bit%64))
		} else {
			binary.NativeEndian.PutUint32(b[at:], binary.NativeEndian.Uint32(b[at:])|1<<uint(bit%32))
		}
	}
	return func(r uintptr, p unsafe.Pointer) error {
		switch r & 0xffff {
		case 0x4522, 0x4518, 0x450a:
		case 0x4521:
			for _, bit := range []int{0x110, btnTouch, btnToolFinger, btnToolDouble} {
				set(p, bit)
			}
		case 0x4523:
			for _, bit := range []int{absX, absY} {
				set(p, bit)
			}
			if multi {
				for _, bit := range []int{absMTSlot, absMTX, absMTY, absMTTracking} {
					set(p, bit)
				}
			}
		case 0x4509:
			if direct {
				set(p, 1)
			}
		case 0x456f:
			*(*absoluteAxis)(p) = absoluteAxis{Max: 1}
		case 0x4540, 0x4541, 0x4575, 0x4576:
			*(*absoluteAxis)(p) = absoluteAxis{Max: 1000, Resolution: 10}
		default:
			t.Errorf("unexpected ioctl %#x", r)
			return unix.EINVAL
		}
		return nil
	}
}

func feedMouse(t *testing.T, m *mouse, emit func(pointerEvent), events ...[3]int32) {
	t.Helper()
	stamp := int(unsafe.Sizeof(unix.Timeval{}))
	data := make([]byte, len(events)*(stamp+8))
	for i, e := range events {
		b := data[i*(stamp+8)+stamp:]
		binary.NativeEndian.PutUint16(b, uint16(e[0]))
		binary.NativeEndian.PutUint16(b[2:], uint16(e[1]))
		binary.NativeEndian.PutUint32(b[4:], uint32(e[2]))
	}
	if err := m.decode(data, emit); err != nil {
		t.Fatal(err)
	}
}

func TestEvdevTouchpadGestures(t *testing.T) {
	for _, multi := range []bool{false, true} {
		t.Run(fmt.Sprint(multi), func(t *testing.T) {
			m, err := configureMouse(-1, "pad", probeTouchpad(t, multi, false))
			if err != nil {
				t.Fatal(err)
			}
			now := time.Unix(100, 0)
			m.touchpad.now = func() time.Time { return now }
			var events []pointerEvent
			emit := func(e pointerEvent) { events = append(events, e) }
			x, y := int32(absX), int32(absY)
			if multi {
				x, y = absMTX, absMTY
			}
			land := func() {
				feedMouse(t, m, emit, [3]int32{evKey, btnTouch, 1}, [3]int32{evKey, btnToolFinger, 1})
				if multi {
					feedMouse(t, m, emit, [3]int32{evAbsolute, absMTSlot, 0}, [3]int32{evAbsolute, absMTTracking, 10})
				}
				feedMouse(t, m, emit, [3]int32{evAbsolute, x, 100}, [3]int32{evAbsolute, y, 100}, [3]int32{evSyn, synReport, 0})
			}
			lift := func() {
				if multi {
					feedMouse(t, m, emit, [3]int32{evAbsolute, absMTTracking, -1})
				}
				feedMouse(t, m, emit, [3]int32{evKey, btnTouch, 0}, [3]int32{evKey, btnToolFinger, 0}, [3]int32{evSyn, synReport, 0})
			}
			land()
			if len(events) != 0 {
				t.Fatal("landing moved cursor", events)
			}
			now = now.Add(40 * time.Millisecond)
			lift()
			if len(events) != 2 || events[0].button != 1 || !events[0].down || events[1].down {
				t.Fatal("tap did not click", events)
			}
			events = nil
			now = now.Add(40 * time.Millisecond)
			land()
			feedMouse(t, m, emit, [3]int32{evAbsolute, x, 180}, [3]int32{evSyn, synReport, 0})
			lift()
			if len(events) != 3 || events[0].button != 1 || !events[0].down || events[1].dx != 80 || events[2].button != 1 || events[2].down {
				t.Fatal("tap-drag failed", events)
			}
			events = nil
			now = now.Add(time.Second)
			land()
			feedMouse(t, m, emit, [3]int32{evKey, btnToolFinger, 0}, [3]int32{evKey, btnToolDouble, 1})
			if multi {
				feedMouse(t, m, emit, [3]int32{evAbsolute, absMTSlot, 1}, [3]int32{evAbsolute, absMTTracking, 11}, [3]int32{evAbsolute, x, 200}, [3]int32{evAbsolute, y, 100})
			}
			feedMouse(t, m, emit, [3]int32{evSyn, synReport, 0})
			if multi {
				feedMouse(t, m, emit, [3]int32{evAbsolute, y, 130}, [3]int32{evAbsolute, absMTSlot, 0})
			}
			feedMouse(t, m, emit, [3]int32{evAbsolute, y, 130}, [3]int32{evSyn, synReport, 0})
			if len(events) != 1 || events[0].wheel != -1 || events[0].dy != 0 {
				t.Fatal("two-finger scroll moved pointer or miscounted", events)
			}
		})
	}
	if _, err := configureMouse(-1, "screen", probeTouchpad(t, true, true)); err == nil {
		t.Fatal("accepted direct touchscreen")
	}
}

func TestEvdevRelativeWheelDropAndDeviceRelease(t *testing.T) {
	m := &mouse{hiWheel: true, queryButtons: func() ([4]bool, error) { return [4]bool{1: true}, nil }}
	var events []pointerEvent
	emit := func(e pointerEvent) { events = append(events, e) }
	feedMouse(t, m, emit, [3]int32{evRelative, relX, 10}, [3]int32{evKey, 0x110, 1}, [3]int32{evRelative, relWheel, 1}, [3]int32{evRelative, relWheelHi, 60}, [3]int32{evSyn, synReport, 0})
	if len(events) != 2 || events[0].dx != 10 || !events[1].down {
		t.Fatal("motion/button ordering or wheel duplicated", events)
	}
	feedMouse(t, m, emit, [3]int32{evRelative, relWheelHi, 60}, [3]int32{evSyn, synReport, 0})
	if events[len(events)-1].wheel != 1 {
		t.Fatal("lost fractional wheel")
	}
	events = nil
	feedMouse(t, m, emit, [3]int32{evSyn, synDropped, 0}, [3]int32{evRelative, relX, 1000}, [3]int32{evSyn, synReport, 0})
	if len(events) != 2 || !events[0].cancel || events[1].down || events[1].button != 1 {
		t.Fatal("drop did not cancel held drag", events)
	}
	events = nil
	feedMouse(t, m, emit, [3]int32{evKey, 0x110, 1}, [3]int32{evKey, 0x110, 0}, [3]int32{evSyn, synReport, 0})
	if len(events) != 0 {
		t.Fatal("held state synthesized click after resync", events)
	}
	feedMouse(t, m, emit, [3]int32{evKey, 0x110, 1}, [3]int32{evSyn, synReport, 0})
	m.release(emit)
	if events[len(events)-1].down || m.buttons[1] {
		t.Fatal("device removal left button held")
	}
	if err := m.decode([]byte{1}, emit); err == nil {
		t.Fatal("accepted partial evdev record")
	}
}
