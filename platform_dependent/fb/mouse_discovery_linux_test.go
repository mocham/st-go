//go:build linux

package fb

import (
	"bytes"
	"encoding/binary"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
	"unsafe"

	"golang.org/x/sys/unix"
	"st-go/platform_neutral/api"
)

func mouseProbeQuery(t *testing.T, relative, left bool) mouseQuery {
	t.Helper()
	// Independent Linux UAPI request values: don't generate expectations with
	// the same helpers the production discovery path uses.
	dir := uintptr(0x80000000)
	switch runtime.GOARCH {
	case "mips", "mipsle", "mips64", "mips64le", "ppc", "ppc64", "ppc64le", "sparc64":
		dir = 0x40000000
	}
	set := func(p unsafe.Pointer, bit int) {
		bits := unsafe.Slice((*byte)(p), 96)
		wordBytes := int(unsafe.Sizeof(uintptr(0)))
		at := bit / (wordBytes * 8) * wordBytes
		if wordBytes == 8 {
			binary.NativeEndian.PutUint64(bits[at:], binary.NativeEndian.Uint64(bits[at:])|uint64(1)<<uint(bit%64))
		} else {
			binary.NativeEndian.PutUint32(bits[at:], binary.NativeEndian.Uint32(bits[at:])|uint32(1)<<uint(bit%32))
		}
	}
	return func(request uintptr, p unsafe.Pointer) error {
		switch request {
		case dir | 0x00044501:
			*(*uint32)(p) = 0x10001
		case dir | 0x00604522:
			if relative {
				for _, bit := range []int{0, 1, 8, 11} {
					set(p, bit)
				}
			}
		case dir | 0x00604521:
			if left {
				set(p, 0x110)
			}
		case dir | 0x00604518:
		default:
			t.Errorf("incorrect evdev ioctl request %#x", request)
			return unix.EINVAL
		}
		return nil
	}
}

func TestMouseDiscoveryKernelIOCTLsAndCapabilities(t *testing.T) {
	m, err := configureMouse(-1, "test-mouse", mouseProbeQuery(t, true, true))
	if err != nil || !m.hiWheel || m.hiHWheel {
		t.Fatalf("supported USB mouse rejected: %+v, %v", m, err)
	}
	if state, err := m.queryButtons(); err != nil || state != [12]bool{} {
		t.Fatal("button-state query failed", err)
	}
	for _, caps := range [][2]bool{{false, true}, {true, false}} {
		if _, err := configureMouse(-1, "not-a-mouse", mouseProbeQuery(t, caps[0], caps[1])); !errors.Is(err, errNotRelativeMouse) {
			t.Fatalf("unsupported device: %v", err)
		}
	}
	if _, err := configureMouse(-1, "denied", func(uintptr, unsafe.Pointer) error { return unix.EACCES }); !errors.Is(err, unix.EACCES) {
		t.Fatal("discovery hid access failure", err)
	}
}

func encodeMouseEvents(events ...[3]int32) []byte {
	stamp := int(unsafe.Sizeof(unix.Timeval{}))
	size := stamp + 8
	data := make([]byte, len(events)*size)
	for i, e := range events {
		at := i*size + stamp
		binary.NativeEndian.PutUint16(data[at:], uint16(e[0]))
		binary.NativeEndian.PutUint16(data[at+2:], uint16(e[1]))
		binary.NativeEndian.PutUint32(data[at+4:], uint32(e[2]))
	}
	return data
}

