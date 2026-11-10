//go:build linux

package fb

import (
	"encoding/binary"
	"errors"
	"fmt"
	"image"
	"path/filepath"
	"strings"
	"time"
	"unsafe"

	"golang.org/x/sys/unix"
	"st-go/limits"
)

const (
	evSyn       = 0
	evKey       = 1
	evRelative  = 2
	evAbsolute  = 3
	synReport   = 0
	synDropped  = 3
	relX        = 0
	relY        = 1
	relHWheel   = 6
	relWheel    = 8
	relWheelHi  = 11
	relHWheelHi = 12
)

type buttonChange struct {
	button byte
	down   bool
}
type mouse struct {
	fd                    int
	path                  string
	hiWheel, hiHWheel     bool
	dropped               bool
	dx, dy, wheel, hWheel int64
	buttons               [12]bool
	changes               []buttonChange
	queryButtons          func() ([12]bool, error)
	touchpad              *touchpad
}

var errNotRelativeMouse = errors.New("not a supported relative mouse or absolute touchpad")

type mouseQuery func(uintptr, unsafe.Pointer) error

func hasBit(bits []byte, bit int) bool {
	if bit < 0 {
		return false
	}
	// Evdev capability/state bitmaps contain native unsigned-long words,
	// rather than an endian-independent byte stream.
	wordBytes := int(unsafe.Sizeof(uintptr(0)))
	at := bit / 8
	if binary.NativeEndian.Uint16([]byte{1, 0}) != 1 {
		at = (bit/(8*wordBytes))*wordBytes + (wordBytes - 1 - (bit/8)%wordBytes)
	}
	return at < len(bits) && bits[at]&(1<<uint(bit%8)) != 0
}
func mouseButton(code uint16) byte {
	switch code {
	case 0x110:
		return 1
	case 0x111:
		return 3
	case 0x112:
		return 2
	case 0x113:
		return 8
	case 0x114:
		return 9
	case 0x115:
		return 10
	case 0x116:
		return 11
	}
	return 0
}
func deviceBits(query mouseQuery, kind uint16) ([]byte, error) {
	bits := make([]byte, 96)
	err := query(readRequest('E', 0x20+uintptr(kind), uintptr(len(bits))), unsafe.Pointer(&bits[0]))
	return bits, err
}
func openMouse(path string) (*mouse, error) {
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	good := false
	defer func() {
		if !good {
			unix.Close(fd)
		}
	}()
	m, err := configureMouse(fd, path, func(request uintptr, p unsafe.Pointer) error { return ioctlPointer(fd, request, p) })
	if err != nil {
		return nil, err
	}
	good = true
	return m, nil
}

// Keep the production ioctl/capability path testable, not just synthetic events
// injected after discovery. Querying a device never reads its event stream.
func configureMouse(fd int, path string, query mouseQuery) (*mouse, error) {
	var version uint32
	if err := query(readRequest('E', 1, 4), unsafe.Pointer(&version)); err != nil {
		return nil, fmt.Errorf("not an evdev device: %w", err)
	}
	rel, err := deviceBits(query, evRelative)
	if err != nil {
		return nil, err
	}
	keys, err := deviceBits(query, evKey)
	if err != nil {
		return nil, err
	}
	m := &mouse{fd: fd, path: path, hiWheel: hasBit(rel, relWheelHi), hiHWheel: hasBit(rel, relHWheelHi)}
	if !hasBit(rel, relX) || !hasBit(rel, relY) || !hasBit(keys, 0x110) {
		m.touchpad, err = configureTouchpad(keys, query)
		if err != nil {
			return nil, err
		}
	}
	m.queryButtons = func() ([12]bool, error) {
		var state [12]bool
		var bits [96]byte
		if err := query(readRequest('E', 0x18, uintptr(len(bits))), unsafe.Pointer(&bits[0])); err != nil {
			return state, err
		}
		for code := uint16(0x110); code <= 0x116; code++ {
			if button := mouseButton(code); button != 0 {
				state[button] = hasBit(bits[:], int(code))
			}
		}
		return state, nil
	}
	return m, nil
}
func (m *mouse) grab(grab bool) error {
	value := uintptr(0)
	if grab {
		value = 1
	}
	return ioctlValue(m.fd, writeRequest('E', 0x90, 4), value)
}
func (m *mouse) close() {
	if m.fd >= 0 {
		unix.Close(m.fd)
	}
}
func (m *mouse) reset() {
	m.dx = 0
	m.dy = 0
	m.wheel = 0
	m.hWheel = 0
	m.changes = nil
	m.dropped = false
	if m.touchpad != nil {
		m.touchpad.reset()
	}
}
func (m *mouse) drain() {
	var buf [4096]byte
	for range 32 {
		n, err := unix.Read(m.fd, buf[:])
		if err != nil || n == 0 {
			return
		}
	}
}

