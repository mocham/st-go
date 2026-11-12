//go:build linux

package fb

import (
	"errors"
	"fmt"
	"image"
	"os"
	"os/exec"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
	"st-go/platform_neutral/api"
)

type nativeVideoJob struct {
	request api.NativeVideoRequest
	done    func(error)
}

type nativeVideoOps struct {
	openTTY           func() (*os.File, error)
	restoreForeground func() error
	run               func(api.NativeVideoRequest, *os.File) error
}

func (d *Driver) StartNativeVideo(request api.NativeVideoRequest, done func(error)) error {
	if err := request.Validate(); err != nil {
		return err
	}
	for _, path := range []string{request.File, request.ALSAConfig} {
		info, err := os.Stat(path)
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() || (path == request.ALSAConfig && info.Size() > 128<<10) {
			return fmt.Errorf("native video requires regular media/config files")
		}
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.closed || !d.active || d.nativeBusy || done == nil {
		return fmt.Errorf("native console is unavailable or busy")
	}
	if d.nativeJobs == nil {
		d.nativeJobs = make(chan nativeVideoJob, 1)
	}
	d.nativeBusy = true
	d.nativeJobs <- nativeVideoJob{request, done}
	d.inputWake.signal()
	return nil
}

func (d *Driver) openNativeTTY() (*os.File, error) {
	foreground, err := unix.IoctlGetInt(d.console.fd, unix.TIOCGPGRP)
	if err != nil || foreground != unix.Getpgrp() {
		return nil, fmt.Errorf("st-fb no longer owns the console foreground group")
	}
	// Derive the physical VT from the validated kernel console number, never
	// from a client path or the browser's /dev/pts/N. Open a blocking description.
	path := fmt.Sprintf("/dev/tty%d", d.console.number)
	f, err := os.OpenFile(path, os.O_RDWR|unix.O_NOCTTY, 0)
	if err != nil {
		return nil, err
	}
	device, err := unix.IoctlGetInt(int(f.Fd()), unix.TIOCGDEV)
	if err != nil || unix.Major(uint64(uint32(device))) != 4 || int(unix.Minor(uint64(uint32(device)))) != d.console.number {
		f.Close()
		return nil, fmt.Errorf("native player tty does not match the owned VT")
	}
	return f, nil
}

// Run owns this operation; no console/evdev reads or scans run concurrently.
// Painter uploads may continue, but active=false keeps them out of scanout.
func (d *Driver) runNativeVideo(request api.NativeVideoRequest) (result error) {
	ops := d.nativeOps
	if ops == nil {
		ops = &nativeVideoOps{openTTY: d.openNativeTTY, run: d.runNativePlayer,
			restoreForeground: func() error {
				return unix.IoctlSetPointerInt(d.console.fd, unix.TIOCSPGRP, unix.Getpgrp())
			}}
	}
	tty, err := ops.openTTY()
	if err != nil {
		return err
	}
	defer tty.Close()
	cb := d.callbacks
	cb.Lock()
	owned, err := d.console.active()
	if err != nil || !owned || !d.isActive() {
		cb.Unlock()
		return fmt.Errorf("native video requires the active owned VT")
	}
	d.suspendPresentation()
	if cb.Visibility != nil {
		cb.Visibility(false)
	}
	for _, m := range d.mice {
		d.releaseMouse(m)
		m.grab(false)
		m.reset()
	}
	d.keyboard.reset()
	cb.Focus(false)
	cb.Unlock()

	defer func() {
		// Cover all recovery ioctls, including cleanup after a failed foreground
		// restore, so job control cannot stop the background host mid-rollback.
		if d.nativeOps == nil {
			ignored := signal.Ignored(unix.SIGTTOU)
			signal.Ignore(unix.SIGTTOU)
			defer func() {
				if !ignored {
					signal.Reset(unix.SIGTTOU)
				}
			}()
		}
		foregroundErr := ops.restoreForeground()
		if foregroundErr != nil {
			cb.Lock()
			cb.Close()
			cb.Unlock()
		}
		resumeErr := d.resumeNativeVideo()
		result = errors.Join(result, foregroundErr, resumeErr)
		if foregroundErr != nil || resumeErr != nil {
			cb.Lock()
			cb.Close()
			cb.Unlock()
		}
	}()
	// Return VT_AUTO, translated keyboard, saved termios and KD_TEXT before
	// mpv starts. Its /dev/tty now names this real VT, allowing its VT_PROCESS
	// switcher to release/reacquire DRM master on Ctrl-Alt-Fn.
	if err := d.console.restore(); err != nil {
		return err
	}
	unix.IoctlSetInt(d.console.fd, unix.TCFLSH, unix.TCIFLUSH)
	return ops.run(request, tty)
}

func (d *Driver) resumeNativeVideo() error {
	// Even a crashed player may have changed these kernel modes. Reassert the
	// saved text modes before re-entering; never trust its cleanup alone.
	c := d.console
	c.changedVT, c.changedKB, c.changedKD, c.changedTermios = true, true, true, true
	if err := c.restore(); err != nil {
		return err
	}
	d.mu.Lock()
	closed := d.closed
	d.mu.Unlock()
	if closed {
		return nil
	}
	l, err := d.screen.current()
	if err != nil {
		return err
	}
	if l != d.screen.layout {
		return fmt.Errorf("framebuffer mode changed during native video; restart st-fb")
	}
	if err := c.enter(); err != nil {
		return err
	}
	owned, err := c.active()
	if err != nil {
		return err
	}
	d.keyboard.reset()
	unix.IoctlSetInt(c.fd, unix.TCFLSH, unix.TCIFLUSH)
	for _, m := range d.mice {
		m.drain()
		m.reset()
		if owned && d.grabMouse {
			if err := m.grab(true); err != nil {
				return err
			}
		}
	}
	d.callbacks.Lock()
	d.mu.Lock()
	d.active, d.restorePixels = owned, owned
	if d.systemBar != nil {
		d.systemBar.nextRefresh, d.systemBar.nextPoll = time.Time{}, time.Time{}
	}
	d.invalidate(image.Rect(0, 0, l.width, l.height))
	d.mu.Unlock()
	if d.callbacks.Visibility != nil {
		d.callbacks.Visibility(owned)
	}
	d.callbacks.Focus(owned)
	d.callbacks.Unlock()
	return nil
}

func nativePlayerCommand(request api.NativeVideoRequest, tty *os.File) *exec.Cmd {
	cmd := exec.Command("mpv", "--vo=drm", "--hwdec=auto-copy", "--ao=alsa", "--audio-device="+request.AudioDevice, "--volume="+strconv.Itoa(request.Volume), "--", request.File)
	cmd.Env = append(os.Environ(), "ALSA_CONFIG_PATH="+request.ALSAConfig)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = tty, tty, tty
	// Inherit st-fb's real controlling terminal/session, but give the player
	// its own foreground group. Do not create a PTY or a new session here.
	cmd.SysProcAttr = &syscall.SysProcAttr{Foreground: true, Ctty: int(tty.Fd())}
	return cmd
}

func (d *Driver) runNativePlayer(request api.NativeVideoRequest, tty *os.File) error {
	cmd := nativePlayerCommand(request, tty)
	if err := cmd.Start(); err != nil {
		return err
	}
	wait := make(chan error, 1)
	go func() { wait <- cmd.Wait() }()
	var kill <-chan time.Time
	var timer *time.Timer
	defer func() {
		if timer != nil {
			timer.Stop()
		}
	}()
	done := d.done
	watch := time.NewTicker(250 * time.Millisecond)
	defer watch.Stop()
	stopping := false
	stop := func() {
		if stopping {
			return
		}
		stopping = true
		unix.Kill(-cmd.Process.Pid, unix.SIGCONT)
		unix.Kill(-cmd.Process.Pid, unix.SIGTERM)
		timer = time.NewTimer(2 * time.Second)
		kill = timer.C
	}
	for {
		select {
		case err := <-wait:
			return err
		case <-done:
			done = nil
			stop()
		case <-watch.C:
			// A stopped player must not strand the physical VT in a dead
			// foreground group. Inspect without reaping (Cmd.Wait owns exit).
			var info unix.Siginfo
			if unix.Waitid(unix.P_PID, cmd.Process.Pid, &info, unix.WSTOPPED|unix.WNOHANG|unix.WNOWAIT, nil) == nil && info.Code == 5 {
				stop() // CLD_STOPPED
			}
		case <-kill:
			unix.Kill(-cmd.Process.Pid, unix.SIGKILL)
			kill = nil
		case sig := <-d.signalQueue():
			if sig != unix.SIGUSR1 && sig != unix.SIGUSR2 {
				d.callbacks.Lock()
				d.callbacks.Close()
				d.callbacks.Unlock()
			}
		}
	}
}
