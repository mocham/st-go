//go:build linux

package fb

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"image"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"
	"unsafe"

	"golang.org/x/sys/unix"
	"st-go/platform_neutral/api"
)

func testKeyboard() *keyboard {
	k := &keyboard{accents: map[[2]rune]rune{{'\'', 'a'}: 'á'}}
	for table := range 4 {
		for code := range 256 {
			k.maps[table][code] = 0x0200
		}
	}
	for table := range 4 {
		for code, symbol := range map[int]uint16{1: 27, 14: 127, 15: 9, 28: 0x0201, 29: 0x0702, 42: 0x0700, 54: 0x0700, 56: 0x0703, 58: 0x0207, 59: 0x0100, 60: 0x0101, 69: 0x0208, 97: 0x0702, 100: 0x0701, 103: 0x0603, 125: 0x0700, 126: 0x0700} {
			k.maps[table][code] = symbol
		}
		k.maps[table][30] = 0x0b61
		k.maps[table][46] = 0x0b63
		if table&1 != 0 {
			k.maps[table][30] = 0x0b41
			k.maps[table][46] = 0x0b43
		}
	}
	k.maps[0][40] = 0x0401 // dead acute
	k.maps[0][127] = uint16('中') ^ 0xf000
	k.maps[2][30] = uint16('世') ^ 0xf000
	k.maps[3][30] = uint16('界') ^ 0xf000
	return k
}

func TestMediumRawKeyboardModifiersUnicodeComposeAndVT(t *testing.T) {
	k := testKeyboard()
	var keys []string
	var text bytes.Buffer
	var switched []int
	closed := false
	cb := api.Callbacks{Closed: func() bool { return closed }, KeyPress: func(key uint, mods uint16) { keys = append(keys, fmt.Sprintf("%x/%d", key, mods)) }, Text: func(s string) { text.WriteString(s) }}
	feed := func(data ...byte) {
		k.feed(data, cb, func(n int) { switched = append(switched, n) }, func() { closed = true })
	}
	feed(30, 30|128, 42, 30, 30|128, 42|128)             // a, Shift-A
	feed(58, 58, 58|128, 30, 30|128)                     // repeated Caps press toggles only once
	feed(42, 30, 30|128, 42|128, 58, 58|128)             // Shift+Caps=a, Caps off
	feed(29, 46, 46|128, 29|128, 42, 15, 15|128, 42|128) // Ctrl-C, Shift-Tab
	k.maps[1][59] = 0x010c                               // kernel Shift-F1 = F13; neutral host needs F1+Shift
	feed(42, 59, 59|128, 42|128)
	feed(40, 40|128, 30, 30|128)   // dead acute + a
	feed(127, 255)                 // a single-byte keycode 127 with Unicode mapping
	feed(100, 30, 30|128, 100|128) // AltGr Unicode, without spurious Alt/Ctrl bytes
	want := []string{"61/0", "41/1", "41/0", "61/1", "63/4", "fe20/1", "ffbe/1"}
	if fmt.Sprint(keys) != fmt.Sprint(want) || text.String() != "á中世" {
		t.Fatalf("keys=%v text=%q", keys, text.String())
	}
	// Extended keycode split over reads: code 130 is decoded, never typed as bytes.
	k.maps[0][130] = 'z'
	feed(0, 0x81)
	feed(0x82, 0x80, 0x81, 0x82)
	if keys[len(keys)-1] != "7a/0" || k.pressed[130] {
		t.Fatal("extended keycode framing failed")
	}
	feed(29, 56, 60, 60, 60|128) // Ctrl-Alt-F2 switches once despite repeat
	if fmt.Sprint(switched) != "[2]" {
		t.Fatalf("VT switches=%v", switched)
	}
	feed(14, 30) // emergency close must not dispatch following queued input
	if !closed || keys[len(keys)-1] != "7a/0" {
		t.Fatal("emergency exit leaked queued keys")
	}
	k.reset()
	if k.modifiers() != 0 || k.prefixLength != 0 {
		t.Fatal("VT release retained keyboard state")
	}
}

