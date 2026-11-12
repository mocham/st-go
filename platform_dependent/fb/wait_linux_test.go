//go:build linux

package fb

import (
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func TestEventFDCoalescesAndInterruptsBlockingPoll(t *testing.T) {
	var wake inputWake
	fd, err := wake.init()
	if err != nil {
		t.Fatal(err)
	}
	defer wake.close()
	wake.signal()
	wake.signal()
	fds := []unix.PollFd{{Fd: int32(fd), Events: unix.POLLIN}}
	if n, err := unix.Poll(fds, 0); err != nil || n != 1 {
		t.Fatal(n, err)
	}
	var buf [8]byte
	unix.Read(fd, buf[:])
	if n, err := unix.Poll(fds, 0); err != nil || n != 0 {
		t.Fatal("mailbox not drained", n, err)
	}
	result := make(chan error, 1)
	go func() { _, err := unix.Poll(fds, -1); result <- err }()
	wake.signal()
	select {
	case err := <-result:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("eventfd did not interrupt indefinite poll")
	}
	wake.close()
	wake.signal() // late completion is harmless after descriptor closure
}

func TestMaintenanceOnlyHasRealDeadlines(t *testing.T) {
	now := time.Now()
	d := &Driver{active: true, mousePaths: "none", systemBar: &systemBar{nextRefresh: now.Add(30 * time.Second)}}
	if got := pollTimeout(now, d.maintenanceDeadline(now, false)); got != 30000 {
		t.Fatal(got)
	}
	d.active = false
	if got := pollTimeout(now, d.maintenanceDeadline(now, false)); got != -1 {
		t.Fatal("inactive VT polls", got)
	}
	d.active, d.systemBar = true, nil
	if got := pollTimeout(now, d.maintenanceDeadline(now, false)); got != -1 {
		t.Fatal("idle input polls", got)
	}
}

func TestSystemBarUIRefreshDoesNotPollDevices(t *testing.T) {
	d := memoryDriver(t, 960, 64)
	reads := 0
	d.systemBar = &systemBar{powerSupplyPath: t.TempDir(), audioOutput: func() (int, bool) { reads++; return 0, true }}
	now := time.Now()
	d.refreshSystemBar(now)
	for i := 1; i <= 29; i++ {
		d.systemBar.nextRefresh = time.Time{}
		d.refreshSystemBar(now.Add(time.Duration(i) * time.Second))
	}
	if reads != 1 {
		t.Fatal("UI changes polled hardware", reads)
	}
	d.refreshSystemBar(now.Add(30 * time.Second))
	if reads != 2 {
		t.Fatal("maintenance did not poll all status", reads)
	}
}
