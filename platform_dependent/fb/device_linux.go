//go:build linux

// Package fb implements the uniform display host on Linux fbdev and evdev.
// Device access uses the kernel ABI directly; no cgo, X, libdrm or libinput.
package fb

import (
	"encoding/binary"
	"fmt"
	"image"
	"runtime"
	"structs"
	"unsafe"

	"golang.org/x/sys/unix"
	"st-go/limits"
)

const (
	fbGetVariable = 0x4600
	fbGetFixed    = 0x4602
)

type bitfield struct{ Offset, Length, MSBRight uint32 }
type fixedInfo struct {
	_                                   structs.HostLayout
	ID                                  [16]byte
	MemoryStart                         uintptr
	MemoryLength, Type, TypeAux, Visual uint32
	XPanStep, YPanStep, YWrapStep       uint16
	LineLength                          uint32
	MMIOStart                           uintptr
	MMIOLength, Accel                   uint32
	Capabilities                        uint16
	Reserved                            [2]uint16
}
type variableInfo struct {
	_                                                             structs.HostLayout
	Width, Height, VirtualWidth, VirtualHeight, XOffset, YOffset  uint32
	BitsPerPixel, Grayscale                                       uint32
	Red, Green, Blue, Alpha                                       bitfield
	NonStandard, Activate, HeightMM, WidthMM, AccelFlags          uint32
	PixelClock, LeftMargin, RightMargin, UpperMargin, LowerMargin uint32
	HSyncLength, VSyncLength, Sync, VMode, Rotate, Colorspace     uint32
	Reserved                                                      [4]uint32
}

func ioctlPointer(fd int, request uintptr, value unsafe.Pointer) error {
	_, _, errno := unix.Syscall(unix.SYS_IOCTL, uintptr(fd), request, uintptr(value))
	runtime.KeepAlive(value)
	if errno != 0 {
		return errno
	}
	return nil
}
func ioctlValue(fd int, request, value uintptr) error {
	_, _, errno := unix.Syscall(unix.SYS_IOCTL, uintptr(fd), request, value)
	if errno != 0 {
		return errno
	}
	return nil
}
func readRequest(kind, number, size uintptr) uintptr {
	direction := uintptr(2)
	switch runtime.GOARCH {
	case "mips", "mipsle", "mips64", "mips64le", "ppc", "ppc64", "ppc64le", "sparc64":
		direction = 1
	}
	return direction<<30 | size<<16 | kind<<8 | number
}
func writeRequest(kind, number, size uintptr) uintptr {
	direction := uintptr(1) << 30
	if readRequest(0, 0, 0) == 1<<30 {
		direction = 1 << 31
	}
	return direction | size<<16 | kind<<8 | number
}

type layout struct {
	width, height, stride, bytes, length, xOffset, yOffset int
	red, green, blue, alpha                                bitfield
}

func screenLayout(f fixedInfo, v variableInfo) (layout, error) {
	fail := func() (layout, error) {
		return layout{}, fmt.Errorf("unsupported or invalid fbdev mode (need packed truecolor 16/24/32-bit pixels)")
	}
	if f.Type != 0 || f.Visual != 2 || v.NonStandard != 0 || v.Grayscale != 0 ||
		(v.BitsPerPixel != 16 && v.BitsPerPixel != 24 && v.BitsPerPixel != 32) || v.Width == 0 || v.Height == 0 || v.Width > 65535 || v.Height > 65535 {
		return fail()
	}
	if _, ok := limits.RGBA("fb display", int(v.Width), int(v.Height)); !ok {
		return fail()
	}
	var mask uint64
	for i, c := range []bitfield{v.Red, v.Green, v.Blue, v.Alpha} {
		if c.MSBRight != 0 || c.Length > 16 || (i < 3 && c.Length == 0) || c.Offset > v.BitsPerPixel || c.Length > v.BitsPerPixel-c.Offset {
			return fail()
		}
		bits := ((uint64(1) << c.Length) - 1) << c.Offset
		if bits&mask != 0 {
			return fail()
		}
		mask |= bits
	}
	endX, endY := uint64(v.XOffset)+uint64(v.Width), uint64(v.YOffset)+uint64(v.Height)
	if endX > uint64(v.VirtualWidth) || endY > uint64(v.VirtualHeight) || endX*uint64(v.BitsPerPixel/8) > uint64(f.LineLength) {
		return fail()
	}
	length := (endY-1)*uint64(f.LineLength) + endX*uint64(v.BitsPerPixel/8)
	if length == 0 || length > uint64(f.MemoryLength) || length > limits.PixelBytes {
		return fail()
	}
	return layout{int(v.Width), int(v.Height), int(f.LineLength), int(v.BitsPerPixel / 8), int(length), int(v.XOffset), int(v.YOffset), v.Red, v.Green, v.Blue, v.Alpha}, nil
}

