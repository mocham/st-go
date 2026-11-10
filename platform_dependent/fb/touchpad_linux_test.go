//go:build linux

package fb

import (
	"encoding/binary"
	"errors"
	"fmt"
	"image"
	"testing"
	"time"
	"unsafe"

	"golang.org/x/sys/unix"
)

func touchpadProbe(t *testing.T, multitouch, direct bool) (mouseQuery, *bool) {
	t.Helper()
	touching := false
	set := func(p unsafe.Pointer, bit int) {
		wordBytes := int(unsafe.Sizeof(uintptr(0)))
		at := bit / (wordBytes * 8) * wordBytes
		bytes := unsafe.Slice((*byte)(p), at+wordBytes)
		if wordBytes == 8 {
			binary.NativeEndian.PutUint64(bytes[at:], binary.NativeEndian.Uint64(bytes[at:])|1<<uint(bit%64))
		} else {
			binary.NativeEndian.PutUint32(bytes[at:], binary.NativeEndian.Uint32(bytes[at:])|1<<uint(bit%32))
		}
	}
	query := func(request uintptr, p unsafe.Pointer) error {
		switch request & 0xffff {
		case 0x4501:
			*(*uint32)(p) = 0x10001
		case 0x4522: // EVIOCGBIT(EV_REL)
		case 0x4521: // EVIOCGBIT(EV_KEY)
			for _, bit := range []int{0x110, btnTouch, btnToolFinger, btnToolDouble} {
				set(p, bit)
			}
		case 0x4523: // EVIOCGBIT(EV_ABS)
			for _, bit := range []int{absX, absY} {
				set(p, bit)
			}
			if multitouch {
				for _, bit := range []int{absMTSlot, absMTX, absMTY, absMTTracking} {
					set(p, bit)
				}
			}
		case 0x4509: // EVIOCGPROP
			if direct {
				set(p, 1)
			} else {
				set(p, 0)
			}
		case 0x456f: // EVIOCGABS(ABS_MT_SLOT)
			*(*absoluteAxis)(p) = absoluteAxis{Max: 1}
		case 0x4540, 0x4541, 0x4575, 0x4576:
			*(*absoluteAxis)(p) = absoluteAxis{Min: 0, Max: 1000, Resolution: 10}
		case 0x4518: // EVIOCGKEY
			if touching {
				set(p, btnTouch)
			}
		case 0x450a: // EVIOCGMTSLOTS; initial coordinate values are zero
		default:
			t.Errorf("unexpected touchpad ioctl %#x", request)
			return unix.EINVAL
		}
		return nil
	}
	return query, &touching
}

func testTouchpad(t *testing.T, multi bool) (*mouse, *bool) {
	t.Helper()
	query, touching := touchpadProbe(t, multi, false)
	m, err := configureMouse(-1, "test-touchpad", query)
	if err != nil || m.touchpad == nil || m.touchpad.multitouch != multi {
		t.Fatal("touchpad discovery failed", m, err)
	}
	return m, touching
}

func touchEvents(t *testing.T, m *mouse, d *Driver, events ...[3]int32) {
	t.Helper()
	if err := m.decode(d, encodeMouseEvents(events...)); err != nil {
		t.Fatal(err)
	}
}

func TestTouchpadDiscoveryAndTouchscreenRejection(t *testing.T) {
	for _, multi := range []bool{false, true} {
		testTouchpad(t, multi)
	}
	query, _ := touchpadProbe(t, true, true)
	if _, err := configureMouse(-1, "touchscreen", query); !errors.Is(err, errNotRelativeMouse) {
		t.Fatal("accepted direct touchscreen", err)
	}
	query, _ = touchpadProbe(t, false, false)
	if _, err := configureMouse(-1, "invalid-range", func(req uintptr, p unsafe.Pointer) error {
		if req&0xffff == 0x4540 {
			*(*absoluteAxis)(p) = absoluteAxis{}
			return nil
		}
		return query(req, p)
	}); err == nil {
		t.Fatal("accepted unusable absolute axis")
	}
	query, _ = touchpadProbe(t, true, false)
	if _, err := configureMouse(-1, "too-many-slots", func(req uintptr, p unsafe.Pointer) error {
		if req&0xffff == 0x456f {
			*(*absoluteAxis)(p) = absoluteAxis{Max: 100000}
			return nil
		}
		return query(req, p)
	}); err == nil {
		t.Fatal("allocated unbounded multitouch slots")
	}
}

