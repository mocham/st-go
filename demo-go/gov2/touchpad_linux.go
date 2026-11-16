//go:build linux

package main

// Evdev touchpad gestures, forked locally from the framebuffer input decoder.
// No terminal input or rendering package is linked into gov2.
import (
	"fmt"
	"time"
	"unsafe"
)

const (
	absX          = 0
	absY          = 1
	absMTSlot     = 0x2f
	absMTX        = 0x35
	absMTY        = 0x36
	absMTTracking = 0x39
	btnToolFinger = 0x145
	btnTouch      = 0x14a
	btnToolDouble = 0x14d
	btnToolTriple = 0x14e
	btnToolQuad   = 0x14f
	btnToolQuint  = 0x148
	tapTimeout    = 200 * time.Millisecond
	doubleTapTime = 300 * time.Millisecond
)

type absoluteAxis struct{ Value, Min, Max, Fuzz, Flat, Resolution int32 }
type touchContact struct {
	id, x, y       int32
	xValid, yValid bool
}
type touchpad struct {
	xAxis, yAxis                                         absoluteAxis
	multitouch                                           bool
	slots                                                []touchContact
	slot                                                 int
	x, y                                                 int32
	xValid, yValid, touching, suppressed                 bool
	tools                                                [5]bool
	previousCount                                        int
	previousID                                           uint64
	previousX, previousY, moveX, moveY, scrollX, scrollY int64
	tapStart, lastTap                                    time.Time
	tapX, tapY                                           int64
	tapCanceled, tapDrag, tapDragMoved                   bool
	now                                                  func() time.Time
	queryContact                                         func() (bool, error)
	queryPositions                                       func() error
}

