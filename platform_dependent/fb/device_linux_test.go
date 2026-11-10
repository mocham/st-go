//go:build linux

package fb

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"image"
	"os"
	"path/filepath"
	"testing"
	"unsafe"

	"st-go/platform_neutral/api"
)

func info(w, h uint32) (fixedInfo, variableInfo) {
	return fixedInfo{MemoryLength: w * h * 4, LineLength: w * 4, Visual: 2}, variableInfo{Width: w, Height: h, VirtualWidth: w, VirtualHeight: h, BitsPerPixel: 32, Red: bitfield{Offset: 16, Length: 8}, Green: bitfield{Offset: 8, Length: 8}, Blue: bitfield{Length: 8}}
}
func memoryDriver(t *testing.T, w, h int) *Driver {
	t.Helper()
	f, v := info(uint32(w), uint32(h))
	l, err := screenLayout(f, v)
	if err != nil {
		t.Fatal(err)
	}
	d := &Driver{screen: &framebuffer{fd: -1, layout: l, memory: make([]byte, l.length), original: make([]byte, l.length), current: func() (layout, error) { return l, nil }}, pixels: make([]uint32, w*h), active: true, keyboard: testKeyboard(), done: make(chan struct{}), clipboard: make(map[byte]string)}
	t.Cleanup(d.Close)
	return d
}
func pixelAt(d *Driver, x, y int) uint32 {
	// Inspection is an explicit presentation barrier for deterministic tests.
	// Paint requests themselves must never write scanout synchronously.
	d.flushPending()
	d.presentMu.Lock()
	defer d.presentMu.Unlock()
	l := d.screen.layout
	return binary.NativeEndian.Uint32(d.screen.memory[(y+l.yOffset)*l.stride+(x+l.xOffset)*4:])
}

func TestFramebufferABIAndValidation(t *testing.T) {
	if unsafe.Sizeof(variableInfo{}) != 160 || unsafe.Sizeof(vtMode{}) != 8 || unsafe.Sizeof(vtState{}) != 6 || unsafe.Sizeof(kbEntry{}) != 4 {
		t.Fatal("kernel ABI layout changed")
	}
	wantFixed := uintptr(80)
	if unsafe.Sizeof(uintptr(0)) == 4 {
		wantFixed = 68
	}
	if unsafe.Sizeof(fixedInfo{}) != wantFixed {
		t.Fatalf("fixed info size=%d", unsafe.Sizeof(fixedInfo{}))
	}
	f, v := info(20, 10)
	for _, test := range []struct {
		name   string
		change func(*fixedInfo, *variableInfo)
	}{
		{"over budget", func(f *fixedInfo, v *variableInfo) { v.Width = 8192; v.Height = 8192 }},
		{"zero", func(f *fixedInfo, v *variableInfo) { v.Width = 0 }},
		{"short memory", func(f *fixedInfo, v *variableInfo) { f.MemoryLength = 1 }},
		{"stride", func(f *fixedInfo, v *variableInfo) { f.LineLength = 1 }},
		{"virtual", func(f *fixedInfo, v *variableInfo) { v.XOffset = 1 }},
		{"overlap", func(f *fixedInfo, v *variableInfo) { v.Green = v.Red }},
		{"bit range", func(f *fixedInfo, v *variableInfo) { v.Red.Offset = 32 }},
		{"reversed", func(f *fixedInfo, v *variableInfo) { v.Red.MSBRight = 1 }},
		{"palette", func(f *fixedInfo, v *variableInfo) { f.Visual = 4 }},
		{"planar", func(f *fixedInfo, v *variableInfo) { f.Type = 1 }},
		{"nonstandard", func(f *fixedInfo, v *variableInfo) { v.NonStandard = 1 }},
	} {
		t.Run(test.name, func(t *testing.T) {
			a, b := f, v
			test.change(&a, &b)
			if _, err := screenLayout(a, b); err == nil {
				t.Fatal("accepted invalid mode")
			}
		})
	}
}

func TestNativePixelFormatsOffsetsAndPadding(t *testing.T) {
	for _, depth := range []uint32{16, 24, 32} {
		t.Run(fmt.Sprint(depth), func(t *testing.T) {
			f, v := info(2, 2)
			v.BitsPerPixel = depth
			v.VirtualWidth = 4
			v.VirtualHeight = 4
			v.XOffset = 1
			v.YOffset = 1
			f.LineLength = 4*(depth/8) + 8
			f.MemoryLength = f.LineLength * 4
			if depth == 16 {
				v.Red = bitfield{Offset: 11, Length: 5}
				v.Green = bitfield{Offset: 5, Length: 6}
				v.Blue = bitfield{Length: 5}
			}
			l, err := screenLayout(f, v)
			if err != nil {
				t.Fatal(err)
			}
			memory := bytes.Repeat([]byte{0xcc}, l.length)
			l.pixel(memory, 0, 0, 0xffff0080)
			offset := l.stride + l.bytes
			value := uint32(0xff0080)
			if depth == 16 {
				value = 0xf80f
			}
			var got uint32
			if depth == 16 {
				got = uint32(binary.NativeEndian.Uint16(memory[offset:]))
			} else if depth == 32 {
				got = binary.NativeEndian.Uint32(memory[offset:])
			} else if binary.NativeEndian.Uint16([]byte{1, 0}) == 1 {
				got = uint32(memory[offset]) | uint32(memory[offset+1])<<8 | uint32(memory[offset+2])<<16
			} else {
				got = uint32(memory[offset])<<16 | uint32(memory[offset+1])<<8 | uint32(memory[offset+2])
			}
			if got != value {
				t.Fatalf("pixel=%x want %x", got, value)
			}
			for i, b := range memory {
				if i >= offset && i < offset+l.bytes {
					continue
				}
				if b != 0xcc {
					t.Fatalf("padding/prefix changed at %d", i)
				}
			}
		})
	}
}