func TestTouchpadRelativeMotionLandingAndLiftDoNotJump(t *testing.T) {
	for _, multi := range []bool{false, true} {
		t.Run(fmt.Sprint(multi), func(t *testing.T) {
			d := memoryDriver(t, 200, 200)
			d.pointer = image.Pt(100, 100)
			var events []string
			mouseCallbacks(d, &events)
			m, _ := testTouchpad(t, multi)
			now := time.Unix(100, 0)
			m.touchpad.now = func() time.Time { return now }
			start := [][3]int32{{evKey, btnTouch, 1}, {evKey, btnToolFinger, 1}}
			x, y := int32(absX), int32(absY)
			if multi {
				start = append(start, [3]int32{evAbsolute, absMTSlot, 0}, [3]int32{evAbsolute, absMTTracking, 10})
				x, y = absMTX, absMTY
			}
			start = append(start, [3]int32{evAbsolute, x, 200}, [3]int32{evAbsolute, y, 300}, [3]int32{evSyn, synReport, 0})
			touchEvents(t, m, d, start...)
			if len(events) != 0 || d.pointer != image.Pt(100, 100) {
				t.Fatal("finger landing moved pointer", events)
			}
			touchEvents(t, m, d, [3]int32{evAbsolute, x, 204}, [3]int32{evAbsolute, y, 297}, [3]int32{evSyn, synReport, 0})
			if fmt.Sprint(events) != "[move 104 97 0]" {
				t.Fatal("absolute coordinates were not relative motion", events)
			}
			events = nil
			now = now.Add(time.Second)
			touchEvents(t, m, d, [3]int32{evKey, btnTouch, 0}, [3]int32{evKey, btnToolFinger, 0}, [3]int32{evSyn, synReport, 0})
			if len(events) != 0 {
				t.Fatal("finger lift generated motion/click", events)
			}
		})
	}
}

func TestTouchpadTapPhysicalDragAndTwoFingerScroll(t *testing.T) {
	d := memoryDriver(t, 200, 200)
	d.pointer = image.Pt(100, 100)
	var events []string
	mouseCallbacks(d, &events)
	m, _ := testTouchpad(t, false)
	now := time.Unix(100, 0)
	m.touchpad.now = func() time.Time { return now }
	feed := func(e ...[3]int32) { touchEvents(t, m, d, append(e, [3]int32{evSyn, synReport, 0})...) }
	feed([3]int32{evKey, btnTouch, 1}, [3]int32{evKey, btnToolFinger, 1}, [3]int32{evAbsolute, absX, 200}, [3]int32{evAbsolute, absY, 300})
	now = now.Add(100 * time.Millisecond)
	feed([3]int32{evKey, btnTouch, 0}, [3]int32{evKey, btnToolFinger, 0})
	if fmt.Sprint(events) != "[press 1 100 100 0 release 1 100 100 0]" {
		t.Fatal("tap-to-click failed", events)
	}
	events = nil
	feed([3]int32{evKey, btnTouch, 1}, [3]int32{evKey, btnToolFinger, 1}, [3]int32{evKey, 0x110, 1})
	feed([3]int32{evAbsolute, absX, 210})
	feed([3]int32{evKey, 0x110, 0}, [3]int32{evKey, btnTouch, 0}, [3]int32{evKey, btnToolFinger, 0})
	if fmt.Sprint(events) != "[press 1 100 100 0 move 110 100 0 release 1 110 100 0]" {
		t.Fatal("physical drag or tap suppression failed", events)
	}
	events = nil
	feed([3]int32{evKey, btnTouch, 1}, [3]int32{evKey, btnToolDouble, 1})
	feed([3]int32{evAbsolute, absY, 330})
	feed([3]int32{evAbsolute, absX, 240})
	feed([3]int32{evKey, btnTouch, 0}, [3]int32{evKey, btnToolDouble, 0})
	if fmt.Sprint(events) != "[press 5 110 100 0 release 5 110 100 0 press 7 110 100 0 release 7 110 100 0]" {
		t.Fatal("two-finger scroll moved pointer or used wrong wheel directions", events)
	}
}