func TestConsoleRollbackAtEveryTakeoverStage(t *testing.T) {
	for fail := 0; fail <= 4; fail++ {
		t.Run(fmt.Sprint(fail), func(t *testing.T) {
			c := &console{fd: -1, oldKB: 3, oldTermios: unix.Termios{Lflag: unix.ECHO | unix.ICANON | unix.ISIG}}
			stage := 0
			var restored []string
			step := func() error {
				stage++
				if stage == fail {
					return errors.New("injected takeover failure")
				}
				return nil
			}
			c.ops = consoleOps{
				pointer: func(req uintptr, p unsafe.Pointer) error {
					if (*vtMode)(p).Mode == 1 {
						return step()
					}
					restored = append(restored, "vt")
					return nil
				},
				termios: func(term *unix.Termios) error {
					if term.Lflag == c.oldTermios.Lflag {
						restored = append(restored, "termios")
						return nil
					}
					return step()
				},
				value: func(req, value uintptr) error {
					if (req == kbSetMode && value == 2) || (req == kdSetMode && value == 1) {
						return step()
					}
					if req == kbSetMode {
						restored = append(restored, "keyboard")
					} else {
						restored = append(restored, "display")
					}
					return nil
				},
			}
			err := c.enter()
			if (fail == 0) != (err == nil) {
				t.Fatal("wrong injected failure")
			}
			if err := c.restore(); err != nil {
				t.Fatal(err)
			}
			if c.changedVT || c.changedKD || c.changedKB || c.changedTermios {
				t.Fatal("restore left console state armed")
			}
			before := len(restored)
			c.restore()
			if len(restored) != before {
				t.Fatal("restoration was not idempotent")
			}
			if len(restored) != stage {
				t.Fatalf("restored=%v stages=%d", restored, stage)
			}
		})
	}
}

func TestConsoleRestoreContinuesAfterIOCTLError(t *testing.T) {
	c := &console{changedVT: true, changedKD: true, changedKB: true, changedTermios: true}
	count := 0
	c.ops = consoleOps{pointer: func(uintptr, unsafe.Pointer) error { count++; return unix.EIO }, value: func(uintptr, uintptr) error { count++; return unix.EIO }, termios: func(*unix.Termios) error { count++; return unix.EIO }}
	if c.restore() == nil || count != 4 {
		t.Fatal("restore stopped after the first error")
	}
}

func mouseCallbacks(d *Driver, events *[]string) {
	d.callbacks = api.Callbacks{PointerMotion: func(mod uint16, x, y int) { *events = append(*events, fmt.Sprintf("move %d %d %d", x, y, mod)) },
		PointerPress: func(b byte, mod uint16, x, y int) {
			*events = append(*events, fmt.Sprintf("press %d %d %d %d", b, x, y, mod))
		},
		PointerRelease: func(b byte, mod uint16, x, y int) {
			*events = append(*events, fmt.Sprintf("release %d %d %d %d", b, x, y, mod))
		}}
}

func TestMousePacketsHiResButtonsClampAndDroppedRecovery(t *testing.T) {
	d := memoryDriver(t, 40, 40)
	d.pointer = image.Pt(10, 10)
	d.keyboard.pressed[42] = true
	var events []string
	mouseCallbacks(d, &events)
	m := &mouse{fd: -1, hiWheel: true, queryButtons: func() ([12]bool, error) { return [12]bool{}, nil }}
	event := func(kind, code uint16, value int32) {
		t.Helper()
		if err := m.event(d, kind, code, value); err != nil {
			t.Fatal(err)
		}
	}
	event(evRelative, relX, 4)
	event(evRelative, relY, -3)
	event(evKey, 0x110, 1)
	if len(events) != 0 {
		t.Fatal("input escaped a partial SYN packet")
	}
	event(evSyn, synReport, 0)
	if fmt.Sprint(events) != "[move 14 7 1 press 1 14 7 1]" {
		t.Fatal(events)
	}
	events = nil
	event(evRelative, relWheel, 1)
	event(evRelative, relWheelHi, 60)
	event(evSyn, synReport, 0)
	if len(events) != 0 {
		t.Fatal("coarse wheel double-counted alongside high-resolution data")
	}
	event(evRelative, relWheelHi, 60)
	event(evSyn, synReport, 0)
	if fmt.Sprint(events) != "[press 4 14 7 1 release 4 14 7 1]" {
		t.Fatal(events)
	}
	events = nil
	event(evRelative, relX, 1<<30)
	event(evRelative, relY, -1<<30)
	event(evSyn, synReport, 0)
	if d.pointer != image.Pt(39, 0) {
		t.Fatal("pointer was not clamped")
	}
	events = nil
	event(evSyn, synDropped, 0)
	event(evRelative, relX, -5)
	event(evKey, 0x110, 1)
	event(evSyn, synReport, 0)
	if fmt.Sprint(events) != "[release 1 39 0 1]" || m.buttons[1] {
		t.Fatal("SYN_DROPPED did not resynchronize buttons", events)
	}
	stamp := int(unsafe.Sizeof(unix.Timeval{}))
	data := make([]byte, stamp+8)
	binary.NativeEndian.PutUint16(data[stamp:], evRelative)
	binary.NativeEndian.PutUint16(data[stamp+2:], relX)
	binary.NativeEndian.PutUint32(data[stamp+4:], uint32(3))
	if err := m.decode(d, data); err != nil || m.dx != 3 {
		t.Fatal("native evdev decoding failed")
	}
	if m.decode(d, data[:len(data)-1]) == nil {
		t.Fatal("accepted truncated input_event")
	}
}