func (d *Driver) scanMice(initial bool) error {
	d.lastScan = time.Now()
	if d.mousePaths == "none" {
		return nil
	}
	paths := strings.Split(d.mousePaths, ",")
	auto := d.mousePaths == "auto"
	if auto {
		paths, _ = filepath.Glob("/dev/input/event*")
	}
	for _, path := range paths {
		path = strings.TrimSpace(path)
		found := false
		for _, m := range d.mice {
			if m.path == path {
				found = true
				break
			}
		}
		if found {
			continue
		}
		if len(d.mice) >= 16 {
			if auto {
				break
			}
			return fmt.Errorf("maximum 16 mouse devices")
		}
		m, err := openMouse(path)
		if err != nil {
			// Auto discovery used to discard all failures here, making a mouse
			// permission/ioctl problem indistinguishable from broken input.
			info := readMouseInfo(path)
			if !errors.Is(err, errNotRelativeMouse) && (!info.relKnown || info.relative || info.absolute) {
				d.reportMouseFailure(path, fmt.Sprintf("%q: %v", info.name, err))
			}
			if initial && !auto {
				return fmt.Errorf("mouse %s: %w", path, err)
			}
			continue
		}
		delete(d.mouseFailures, path)
		if d.grabMouse && d.isActive() {
			if err := m.grab(true); err != nil {
				m.close()
				if !auto {
					return err
				}
				limits.Report("fb mouse grab", "%s: %v", path, err)
				continue
			}
		}
		d.mice = append(d.mice, m)
	}
	if initial && auto && len(d.mice) == 0 {
		limits.Report("fb mouse", "no accessible mice/touchpads; run st-fb -mouse-list on the host to check device nodes, permissions and capabilities; keyboard-only until a pointer is discovered")
	}
	return nil
}

func (d *Driver) reportMouseFailure(path, detail string) {
	if d.mouseFailures == nil {
		d.mouseFailures = make(map[string]string)
	}
	if d.mouseFailures[path] == detail {
		return
	}
	d.mouseFailures[path] = detail
	limits.Report("fb mouse", "%s: %s", path, detail)
}

func (m *mouse) read(d *Driver) error {
	var data [4096]byte
	n, err := unix.Read(m.fd, data[:])
	if err == unix.EAGAIN || err == unix.EINTR {
		return nil
	}
	if err != nil {
		return err
	}
	if n == 0 {
		return fmt.Errorf("evdev EOF")
	}
	if !d.isActive() {
		m.reset()
		return nil
	}
	return m.decode(d, data[:n])
}
func (m *mouse) decode(d *Driver, data []byte) error {
	stamp := int(unsafe.Sizeof(unix.Timeval{}))
	size := stamp + 8
	if len(data)%size != 0 {
		return fmt.Errorf("truncated evdev record")
	}
	for len(data) > 0 {
		e := data[stamp:size]
		kind, code, value := binary.NativeEndian.Uint16(e), binary.NativeEndian.Uint16(e[2:]), int32(binary.NativeEndian.Uint32(e[4:]))
		if err := m.event(d, kind, code, value); err != nil {
			return err
		}
		data = data[size:]
	}
	return nil
}