func configureTouchpad(keys []byte, query mouseQuery) (*touchpad, error) {
	unsupported := fmt.Errorf("not a relative mouse or touchpad")
	if !hasBit(keys, btnToolFinger) || !hasBit(keys, btnTouch) {
		return nil, unsupported
	}
	var props [8]byte
	if err := query(readRequest('E', 0x09, uintptr(len(props))), unsafe.Pointer(&props[0])); err == nil && hasBit(props[:], 1) {
		return nil, unsupported
	} // INPUT_PROP_DIRECT
	abs, err := deviceBits(query, evAbsolute)
	if err != nil {
		return nil, err
	}
	p := &touchpad{now: time.Now}
	p.multitouch = hasBit(abs, absMTSlot) && hasBit(abs, absMTTracking) && hasBit(abs, absMTX) && hasBit(abs, absMTY)
	x, y := uint16(absX), uint16(absY)
	if p.multitouch {
		x, y = absMTX, absMTY
		var slots absoluteAxis
		if err := query(readRequest('E', 0x40+absMTSlot, 24), unsafe.Pointer(&slots)); err != nil {
			return nil, err
		}
		if slots.Min != 0 || slots.Max < 0 || slots.Max >= 32 {
			return nil, unsupported
		}
		p.slots = make([]touchContact, int(slots.Max)+1)
	} else if !hasBit(abs, absX) || !hasBit(abs, absY) {
		return nil, unsupported
	}
	for _, axis := range []struct {
		code uint16
		info *absoluteAxis
	}{{x, &p.xAxis}, {y, &p.yAxis}} {
		if err := query(readRequest('E', 0x40+uintptr(axis.code), 24), unsafe.Pointer(axis.info)); err != nil {
			return nil, err
		}
		if axis.info.Max <= axis.info.Min || axis.info.Resolution < 0 {
			return nil, unsupported
		}
	}
	p.queryContact = func() (bool, error) {
		var state [96]byte
		err := query(readRequest('E', 0x18, uintptr(len(state))), unsafe.Pointer(&state[0]))
		return hasBit(state[:], btnTouch), err
	}
	p.queryPositions = func() error {
		if p.multitouch {
			for _, axis := range []uint16{absMTX, absMTY} {
				values := make([]int32, len(p.slots)+1)
				values[0] = int32(axis)
				if err := query(readRequest('E', 0x0a, uintptr(len(values)*4)), unsafe.Pointer(&values[0])); err != nil {
					return err
				}
				for i := range p.slots {
					if axis == absMTX {
						p.slots[i].x, p.slots[i].xValid = max(p.xAxis.Min, min(p.xAxis.Max, values[i+1])), true
					} else {
						p.slots[i].y, p.slots[i].yValid = max(p.yAxis.Min, min(p.yAxis.Max, values[i+1])), true
					}
				}
			}
			return nil
		}
		var x, y absoluteAxis
		if err := query(readRequest('E', 0x40+absX, 24), unsafe.Pointer(&x)); err != nil {
			return err
		}
		if err := query(readRequest('E', 0x40+absY, 24), unsafe.Pointer(&y)); err != nil {
			return err
		}
		p.x, p.y = max(p.xAxis.Min, min(p.xAxis.Max, x.Value)), max(p.yAxis.Min, min(p.yAxis.Max, y.Value))
		p.xValid, p.yValid = true, true
		return nil
	}
	p.reset()
	return p, nil
}
func (p *touchpad) reset() {
	p.slot = 0
	for i := range p.slots {
		p.slots[i] = touchContact{id: -1}
	}
	p.xValid, p.yValid, p.touching, p.suppressed = false, false, false, false
	// Unchanged evdev axes are not re-emitted on the first movement.
	if p.queryPositions != nil {
		_ = p.queryPositions()
	}
	p.tools = [5]bool{}
	p.previousCount, p.previousID = 0, 0
	p.moveX, p.moveY, p.scrollX, p.scrollY = 0, 0, 0, 0
	p.tapStart, p.lastTap = time.Time{}, time.Time{}
	p.tapCanceled, p.tapDrag, p.tapDragMoved = true, false, false
}
func (p *touchpad) key(m *mouse, code uint16, value int32) {
	if value != 0 && value != 1 {
		return
	}
	down := value == 1
	switch code {
	case btnTouch:
		p.touching = down
		if !down {
			p.suppressed = false
		}
	case btnToolFinger:
		p.tools[0] = down
	case btnToolDouble:
		p.tools[1] = down
	case btnToolTriple:
		p.tools[2] = down
	case btnToolQuad:
		p.tools[3] = down
	case btnToolQuint:
		p.tools[4] = down
	default:
		if mouseButton(code) != 0 && down {
			p.tapCanceled, p.lastTap = true, time.Time{}
			if p.tapDrag {
				if len(m.changes) < 64 {
					m.changes = append(m.changes, buttonChange{1, false})
				}
				p.tapDrag, p.tapDragMoved = false, false
			}
		}
	}
}
func (p *touchpad) absolute(code uint16, value int32) {
	if code == absMTSlot {
		p.slot = int(value)
		return
	}
	if p.multitouch {
		if p.slot < 0 || p.slot >= len(p.slots) {
			return
		}
		s := &p.slots[p.slot]
		switch code {
		case absMTTracking:
			s.id = value
		case absMTX:
			s.x, s.xValid = max(p.xAxis.Min, min(p.xAxis.Max, value)), true
		case absMTY:
			s.y, s.yValid = max(p.yAxis.Min, min(p.yAxis.Max, value)), true
		}
		return // Do not count the emulated ABS_X/Y stream twice.
	}
	switch code {
	case absX:
		p.x, p.xValid = max(p.xAxis.Min, min(p.xAxis.Max, value)), true
	case absY:
		p.y, p.yValid = max(p.yAxis.Min, min(p.yAxis.Max, value)), true
	}
}
func (p *touchpad) contacts() (count int, x, y int64, identity uint64, valid bool) {
	if !p.touching || p.suppressed {
		return
	}
	if p.multitouch {
		valid = true
		for i, s := range p.slots {
			if s.id >= 0 {
				count++
				x += int64(s.x)
				y += int64(s.y)
				identity = identity*1099511628211 + uint64(uint32(s.id)) + uint64(i+1)
				valid = valid && s.xValid && s.yValid
			}
		}
		if count > 0 {
			x, y = x/int64(count), y/int64(count)
		}
	} else {
		count, x, y, valid = 1, int64(p.x), int64(p.y), p.xValid && p.yValid
	}
	for i, down := range p.tools {
		if down {
			count = max(count, i+1)
		}
	}
	return
}
func touchScale(delta int64, axis absoluteAxis, numerator, millimeters int64, remainder *int64) int64 {
	denominator := int64(axis.Resolution) * millimeters
	if denominator == 0 {
		denominator = (int64(axis.Max) - int64(axis.Min)) * millimeters
		numerator *= 100
	}
	value := delta*numerator + *remainder
	*remainder = value % denominator
	return value / denominator
}
func (p *touchpad) movedPastTap(x, y int64) bool {
	var remainder int64
	mx := touchScale(x-p.tapX, p.xAxis, 10, 1, &remainder)
	remainder = 0
	my := touchScale(y-p.tapY, p.yAxis, 10, 1, &remainder)
	return mx < -20 || mx > 20 || my < -20 || my > 20
}
func (p *touchpad) finishDrag(m *mouse, now time.Time, remember bool) {
	if len(m.changes) < 64 {
		m.changes = append(m.changes, buttonChange{1, false})
	}
	if remember && !p.tapDragMoved {
		p.lastTap = now
	} else {
		p.lastTap = time.Time{}
	}
	p.tapDrag, p.tapDragMoved = false, false
}
func (p *touchpad) flush(m *mouse) {
	count, x, y, identity, valid := p.contacts()
	now := p.now()
	if count == 0 || !valid {
		if p.tapDrag {
			p.finishDrag(m, now, count == 0 && !p.suppressed)
		} else if count == 0 && !p.suppressed && !p.tapCanceled && p.previousCount == 1 && !p.tapStart.IsZero() && now.Sub(p.tapStart) <= tapTimeout && !m.buttons[1] && len(m.changes) <= 62 {
			m.changes = append(m.changes, buttonChange{1, true}, buttonChange{1, false})
			p.lastTap = now
		} else if p.previousCount != 0 || p.suppressed || (!p.lastTap.IsZero() && now.Sub(p.lastTap) > doubleTapTime) {
			p.lastTap = time.Time{}
		}
		p.previousCount, p.tapCanceled = 0, true
		return
	}
	if p.previousCount != count || p.previousID != identity {
		if p.tapDrag {
			p.finishDrag(m, now, false)
		}
		if p.previousCount == 0 && count == 1 {
			p.tapStart, p.tapX, p.tapY = now, x, y
			p.tapCanceled = m.buttons[1] || len(m.changes) != 0
			elapsed := now.Sub(p.lastTap)
			if !p.tapCanceled && !p.lastTap.IsZero() && elapsed >= 0 && elapsed <= doubleTapTime && len(m.changes) < 64 {
				m.changes = append(m.changes, buttonChange{1, true})
				p.tapDrag, p.tapDragMoved, p.tapCanceled = true, false, true
				p.lastTap = time.Time{}
			} else if p.lastTap.IsZero() || elapsed < 0 || elapsed > doubleTapTime {
				p.lastTap = time.Time{}
			}
		} else {
			p.tapCanceled, p.lastTap = true, time.Time{}
		}
		p.previousCount, p.previousID, p.previousX, p.previousY = count, identity, x, y
		p.moveX, p.moveY, p.scrollX, p.scrollY = 0, 0, 0, 0
		return // Landing/lifting/replacing fingers must not jump the cursor.
	}
	dx, dy := x-p.previousX, y-p.previousY
	p.previousX, p.previousY = x, y
	if count == 1 {
		m.dx = max(-65535, min(65535, m.dx+touchScale(dx, p.xAxis, 10, 1, &p.moveX)))
		m.dy = max(-65535, min(65535, m.dy+touchScale(dy, p.yAxis, 10, 1, &p.moveY)))
		if p.movedPastTap(x, y) {
			if p.tapDrag {
				p.tapDragMoved = true
			} else {
				p.tapCanceled = true
			}
		}
	} else if count == 2 {
		p.tapCanceled = true
		m.wheel = boundedWheel(m.wheel - touchScale(dy, p.yAxis, 120, 3, &p.scrollY))
		m.hWheel = boundedWheel(m.hWheel + touchScale(dx, p.xAxis, 120, 3, &p.scrollX))
	} else {
		p.tapCanceled = true
	}
}