func TestTouchpadDoubleTapAndTapDrag(t *testing.T) {
	for _, multi := range []bool{false, true} {
		t.Run(fmt.Sprint(multi), func(t *testing.T) {
			d := memoryDriver(t, 300, 200)
			d.pointer = image.Pt(100, 100)
			var events []string
			mouseCallbacks(d, &events)
			m, _ := testTouchpad(t, multi)
			now := time.Unix(100, 0)
			m.touchpad.now = func() time.Time { return now }
			feed := func(e ...[3]int32) { touchEvents(t, m, d, append(e, [3]int32{evSyn, synReport, 0})...) }
			tracking := int32(10)
			land := func(x, y int32) {
				events := [][3]int32{{evKey, btnTouch, 1}, {evKey, btnToolFinger, 1}}
				if multi {
					events = append(events, [3]int32{evAbsolute, absMTSlot, 0}, [3]int32{evAbsolute, absMTTracking, tracking}, [3]int32{evAbsolute, absMTX, x}, [3]int32{evAbsolute, absMTY, y})
					tracking++
				} else {
					events = append(events, [3]int32{evAbsolute, absX, x}, [3]int32{evAbsolute, absY, y})
				}
				feed(events...)
			}
			move := func(x, y int32) {
				if multi {
					feed([3]int32{evAbsolute, absMTSlot, 0}, [3]int32{evAbsolute, absMTX, x}, [3]int32{evAbsolute, absMTY, y})
				} else {
					feed([3]int32{evAbsolute, absX, x}, [3]int32{evAbsolute, absY, y})
				}
			}
			lift := func() {
				events := make([][3]int32, 0, 3)
				if multi {
					events = append(events, [3]int32{evAbsolute, absMTSlot, 0}, [3]int32{evAbsolute, absMTTracking, -1})
				}
				events = append(events, [3]int32{evKey, btnTouch, 0}, [3]int32{evKey, btnToolFinger, 0})
				feed(events...)
			}

			// The second immediate tap presses on landing and releases on lift,
			// producing the second click rather than another delayed tap.
			land(200, 300)
			now = now.Add(100 * time.Millisecond)
			lift()
			// Real devices can emit an idle SYN packet between contacts. It must
			// not erase the completed tap, and the next landing need not be at
			// the same absolute pad coordinate.
			now = now.Add(50 * time.Millisecond)
			feed()
			now = now.Add(50 * time.Millisecond)
			land(800, 700)
			if !m.buttons[1] || fmt.Sprint(events) != "[press 1 100 100 0 release 1 100 100 0 press 1 100 100 0]" {
				t.Fatal("second tap did not press on landing", events, m.buttons[1])
			}
			now = now.Add(100 * time.Millisecond)
			lift()
			if m.buttons[1] || fmt.Sprint(events) != "[press 1 100 100 0 release 1 100 100 0 press 1 100 100 0 release 1 100 100 0]" {
				t.Fatal("double tap did not produce two clicks", events, m.buttons[1])
			}

			// A later tap followed by an immediate second landing holds button 1
			// throughout pointer motion, enabling selection and drag gestures.
			events = nil
			now = now.Add(time.Second)
			land(300, 300)
			now = now.Add(100 * time.Millisecond)
			lift()
			now = now.Add(50 * time.Millisecond)
			feed()
			now = now.Add(50 * time.Millisecond)
			land(700, 500)
			move(730, 500)
			if !m.buttons[1] || fmt.Sprint(events) != "[press 1 100 100 0 release 1 100 100 0 press 1 100 100 0 move 130 100 0]" {
				t.Fatal("tap-and-drag did not hold button through motion", events, m.buttons[1])
			}
			now = now.Add(100 * time.Millisecond)
			lift()
			if m.buttons[1] || fmt.Sprint(events) != "[press 1 100 100 0 release 1 100 100 0 press 1 100 100 0 move 130 100 0 release 1 130 100 0]" {
				t.Fatal("tap-and-drag did not release on lift", events, m.buttons[1])
			}
		})
	}
}