type framebuffer struct {
	fd               int
	layout           layout
	memory, original []byte
	current          func() (layout, error)
}

func openFramebuffer(path string) (*framebuffer, error) {
	fd, err := unix.Open(path, unix.O_RDWR|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	good := false
	defer func() {
		if !good {
			unix.Close(fd)
		}
	}()
	var stat unix.Stat_t
	if err := unix.Fstat(fd, &stat); err != nil {
		return nil, err
	}
	if stat.Mode&unix.S_IFMT != unix.S_IFCHR {
		return nil, fmt.Errorf("framebuffer must be a character device")
	}
	l, err := queryLayout(fd)
	if err != nil {
		return nil, err
	}
	memory, err := unix.Mmap(fd, 0, l.length, unix.PROT_READ|unix.PROT_WRITE, unix.MAP_SHARED)
	if err != nil {
		return nil, err
	}
	good = true
	return &framebuffer{fd: fd, layout: l, memory: memory, original: append([]byte(nil), memory...), current: func() (layout, error) { return queryLayout(fd) }}, nil
}

func queryLayout(fd int) (layout, error) {
	var f fixedInfo
	var v variableInfo
	if err := ioctlPointer(fd, fbGetFixed, unsafe.Pointer(&f)); err != nil {
		return layout{}, fmt.Errorf("FBIOGET_FSCREENINFO: %w", err)
	}
	if err := ioctlPointer(fd, fbGetVariable, unsafe.Pointer(&v)); err != nil {
		return layout{}, fmt.Errorf("FBIOGET_VSCREENINFO: %w", err)
	}
	return screenLayout(f, v)
}

func channel(value uint32, c bitfield) uint32 {
	if c.Length == 0 {
		return 0
	}
	return uint32(uint64(value)*((1<<c.Length)-1)/255) << c.Offset
}

// blit presents an already-clipped opaque rectangle. Common 32-bit RGB layouts
// avoid per-channel scaling; uncommon formats retain the validated converter.
func (l layout) blit(memory []byte, r image.Rectangle, origin image.Point, pixels []uint32, stride int) {
	if r.Empty() {
		return
	}
	fast := l.bytes == 4 && l.red == (bitfield{Offset: 16, Length: 8}) && l.green == (bitfield{Offset: 8, Length: 8}) && l.blue == (bitfield{Length: 8}) && (l.alpha.Length == 0 || l.alpha == (bitfield{Offset: 24, Length: 8}))
	for y := r.Min.Y; y < r.Max.Y; y++ {
		at := (y-origin.Y)*stride + r.Min.X - origin.X
		row := pixels[at : at+r.Dx()]
		if fast {
			offset := (y+l.yOffset)*l.stride + (r.Min.X+l.xOffset)*4
			dst := memory[offset : offset+len(row)*4]
			mask := uint32(0)
			if l.alpha.Length != 0 {
				mask = 0xff000000
			}
			for x, pixel := range row {
				binary.NativeEndian.PutUint32(dst[x*4:], pixel&0xffffff|mask)
			}
		} else {
			for x, pixel := range row {
				l.pixel(memory, r.Min.X+x, y, pixel)
			}
		}
	}
}

func (l layout) pixel(memory []byte, x, y int, argb uint32) {
	pixel := channel(argb>>16&255, l.red) | channel(argb>>8&255, l.green) | channel(argb&255, l.blue) | channel(255, l.alpha)
	offset := (y+l.yOffset)*l.stride + (x+l.xOffset)*l.bytes
	switch l.bytes {
	case 2:
		binary.NativeEndian.PutUint16(memory[offset:], uint16(pixel))
	case 4:
		binary.NativeEndian.PutUint32(memory[offset:], pixel)
	case 3:
		if binary.NativeEndian.Uint16([]byte{1, 0}) == 1 {
			memory[offset], memory[offset+1], memory[offset+2] = byte(pixel), byte(pixel>>8), byte(pixel>>16)
		} else {
			memory[offset], memory[offset+1], memory[offset+2] = byte(pixel>>16), byte(pixel>>8), byte(pixel)
		}
	}
}

func (f *framebuffer) close() error {
	err := unix.Munmap(f.memory)
	f.memory, f.original = nil, nil
	return fmtClose(err, unix.Close(f.fd))
}
func (f *framebuffer) restore() {
	l := f.layout
	for y := range l.height {
		start := (y+l.yOffset)*l.stride + l.xOffset*l.bytes
		end := start + l.width*l.bytes
		copy(f.memory[start:end], f.original[start:end])
	}
}
func fmtClose(a, b error) error {
	if a != nil {
		return a
	}
	return b
}