func TestMultipleMiceReleaseAndInactiveInput(t *testing.T) {
	d := memoryDriver(t, 40, 40)
	var events []string
	mouseCallbacks(d, &events)
	a, b := &mouse{fd: -1}, &mouse{fd: -1}
	d.changeButton(a, 1, true)
	d.changeButton(b, 1, true)
	d.releaseMouse(a)
	if len(events) != 1 || d.buttonCounts[1] != 1 {
		t.Fatal("one mouse released another mouse's button")
	}
	d.releaseMouse(b)
	if len(events) != 2 || d.buttonCounts[1] != 0 {
		t.Fatal("unplug retained held button")
	}
	d.active = false
	events = nil
	a.event(d, evRelative, relX, 10)
	a.event(d, evKey, 0x110, 1)
	a.event(d, evSyn, synReport, 0)
	if len(events) != 0 || a.dx != 0 || len(a.changes) != 0 {
		t.Fatal("inactive VT consumed physical input")
	}
}

func TestRunKeyboardVTSwitchRepaintAndGracefulCleanup(t *testing.T) {
	d := memoryDriver(t, 40, 40)
	var pipe [2]int
	if err := unix.Pipe2(pipe[:], unix.O_NONBLOCK|unix.O_CLOEXEC); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { unix.Close(pipe[1]) })
	var ownedVT atomic.Int32
	ownedVT.Store(1)
	var restored atomic.Int32
	released := make(chan struct{}, 1)
	d.console = &console{fd: pipe[0], number: 1, oldKB: 3, changedKD: true, changedKB: true, changedVT: true, changedTermios: true}
	d.restorePixels = true
	d.console.ops = consoleOps{pointer: func(req uintptr, p unsafe.Pointer) error {
		if req == vtGetState {
			(*vtState)(p).Active = uint16(ownedVT.Load())
		} else if req == vtSetMode {
			restored.Add(1)
		}
		return nil
	},
		value: func(req, value uintptr) error {
			if req == vtRelease && value == 1 {
				ownedVT.Store(2)
				released <- struct{}{}
			} else if req == kbSetMode || req == kdSetMode {
				restored.Add(1)
			}
			return nil
		}, termios: func(*unix.Termios) error { restored.Add(1); return nil }}
	d.signals = make(chan os.Signal, 8)
	d.mousePaths = "none"
	var lock sync.Mutex
	var closed atomic.Bool
	focus := make(chan bool, 8)
	keys := make(chan uint, 8)
	d.callbacks = api.Callbacks{Lock: lock.Lock, Unlock: lock.Unlock, Closed: closed.Load, Close: func() { closed.Store(true); d.Close() }, Resize: func(int, int) {}, Focus: func(v bool) { focus <- v }, PaintDirty: func() {}, KeyPress: func(k uint, _ uint16) { keys <- k }, Text: func(string) {}}
	done := make(chan struct{})
	go func() { defer close(done); d.Run() }()
	awaitFocus := func(want bool) {
		t.Helper()
		select {
		case got := <-focus:
			if got != want {
				t.Fatal("wrong focus")
			}
		case <-time.After(2 * time.Second):
			t.Fatal("Run stalled")
		}
	}
	awaitFocus(true)
	unix.Write(pipe[1], []byte{30, 30 | 128})
	select {
	case key := <-keys:
		if key != 'a' {
			t.Fatal(key)
		}
	case <-time.After(time.Second):
		t.Fatal("console key did not reach uniform callback")
	}
	d.signals <- unix.SIGUSR1
	awaitFocus(false)
	select {
	case <-released:
	case <-time.After(time.Second):
		t.Fatal("VT release was not acknowledged")
	}
	// No writes while another VT owns the screen; retained output survives.
	pixels := make([]uint32, 1600)
	for i := range pixels {
		pixels[i] = 0xff112233
	}
	d.Upload(pixels, 40, 0, 0, 40, 40)
	ownedVT.Store(1)
	d.signals <- unix.SIGUSR2
	awaitFocus(true)
	got := pixelAt(d, 0, 0)
	if got != 0x112233 {
		t.Fatal("VT acquire lost retained output")
	}
	d.signals <- unix.SIGTERM
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("graceful shutdown blocked on terminal lock")
	}
	if restored.Load() != 4 || !bytes.Equal(d.screen.memory, d.screen.original) {
		t.Fatal("shutdown did not restore console and pixels")
	}
}