func TestTouchpadDroppedPacketReleasesTapDrag(t *testing.T) {
	d := memoryDriver(t, 200, 200)
	var events []string
	mouseCallbacks(d, &events)
	m, touching := testTouchpad(t, false)
	now := time.Unix(100, 0)
	m.touchpad.now = func() time.Time { return now }
	feed := func(e ...[3]int32) { touchEvents(t, m, d, append(e, [3]int32{evSyn, synReport, 0})...) }
	land := func() {
		feed([3]int32{evKey, btnTouch, 1}, [3]int32{evKey, btnToolFinger, 1}, [3]int32{evAbsolute, absX, 200}, [3]int32{evAbsolute, absY, 300})
	}
	lift := func() { feed([3]int32{evKey, btnTouch, 0}, [3]int32{evKey, btnToolFinger, 0}) }
	land()
	now = now.Add(100 * time.Millisecond)
	lift()
	now = now.Add(100 * time.Millisecond)
	land()
	if !m.buttons[1] {
		t.Fatal("tap drag did not hold button before dropped packet")
	}
	events = nil
	*touching = true
	touchEvents(t, m, d, [3]int32{evSyn, synDropped, 0}, [3]int32{evSyn, synReport, 0})
	if m.buttons[1] || fmt.Sprint(events) != "[release 1 0 0 0]" {
		t.Fatal("dropped packet left tap drag held", events, m.buttons[1])
	}
	events = nil
	*touching = false
	lift()
	if len(events) != 0 {
		t.Fatal("resynchronized finger lift generated a phantom click", events)
	}
}

func TestMultitouchSlotsCentroidContactChangesAndNoDoubleCounting(t *testing.T) {
	d := memoryDriver(t, 200, 200)
	d.pointer = image.Pt(100, 100)
	var events []string
	mouseCallbacks(d, &events)
	m, _ := testTouchpad(t, true)
	feed := func(e ...[3]int32) { touchEvents(t, m, d, append(e, [3]int32{evSyn, synReport, 0})...) }
	feed([3]int32{evKey, btnTouch, 1}, [3]int32{evKey, btnToolDouble, 1}, [3]int32{evAbsolute, absMTSlot, 0}, [3]int32{evAbsolute, absMTTracking, 10}, [3]int32{evAbsolute, absMTX, 200}, [3]int32{evAbsolute, absMTY, 300}, [3]int32{evAbsolute, absMTSlot, 1}, [3]int32{evAbsolute, absMTTracking, 11}, [3]int32{evAbsolute, absMTX, 400}, [3]int32{evAbsolute, absMTY, 300})
	feed([3]int32{evAbsolute, absMTSlot, 0}, [3]int32{evAbsolute, absMTY, 330}, [3]int32{evAbsolute, absMTSlot, 1}, [3]int32{evAbsolute, absMTY, 330}, [3]int32{evAbsolute, absX, 999}, [3]int32{evAbsolute, absY, 999})
	if fmt.Sprint(events) != "[press 5 100 100 0 release 5 100 100 0]" {
		t.Fatal("multitouch centroid/emulated ABS stream mishandled", events)
	}
	events = nil
	feed([3]int32{evAbsolute, absMTTracking, -1}, [3]int32{evKey, btnToolDouble, 0}, [3]int32{evKey, btnToolFinger, 1})
	feed([3]int32{evAbsolute, absMTSlot, 0}, [3]int32{evAbsolute, absMTX, 205})
	if fmt.Sprint(events) != "[move 105 100 0]" {
		t.Fatal("lifting a finger jumped pointer", events)
	}
	events = nil
	feed([3]int32{evAbsolute, absMTTracking, 20}, [3]int32{evAbsolute, absMTX, 800}, [3]int32{evAbsolute, absMTY, 800})
	if len(events) != 0 {
		t.Fatal("replacing a contact jumped pointer", events)
	}
}

