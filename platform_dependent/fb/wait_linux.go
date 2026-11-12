//go:build linux

package fb

import (
	"encoding/binary"
	"os"
	"sync"
	"time"

	"golang.org/x/sys/unix"
)

// eventfd is a coalescing mailbox. The lock prevents a late asynchronous
// completion from writing to a descriptor which cleanup has closed/reused.
type inputWake struct {
	mu   sync.Mutex
	fd   int
	open bool
}

func (w *inputWake) init() (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	fd, err := unix.Eventfd(0, unix.EFD_CLOEXEC|unix.EFD_NONBLOCK)
	if err == nil {
		w.fd, w.open = fd, true
	}
	return fd, err
}

func (w *inputWake) signal() {
	w.mu.Lock()
	defer w.mu.Unlock()
	if !w.open {
		return
	}
	var data [8]byte
	binary.NativeEndian.PutUint64(data[:], 1)
	for {
		_, err := unix.Write(w.fd, data[:])
		if err != unix.EINTR {
			return
		} // EAGAIN means a wake is already pending.
	}
}

func (w *inputWake) close() {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.open {
		unix.Close(w.fd)
		w.open = false
	}
}

func pollTimeout(now, deadline time.Time) int {
	if deadline.IsZero() {
		return -1
	}
	return int(max(0, (deadline.Sub(now)+time.Millisecond-1)/time.Millisecond))
}

// Input readiness interrupts this wait immediately. Only actual maintenance
// creates a timeout; an inactive VT has no maintenance wakeups.
func (d *Driver) maintenanceDeadline(now time.Time, watch bool) time.Time {
	d.mu.Lock()
	defer d.mu.Unlock()
	if !d.active || d.closed {
		return time.Time{}
	}
	var next time.Time
	if b := d.systemBar; b != nil {
		next = b.nextRefresh
		if next.IsZero() {
			next = now
		}
	}
	if !watch && d.mousePaths != "none" {
		scan := d.lastScan.Add(systemBarInterval)
		if next.IsZero() || scan.Before(next) {
			next = scan
		}
	}
	return next
}

func watchInputDevices() int {
	fd, err := unix.InotifyInit1(unix.IN_CLOEXEC | unix.IN_NONBLOCK)
	if err != nil {
		return -1
	}
	if _, err = unix.InotifyAddWatch(fd, "/dev/input", unix.IN_CREATE|unix.IN_DELETE|unix.IN_ATTRIB|unix.IN_MOVED_TO|unix.IN_MOVED_FROM|unix.IN_DELETE_SELF|unix.IN_MOVE_SELF); err != nil {
		unix.Close(fd)
		return -1
	}
	return fd
}

func (d *Driver) signalQueue() <-chan os.Signal {
	if d.eventSignals != nil {
		return d.eventSignals
	}
	return d.signals
}