func TestMouseDiscoveryThroughRealPollReadAndPointerCallbacks(t *testing.T) {
	d := memoryDriver(t, 40, 40)
	d.pointer.X, d.pointer.Y = 10, 10
	var keys, mousePipe [2]int
	if err := unix.Pipe2(keys[:], unix.O_NONBLOCK|unix.O_CLOEXEC); err != nil {
		t.Fatal(err)
	}
	if err := unix.Pipe2(mousePipe[:], unix.O_NONBLOCK|unix.O_CLOEXEC); err != nil {
		unix.Close(keys[0])
		unix.Close(keys[1])
		t.Fatal(err)
	}
	m, err := configureMouse(mousePipe[0], "test-mouse", mouseProbeQuery(t, true, true))
	if err != nil {
		t.Fatal(err)
	}
	d.mice = []*mouse{m}
	d.mousePaths = "none"
	d.console = &console{fd: keys[0], number: 1, ops: consoleOps{pointer: func(req uintptr, p unsafe.Pointer) error {
		if req == vtGetState {
			(*vtState)(p).Active = 1
		}
		return nil
	}}}
	d.signals = make(chan os.Signal, 1)
	var lock sync.Mutex
	var closed atomic.Bool
	started := make(chan struct{}, 1)
	events := make(chan string, 12)
	d.callbacks = api.Callbacks{Lock: lock.Lock, Unlock: lock.Unlock, Closed: closed.Load, Close: func() { closed.Store(true); d.Close() }, Resize: func(int, int) {}, Focus: func(bool) { started <- struct{}{} }, PaintDirty: func() {},
		PointerMotion: func(_ uint16, x, y int) { events <- fmt.Sprintf("move %d %d", x, y) }, PointerPress: func(b byte, _ uint16, x, y int) { events <- fmt.Sprintf("press %d %d %d", b, x, y) }, PointerRelease: func(b byte, _ uint16, x, y int) { events <- fmt.Sprintf("release %d %d %d", b, x, y) }}
	done := make(chan struct{})
	go func() { defer close(done); d.Run() }()
	t.Cleanup(func() {
		d.Close()
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			t.Error("mouse poll loop failed to close")
		}
		unix.Close(keys[1])
		unix.Close(mousePipe[1])
	})
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("mouse poll loop failed to start")
	}
	data := encodeMouseEvents([3]int32{evRelative, relX, 4}, [3]int32{evRelative, relY, -3}, [3]int32{evKey, 0x110, 1}, [3]int32{evSyn, synReport, 0}, [3]int32{evKey, 0x110, 0}, [3]int32{evSyn, synReport, 0}, [3]int32{evRelative, relWheel, 1}, [3]int32{evRelative, relWheelHi, 120}, [3]int32{evSyn, synReport, 0})
	if n, err := unix.Write(mousePipe[1], data); err != nil || n != len(data) {
		t.Fatal("write synthetic kernel records", n, err)
	}
	for _, want := range []string{"move 14 7", "press 1 14 7", "release 1 14 7", "press 4 14 7", "release 4 14 7"} {
		select {
		case got := <-events:
			if got != want {
				t.Fatalf("mouse input=%q, want %q", got, want)
			}
		case <-time.After(2 * time.Second):
			t.Fatal("mouse input stalled before", want)
		}
	}
	d.mu.Lock()
	visible := d.pointerVisible
	d.mu.Unlock()
	if !visible {
		t.Fatal("mouse events never made the software pointer visible")
	}
}

func TestMouseDiagnosticExplainsMissingAndInaccessibleDevices(t *testing.T) {
	var out bytes.Buffer
	paths := []string{"/dev/input/event2", "/dev/input/event3", "/dev/input/event5"}
	err := describeMice(&out, paths, func(path string) mouseInfo {
		return mouseInfo{name: "USB mouse\x1b[2J", relKnown: true, relative: true}
	}, func(path string) (*mouse, error) {
		switch filepath.Base(path) {
		case "event2":
			return nil, unix.ENOENT
		case "event3":
			return nil, unix.EACCES
		default:
			return &mouse{fd: -1, hiWheel: true}, nil
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	text := out.String()
	for _, want := range []string{"no such file or directory", "permission denied", "OK: relative mouse", "Accessible supported mice: 1"} {
		if !strings.Contains(text, want) {
			t.Fatalf("missing diagnostic %q:\n%s", want, text)
		}
	}
	if strings.ContainsRune(text, '\x1b') {
		t.Fatal("device name injected terminal control bytes")
	}
	out.Reset()
	if err := describeMice(&out, nil, nil, nil); err != nil || !strings.Contains(out.String(), "Framebuffer/keyboard access alone does not grant mouse access") {
		t.Fatal("no-mouse diagnostic is not actionable")
	}
}

func TestMouseMetadataAndDiagnosticFlagDoNotNeedAConsole(t *testing.T) {
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, "capabilities"), 0700); err != nil {
		t.Fatal(err)
	}
	for path, data := range map[string]string{"name": "Wireless USB Mouse\n", "capabilities/rel": "1943\n", "capabilities/abs": "0\n"} {
		if err := os.WriteFile(filepath.Join(dir, path), []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
	}
	info := readMouseInfoAt(dir)
	if info.name != "Wireless USB Mouse" || !info.relKnown || !info.relative || info.absolute {
		t.Fatal("incorrect relative mouse metadata", info)
	}
	fs := flag.NewFlagSet("fb", flag.ContinueOnError)
	RegisterFlags(fs)
	if fs.Lookup("mouse-list") == nil {
		t.Fatal("missing standalone mouse diagnostic")
	}
	if err := fs.Parse([]string{"-mouse-list=false"}); err != nil {
		t.Fatal(err)
	}
}
