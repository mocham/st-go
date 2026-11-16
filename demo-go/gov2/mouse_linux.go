//go:build linux

package main

// Private evdev input for the DRM player. The host releases its grabs before
// handoff; gov2 releases its own grabs before switching VT or returning.
import (
	"encoding/binary"
	"fmt"
	"path/filepath"
	"runtime"
	"time"
	"unsafe"

	"golang.org/x/sys/unix"
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
type mouseQuery func(uintptr, unsafe.Pointer) error
type mouse struct {
	fd                         int
	path                       string
	hiWheel, hiHWheel, dropped bool
	dx, dy, wheel, hWheel      int64
	buttons, suppressed        [4]bool
	changes                    []buttonChange
	queryButtons               func() ([4]bool, error)
	touchpad                   *touchpad
}

func readRequest(kind, number, size uintptr) uintptr {
	direction := uintptr(2)
	switch runtime.GOARCH {
	case "mips", "mipsle", "mips64", "mips64le", "ppc", "ppc64", "ppc64le", "sparc64":
		direction = 1
	}
	return direction<<30 | size<<16 | kind<<8 | number
}
func hasBit(bits []byte, bit int) bool {
	word := int(unsafe.Sizeof(uintptr(0)))
	at := bit / 8
	if binary.NativeEndian.Uint16([]byte{1, 0}) != 1 {
		at = bit/(8*word)*word + word - 1 - (bit/8)%word
	}
	return at < len(bits) && bits[at]&(1<<uint(bit%8)) != 0
}
func deviceBits(query mouseQuery, kind uint16) ([]byte, error) {
	bits := make([]byte, 96)
	err := query(readRequest('E', 0x20+uintptr(kind), uintptr(len(bits))), unsafe.Pointer(&bits[0]))
	return bits, err
}
func mouseButton(code uint16) byte {
	switch code {
	case 0x110:
		return 1
	case 0x111:
		return 3
	case 0x112:
		return 2
	}
	return 0
}
func configureMouse(fd int, path string, query mouseQuery) (*mouse, error) {
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
	m.queryButtons = func() ([4]bool, error) {
		var state [4]bool
		var bits [96]byte
		err := query(readRequest('E', 0x18, uintptr(len(bits))), unsafe.Pointer(&bits[0]))
		for code := uint16(0x110); code <= 0x112; code++ {
			state[mouseButton(code)] = hasBit(bits[:], int(code))
		}
		return state, err
	}
	return m, nil
}
func (m *mouse) grab(grab bool) error {
	direction := uintptr(1) << 30
	if readRequest(0, 0, 0) == 1<<30 {
		direction = 1 << 31
	}
	value := 0
	if grab {
		value = 1
	}
	return unix.IoctlSetInt(m.fd, uint(direction|4<<16|'E'<<8|0x90), value)
}
func (m *mouse) reset() {
	m.dx, m.dy, m.wheel, m.hWheel = 0, 0, 0, 0
	m.changes, m.dropped = nil, false
	if m.touchpad != nil {
		m.touchpad.reset()
	}
}
func (m *mouse) release(emit func(pointerEvent)) {
	emit(pointerEvent{cancel: true})
	for b := byte(1); b < 4; b++ {
		if m.buttons[b] {
			m.buttons[b] = false
			emit(pointerEvent{button: b})
		}
	}
	m.reset()
}
func (m *mouse) synchronize() error {
	state, err := m.queryButtons()
	if err != nil {
		return err
	}
	m.suppressed = state // Never synthesize clicks for buttons held in another VT.
	if m.touchpad != nil {
		touching, err := m.touchpad.queryContact()
		if err != nil {
			return err
		}
		m.touchpad.suppressed = touching
	}
	return nil
}
func (m *mouse) activate() error {
	if err := m.grab(true); err != nil {
		return err
	}
	var buf [4096]byte
	for range 64 {
		n, err := unix.Read(m.fd, buf[:])
		if err == unix.EAGAIN || n == 0 {
			break
		}
		if err != nil && err != unix.EINTR {
			return err
		}
	}
	m.reset()
	return m.synchronize()
}
func (m *mouse) read(emit func(pointerEvent)) error {
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
	return m.decode(data[:n], emit)
}
func (m *mouse) decode(data []byte, emit func(pointerEvent)) error {
	stamp := int(unsafe.Sizeof(unix.Timeval{}))
	size := stamp + 8
	if len(data)%size != 0 {
		return fmt.Errorf("truncated evdev record")
	}
	for len(data) > 0 {
		e := data[stamp:size]
		if err := m.event(binary.NativeEndian.Uint16(e), binary.NativeEndian.Uint16(e[2:]), int32(binary.NativeEndian.Uint32(e[4:])), emit); err != nil {
			return err
		}
		data = data[size:]
	}
	return nil
}
func boundedWheel(n int64) int64 { return max(-120*32, min(120*32, n)) }
func (m *mouse) event(kind, code uint16, value int32, emit func(pointerEvent)) error {
	if kind == evSyn && code == synDropped {
		m.release(emit)
		m.dropped = true
		return nil
	}
	if m.dropped {
		if kind == evSyn && code == synReport {
			m.dropped = false
			return m.synchronize()
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
		if b := mouseButton(code); b != 0 && (value == 0 || value == 1) {
			if m.suppressed[b] {
				if value == 0 {
					m.suppressed[b] = false
				}
				return nil
			}
			if len(m.changes) >= 64 {
				m.release(emit)
				m.dropped = true
				return nil
			}
			m.changes = append(m.changes, buttonChange{b, value == 1})
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
			if m.dx != 0 || m.dy != 0 {
				emit(pointerEvent{dx: int(m.dx), dy: int(m.dy)})
			}
			m.dx, m.dy = 0, 0
			for _, c := range m.changes {
				if m.buttons[c.button] != c.down {
					m.buttons[c.button] = c.down
					emit(pointerEvent{button: c.button, down: c.down})
				}
			}
			m.changes = m.changes[:0]
			if m.wheel/120 != 0 || m.hWheel/120 != 0 {
				emit(pointerEvent{wheel: int(m.wheel / 120), horizontal: int(m.hWheel / 120)})
			}
			m.wheel, m.hWheel = m.wheel%120, m.hWheel%120
		}
	}
	return nil
}

type pointerDevices struct {
	mice     []*mouse
	counts   [4]int
	lastScan time.Time
	emit     func(pointerEvent)
}

func (d *pointerDevices) send(e pointerEvent) {
	if e.button != 0 {
		if e.down {
			d.counts[e.button]++
			if d.counts[e.button] != 1 {
				return
			}
		} else {
			d.counts[e.button] = max(0, d.counts[e.button]-1)
			if d.counts[e.button] != 0 {
				return
			}
		}
	}
	d.emit(e)
}
func (d *pointerDevices) scan() {
	if time.Since(d.lastScan) < time.Second {
		return
	}
	d.lastScan = time.Now()
	paths, _ := filepath.Glob("/dev/input/event*")
	for _, path := range paths {
		found := false
		for _, m := range d.mice {
			if m.path == path {
				found = true
				break
			}
		}
		if found || len(d.mice) >= 16 {
			continue
		}
		fd, err := unix.Open(path, unix.O_RDONLY|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
		if err != nil {
			continue
		}
		m, err := configureMouse(fd, path, func(r uintptr, p unsafe.Pointer) error { return ioctlPointer(fd, r, p) })
		if err == nil {
			err = m.activate()
		}
		if err != nil {
			unix.Close(fd)
			continue
		}
		d.mice = append(d.mice, m)
	}
}
func (d *pointerDevices) remove(i int) {
	m := d.mice[i]
	m.release(d.send)
	m.grab(false)
	unix.Close(m.fd)
	d.mice = append(d.mice[:i], d.mice[i+1:]...)
}
func (d *pointerDevices) close() {
	for len(d.mice) > 0 {
		d.remove(len(d.mice) - 1)
	}
	d.lastScan = time.Time{}
}