func (m *mouse) event(d *Driver, kind, code uint16, value int32) error {
	if kind == evSyn && code == synDropped {
		m.reset()
		m.dropped = true
		return nil
	}
	if m.dropped {
		if kind == evSyn && code == synReport {
			state, err := m.queryButtons()
			if err != nil {
				return err
			}
			m.dropped = false
			if m.touchpad != nil {
				touching, err := m.touchpad.queryContact()
				if err != nil {
					return err
				}
				m.touchpad.suppressed = touching
			}
			for button := byte(1); button < 12; button++ {
				if state[button] != m.buttons[button] {
					d.changeButton(m, button, state[button])
				}
			}
		}
		return nil
	}
	switch kind {
	case evRelative:
		switch code {
		case relX:
			m.dx = max(-65535, min(65535, m.dx+int64(value)))
		case relY:
			m.dy = max(-65535, min(65535, m.dy+int64(value)))
		case relWheel:
			if !m.hiWheel {
				m.wheel = boundedWheel(m.wheel + int64(value)*120)
			}
		case relHWheel:
			if !m.hiHWheel {
				m.hWheel = boundedWheel(m.hWheel + int64(value)*120)
			}
		case relWheelHi:
			if m.hiWheel {
				m.wheel = boundedWheel(m.wheel + int64(value))
			}
		case relHWheelHi:
			if m.hiHWheel {
				m.hWheel = boundedWheel(m.hWheel + int64(value))
			}
		}
	case evKey:
		if m.touchpad != nil {
			m.touchpad.key(m, code, value)
		}
		if button := mouseButton(code); button != 0 && (value == 0 || value == 1) {
			if len(m.changes) >= 64 {
				m.reset()
				m.dropped = true
				return nil
			}
			m.changes = append(m.changes, buttonChange{button, value == 1})
		}
	case evAbsolute:
		if m.touchpad != nil {
			m.touchpad.absolute(code, value)
		}
	case evSyn:
		if code == synReport {
			if m.touchpad != nil {
				m.touchpad.flush(m)
			}
			m.flush(d)
		}
	}
	return nil
}
func boundedWheel(n int64) int64 { return max(-120*32, min(120*32, n)) }

func (m *mouse) flush(d *Driver) {
	if !d.isActive() {
		m.reset()
		return
	}
	d.mu.Lock()
	p := d.pointer
	d.mu.Unlock()
	if m.dx != 0 || m.dy != 0 || len(m.changes) > 0 || m.wheel/120 != 0 || m.hWheel/120 != 0 {
		p = p.Add(image.Pt(int(m.dx), int(m.dy)))
		d.movePointer(p, true)
		d.mu.Lock()
		p = d.pointer
		d.mu.Unlock()
		if m.dx != 0 || m.dy != 0 {
			d.callbacks.PointerMotion(d.keyboard.modifiers(), p.X, p.Y)
		}
	}
	m.dx, m.dy = 0, 0
	for _, change := range m.changes {
		d.changeButton(m, change.button, change.down)
	}
	m.changes = m.changes[:0]
	mods := d.keyboard.modifiers()
	for _, axis := range []struct {
		value              *int64
		positive, negative byte
	}{{&m.wheel, 4, 5}, {&m.hWheel, 7, 6}} {
		steps := *axis.value / 120
		*axis.value -= steps * 120
		button := axis.positive
		if steps < 0 {
			button = axis.negative
			steps = -steps
		}
		for range int(steps) {
			d.callbacks.PointerPress(button, mods, p.X, p.Y)
			d.callbacks.PointerRelease(button, mods, p.X, p.Y)
		}
	}
}

// Button references span all mice: releasing one device must not release a
// button which is still down on another device.
func (d *Driver) changeButton(m *mouse, button byte, down bool) {
	if m.buttons[button] == down {
		return
	}
	m.buttons[button] = down
	mods := d.keyboard.modifiers()
	d.mu.Lock()
	p := d.pointer
	d.mu.Unlock()
	if down {
		d.buttonCounts[button]++
		if d.buttonCounts[button] == 1 {
			d.callbacks.PointerPress(button, mods, p.X, p.Y)
		}
	} else {
		d.buttonCounts[button] = max(0, d.buttonCounts[button]-1)
		if d.buttonCounts[button] == 0 {
			d.callbacks.PointerRelease(button, mods, p.X, p.Y)
		}
	}
}
func (d *Driver) releaseMouse(m *mouse) {
	for button := byte(1); button < 12; button++ {
		if m.buttons[button] {
			d.changeButton(m, button, false)
		}
	}
}

func (d *Driver) removeMouse(index int) {
	m := d.mice[index]
	d.releaseMouse(m)
	m.close()
	d.mice = append(d.mice[:index], d.mice[index+1:]...)
	if len(d.mice) == 0 {
		d.mu.Lock()
		p := d.pointer
		d.mu.Unlock()
		d.movePointer(p, false)
	}
}