func TestDamageSurfacesAndSoftwarePointer(t *testing.T) {
	d := memoryDriver(t, 40, 40)
	pixels := make([]uint32, 1600)
	for i := range pixels {
		pixels[i] = 0xff224466
	}
	d.Upload(pixels, 40, 0, 0, 40, 40)
	d.movePointer(image.Pt(2, 2), true)
	if pixelAt(d, 2, 2) != 0 || d.pixels[2*40+2] != 0xff224466 {
		t.Fatal("pointer contaminated terminal storage")
	}
	d.movePointer(image.Pt(20, 20), true)
	if pixelAt(d, 2, 2) != 0x224466 {
		t.Fatal("pointer left a trail")
	}
	d.movePointer(d.pointer, false)
	s, err := d.NewSurface(image.Rect(25, 0, 30, 5), 0xff55aa00)
	if err != nil {
		t.Fatal(err)
	}
	d.Upload(pixels, 40, 0, 0, 40, 40)
	if pixelAt(d, 26, 1) != 0x55aa00 {
		t.Fatal("terminal erase overwrote retained pane")
	}
	s.Close()
	if pixelAt(d, 26, 1) != 0x224466 {
		t.Fatal("surface close did not restore terminal pixels")
	}
	// Deliberately poison a clean scanout row. Dirty uploads must not bridge it.
	l := d.screen.layout
	l.pixel(d.screen.memory, 3, 15, 0xffbbaa99)
	d.Upload(pixels, 40, 0, 0, 40, 1)
	d.Upload(pixels, 40, 0, 30, 40, 1)
	if pixelAt(d, 3, 15) != 0xbbaa99 {
		t.Fatal("separate damage crossed clean rows")
	}
	before := append([]byte(nil), d.screen.memory...)
	d.active = false
	for i := range pixels {
		pixels[i] = 0xff112233
	}
	d.Upload(pixels, 40, 0, 0, 40, 40)
	if !bytes.Equal(before, d.screen.memory) || d.pixels[0] != 0xff112233 {
		t.Fatal("inactive VT painted or lost retained updates")
	}
}

func TestFramebufferRejectsRegularFileWithoutModifyingIt(t *testing.T) {
	path := filepath.Join(t.TempDir(), "not-a-device")
	if err := os.WriteFile(path, []byte("untouched"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := New(api.WindowOptions{}, Options{Device: path}); err == nil {
		t.Fatal("accepted regular file as hardware")
	}
	data, err := os.ReadFile(path)
	if err != nil || string(data) != "untouched" {
		t.Fatal("failed startup modified file")
	}
}

func TestClipboardIsLocalAndCallbacksRunUnlocked(t *testing.T) {
	d := memoryDriver(t, 20, 20)
	d.SetClipboard('c', "copied 世界")
	d.QueryClipboard('c', func(text string, ok bool) {
		if !ok || text != "copied 世界" {
			t.Fatal("lost clipboard")
		}
		d.SetClipboard('p', text)
	})
	d.QueryClipboard('x', func(_ string, ok bool) {
		if ok {
			t.Fatal("accepted invalid selection")
		}
	})
	d.Close()
	d.QueryClipboard('c', func(_ string, ok bool) {
		if ok {
			t.Fatal("clipboard survived shutdown")
		}
	})
}

func TestModeChangeOrInactiveCloseDoesNotRestoreOverOtherDisplay(t *testing.T) {
	for _, changed := range []bool{false, true} {
		t.Run(fmt.Sprint(changed), func(t *testing.T) {
			d := memoryDriver(t, 20, 20)
			d.console = &console{fd: -1, number: 1, changedKD: true}
			d.console.ops = consoleOps{pointer: func(req uintptr, p unsafe.Pointer) error {
				if req == vtGetState {
					(*vtState)(p).Active = 2
					if changed {
						(*vtState)(p).Active = 1
					}
				}
				return nil
			}, value: func(uintptr, uintptr) error { return nil }}
			d.restorePixels = true
			for i := range d.screen.memory {
				d.screen.memory[i] = 0x5a
			}
			if changed {
				d.screen.current = func() (layout, error) { next := d.screen.layout; next.stride += 4; return next, nil }
				if d.acquire() == nil || d.restorePixels {
					t.Fatal("accepted changed framebuffer layout")
				}
			}
			d.Close()
			if !bytes.Equal(d.screen.memory, bytes.Repeat([]byte{0x5a}, len(d.screen.memory))) {
				t.Fatal("shutdown overwrote another console/mode")
			}
		})
	}
}