func TestTouchpadDroppedPacketsCancelGesturesAndResynchronizeButtons(t *testing.T) {
	d := memoryDriver(t, 200, 200)
	var events []string
	mouseCallbacks(d, &events)
	m, touching := testTouchpad(t, false)
	feed := func(e ...[3]int32) { touchEvents(t, m, d, append(e, [3]int32{evSyn, synReport, 0})...) }
	feed([3]int32{evKey, btnTouch, 1}, [3]int32{evKey, 0x110, 1}, [3]int32{evAbsolute, absX, 200}, [3]int32{evAbsolute, absY, 300})
	events = nil
	*touching = true
	feed([3]int32{evSyn, synDropped, 0}, [3]int32{evAbsolute, absX, 999})
	if fmt.Sprint(events) != "[release 1 0 0 0]" {
		t.Fatal("dropped packet left physical button held", events)
	}
	events = nil
	feed([3]int32{evKey, btnTouch, 1}, [3]int32{evAbsolute, absX, 900}, [3]int32{evAbsolute, absY, 900})
	feed([3]int32{evKey, btnTouch, 0})
	if len(events) != 0 {
		t.Fatal("dropped gesture produced a phantom movement/tap", events)
	}
	feed([3]int32{evKey, btnTouch, 1}, [3]int32{evAbsolute, absX, 200}, [3]int32{evAbsolute, absY, 300})
	feed([3]int32{evAbsolute, absX, 205})
	if fmt.Sprint(events) != "[move 5 0 0]" {
		t.Fatal("touchpad did not recover after lifting fingers", events)
	}
}

func TestTouchpadFractionalMotionAndScrollAccumulate(t *testing.T) {
	axis := absoluteAxis{Min: 0, Max: 1000, Resolution: 100}
	var remainder, total int64
	for range 10 {
		total += touchScale(1, axis, 10, 1, &remainder)
	}
	if total != 1 || remainder != 0 {
		t.Fatal("fractional pointer motion lost", total, remainder)
	}
	remainder, total = 0, 0
	for range 30 {
		total += touchScale(1, absoluteAxis{Resolution: 10}, 120, 3, &remainder)
	}
	if total != 120 || remainder != 0 {
		t.Fatal("fractional scrolling lost", total, remainder)
	}
	remainder = 0
	if value := touchScale(30, absoluteAxis{Max: 1000}, 120, 3, &remainder); value != 120 {
		t.Fatal("missing-resolution fallback changed scroll distance", value)
	}
}

func TestTouchpadResetSeedsAxesWhichEvdevDoesNotRepeat(t *testing.T) {
	for _, multi := range []bool{false, true} {
		t.Run(fmt.Sprint(multi), func(t *testing.T) {
			d := memoryDriver(t, 200, 200)
			var events []string
			mouseCallbacks(d, &events)
			query, _ := touchpadProbe(t, multi, false)
			position := [2]int32{200, 300}
			m, err := configureMouse(-1, "touchpad", func(request uintptr, p unsafe.Pointer) error {
				if err := query(request, p); err != nil {
					return err
				}
				switch request & 0xffff {
				case 0x4540:
					(*absoluteAxis)(p).Value = position[0]
				case 0x4541:
					(*absoluteAxis)(p).Value = position[1]
				case 0x450a:
					values := unsafe.Slice((*int32)(p), int((request>>16)&0x3fff)/4)
					axis := 0
					if values[0] == absMTY {
						axis = 1
					}
					for i := 1; i < len(values); i++ {
						values[i] = position[axis]
					}
				}
				return nil
			})
			if err != nil {
				t.Fatal(err)
			}
			// Another VT changes coordinates, then acquire drains queued records.
			position = [2]int32{700, 800}
			m.reset()
			x, y := int32(absX), int32(absY)
			packet := [][3]int32{{evKey, btnTouch, 1}, {evKey, btnToolFinger, 1}}
			if multi {
				x, y = absMTX, absMTY
				packet = append(packet, [3]int32{evAbsolute, absMTTracking, 42})
			}
			// Y stays 800: the kernel need not repeat it for the new contact.
			packet = append(packet, [3]int32{evAbsolute, x, 710}, [3]int32{evSyn, synReport, 0})
			touchEvents(t, m, d, packet...)
			touchEvents(t, m, d, [3]int32{evAbsolute, y, 805}, [3]int32{evSyn, synReport, 0})
			if fmt.Sprint(events) != "[move 0 5 0]" {
				t.Fatal("stale unchanged axis caused a post-VT pointer jump", events)
			}
		})
	}
}
