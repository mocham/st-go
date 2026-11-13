//go:build linux

package fb

import (
	"flag"
	"fmt"
	"image"
	"log"
	"os"
	"os/signal"
	"strconv"
	"sync"
	"time"
	"unicode/utf8"
	"unsafe"

	"golang.org/x/sys/unix"
	"st-go/audio"
	"st-go/brightness"
	"st-go/limits"
	"st-go/platform_neutral/api"
	"st-go/platform_neutral/sharedclipboard"
)

type Options struct {
	Device, TTY, Mouse string
	GrabMouse          bool
}

func RegisterFlags(fs *flag.FlagSet) api.OpenHost {
	o := Options{}
	o.RegisterFlags(fs)
	return func(window api.WindowOptions) (api.Host, error) { return New(window, o) }
}

func (o *Options) RegisterFlags(fs *flag.FlagSet) {
	fs.StringVar(&o.Device, "fb", "/dev/fb0", "Linux framebuffer device")
	fs.StringVar(&o.TTY, "tty", "/dev/tty", "active Linux virtual console (not a PTY)")
	fs.StringVar(&o.Mouse, "mouse", "auto", "evdev mouse/touchpad paths (comma separated), auto, or none")
	fs.BoolVar(&o.GrabMouse, "mouse-grab", false, "exclusively grab selected pointer devices while the VT is active")
	fs.BoolFunc("mouse-list", "list mouse devices, access errors and capabilities without opening the framebuffer", func(value string) error {
		enabled, err := strconv.ParseBool(value)
		if err != nil {
			return err
		}
		if !enabled {
			return nil
		}
		if err := writeMouseList(os.Stdout); err != nil {
			return err
		}
		// Like -help, this diagnostic exits during flag parsing, before fonts,
		// framebuffer/VT takeover, grabs or child-process startup.
		os.Exit(0)
		return nil
	})
}

type Driver struct {
	mu                      sync.Mutex
	presentMu               sync.Mutex
	presentWake             chan struct{}
	presentDone             chan struct{}
	damage                  []image.Rectangle
	paintDepth              int
	backBuffer              []byte
	callbacks               api.Callbacks
	window                  api.WindowOptions
	screen                  *framebuffer
	console                 *console
	keyboard                *keyboard
	pixels                  []uint32
	textCursor              api.TextCursor
	surfaces                []*surface
	clipboard               map[byte]string
	clipboardSync           *sharedclipboard.Sync
	pointer                 image.Point
	pointerVisible          bool
	systemBar               *systemBar
	active, closed, running bool
	restorePixels           bool
	done                    chan struct{}
	signals                 chan os.Signal
	eventSignals            chan os.Signal
	inputWake               inputWake
	mice                    []*mouse
	mousePaths              string
	grabMouse               bool
	lastScan                time.Time
	buttonCounts            [12]int
	screenshot              screenshotState
	recording               *screenRecording
	recordingFailed         bool
	mouseFailures           map[string]string
	nativeJobs              chan nativeVideoJob
	nativeBusy              bool
	nativeOps               *nativeVideoOps
}

var _ api.Host = (*Driver)(nil)

func New(window api.WindowOptions, o Options) (driver *Driver, err error) {
	if window.InitialAudioVolume < 0 || window.InitialAudioVolume > systemBarMaxInitialVolume {
		return nil, fmt.Errorf("initial audio volume must be between 0 and %d percent", systemBarMaxInitialVolume)
	}
	if o.Device == "" {
		o.Device = "/dev/fb0"
	}
	if o.TTY == "" {
		o.TTY = "/dev/tty"
	}
	if o.Mouse == "" {
		o.Mouse = "auto"
	}
	f, err := openFramebuffer(o.Device)
	if err != nil {
		return nil, fmt.Errorf("fb %s: %w", o.Device, err)
	}
	d := &Driver{window: window, screen: f, pointer: image.Pt(f.layout.width/2, f.layout.height/2), systemBar: &systemBar{powerSupplyPath: "/sys/class/power_supply", audioOutput: audio.SelectedOutput, setAudioOutput: audio.SetOutput, setAudioVolume: audio.SetVolume, volumeLevel: window.InitialAudioVolume, brightness: brightness.Read, changeBrightness: brightness.Change}, done: make(chan struct{}), signals: make(chan os.Signal, 16), clipboard: make(map[byte]string), mousePaths: o.Mouse, grabMouse: o.GrabMouse}
	d.clipboardSync = sharedclipboard.New(window.CachePath, sharedclipboard.Hooks{Changed: d.sharedClipboardChanged, Status: d.sharedClipboardStatus})
	defer func() {
		if err != nil {
			d.cleanup()
		}
	}()
	w, h := f.layout.width, f.layout.height
	if window.Border < 0 || window.Border > 65535 || window.CellWidth < 1 || window.CellHeight < 1 ||
		w < 2*window.Border+window.CellWidth || h < 2*window.Border+window.CellHeight || !limits.Grid((w-2*window.Border)/window.CellWidth, (h-2*window.Border)/window.CellHeight) {
		return nil, fmt.Errorf("physical framebuffer cannot fit the terminal cell geometry")
	}
	d.pixels = make([]uint32, w*h)
	d.console, err = openConsole(o.TTY)
	if err != nil {
		return nil, fmt.Errorf("console %s: %w", o.TTY, err)
	}
	// The fb selected must actually back this VT, not a different GPU's output.
	var mapping struct{ Console, Framebuffer uint32 }
	mapping.Console = uint32(d.console.number)
	if err = ioctlPointer(f.fd, 0x460f, unsafe.Pointer(&mapping)); err != nil {
		return nil, fmt.Errorf("FBIOGET_CON2FBMAP: %w", err)
	}
	var stat unix.Stat_t
	if err = unix.Fstat(f.fd, &stat); err != nil {
		return nil, err
	}
	if unix.Major(uint64(stat.Rdev)) != 29 || uint32(unix.Minor(uint64(stat.Rdev))) != mapping.Framebuffer {
		return nil, fmt.Errorf("framebuffer does not back the selected console")
	}
	d.keyboard, err = d.console.keyboard()
	if err != nil {
		return nil, err
	}
	if err = d.scanMice(true); err != nil {
		return nil, err
	}
	// Establish one bounded mixer level before the initial bar snapshot. Failure
	// leaves playback untouched while the controls retain the configured value.
	audio.SetVolume(window.InitialAudioVolume)
	d.refreshSystemBar(time.Now())
	signal.Notify(d.signals, unix.SIGINT, unix.SIGTERM, unix.SIGHUP, unix.SIGQUIT, unix.SIGUSR1, unix.SIGUSR2)
	if err = d.console.enter(); err != nil {
		return nil, fmt.Errorf("console takeover: %w", err)
	}
	d.active = true
	d.restorePixels = true
	if d.grabMouse {
		for _, m := range d.mice {
			if err = m.grab(true); err != nil {
				return nil, fmt.Errorf("grab mouse: %w", err)
			}
		}
	}
	return d, nil
}

func (d *Driver) Bind(c api.Callbacks) {
	keyPress := c.KeyPress
	c.KeyPress = func(key uint, modifiers uint16) {
		if d.systemBarMediaKey(key, modifiers) {
			return
		}
		if key == '0' && modifiers == altMask && d.focusSystemBar() {
			return
		}
		if keyPress != nil {
			keyPress(key, modifiers)
		}
	}
	press, release := c.PointerPress, c.PointerRelease
	c.PointerPress = func(button byte, modifiers uint16, x, y int) {
		if d.screenshotPress(button, image.Pt(x, y)) {
			return
		}
		if press != nil {
			press(button, modifiers, x, y)
		}
	}
	c.PointerRelease = func(button byte, modifiers uint16, x, y int) {
		if d.screenshotRelease(button) {
			return
		}
		if release != nil {
			release(button, modifiers, x, y)
		}
	}
	d.callbacks = c
}
func (d *Driver) BackendName() string           { return "fb" }
func (d *Driver) SetupKeys()                    {}
func (d *Driver) SetTitle(string)               {}
func (d *Driver) SetIconTitle(string)           {}
func (d *Driver) SupportsWindowGeometry() bool  { return false }
func (d *Driver) RequestWindowRect(api.Rect)    {}
func (d *Driver) ScreenSize() (int, int)        { return d.screen.layout.width, d.screen.layout.height }
func (d *Driver) ClientSize() (int, int, error) { w, h := d.ScreenSize(); return w, h, nil }
func (d *Driver) WindowRect() (api.Rect, error) {
	w, h := d.ScreenSize()
	return api.Rect{Width: w, Height: h}, nil
}

func (d *Driver) SetClipboard(selection byte, text string) {
	if (selection != 'c' && selection != 'p') || len(text) > limits.ClipboardBytes || !utf8.ValidString(text) {
		return
	}
	d.mu.Lock()
	if !d.closed {
		d.clipboard[selection] = text
	}
	sync := d.clipboardSync
	d.mu.Unlock()
	if sync != nil {
		sync.Publish(selection, text, sharedclipboard.SourceLocal, "")
	}
}
func (d *Driver) QueryClipboard(selection byte, reply func(string, bool)) {
	if d.clipboardSync != nil && d.clipboardSync.State().Enabled {
		d.clipboardSync.Query(selection, func(text string, ok bool) {
			if ok {
				reply(text, true)
				return
			}
			d.mu.Lock()
			text, ok = d.clipboard[selection]
			closed := d.closed
			d.mu.Unlock()
			reply(text, ok && !closed)
		})
		return
	}
	d.mu.Lock()
	text, ok := d.clipboard[selection]
	closed := d.closed
	d.mu.Unlock()
	reply(text, ok && !closed)
}

func (d *Driver) ClipboardSyncState() api.ClipboardSyncState {
	if d.clipboardSync == nil {
		return api.ClipboardSyncState{}
	}
	state := d.clipboardSync.State()
	return api.ClipboardSyncState{Enabled: state.Enabled, Ready: state.Ready, Available: state.Available}
}

func (d *Driver) SetClipboardSync(enabled bool) bool {
	if d.clipboardSync == nil {
		return false
	}
	d.mu.Lock()
	seed := make(map[byte]string, len(d.clipboard))
	for selection, text := range d.clipboard {
		seed[selection] = text
	}
	d.mu.Unlock()
	return d.clipboardSync.SetEnabled(enabled, seed)
}

func (d *Driver) sharedClipboardChanged(entry sharedclipboard.Entry) {
	d.mu.Lock()
	if !d.closed {
		d.clipboard[entry.Selection] = entry.Text
		if d.systemBar != nil {
			d.systemBar.nextRefresh = time.Time{}
		}
	}
	d.mu.Unlock()
	d.inputWake.signal()
}

func (d *Driver) sharedClipboardStatus(sharedclipboard.State) {
	d.mu.Lock()
	if d.systemBar != nil {
		d.systemBar.nextRefresh = time.Time{}
	}
	d.mu.Unlock()
	d.inputWake.signal()
}
func (d *Driver) RequestPaste() {
	d.QueryClipboard('c', func(text string, ok bool) {
		if ok {
			d.callbacks.Paste(text)
		}
	})
}

// Close never waits for Run while its caller holds the terminal lock. Run owns
// cleanup once started; otherwise startup failures restore immediately.
func (d *Driver) Close() {
	d.mu.Lock()
	if d.closed {
		d.mu.Unlock()
		return
	}
	d.closed = true
	close(d.done)
	running := d.running
	d.mu.Unlock()
	d.inputWake.signal()
	if !running {
		d.presentMu.Lock()
		d.cleanup()
		d.presentMu.Unlock()
	}
}

func (d *Driver) cleanup() {
	d.finishRecording()
	d.inputWake.close()
	if d.clipboardSync != nil {
		d.clipboardSync.Close()
	}
	for _, m := range d.mice {
		m.close()
	}
	d.mice = nil
	if d.console != nil {
		// Never restore saved pixels over a different VT's display.
		if active, err := d.console.active(); err == nil && active && d.restorePixels && d.screen != nil {
			d.screen.restore()
		}
		if err := d.console.restore(); err != nil {
			log.Printf("fb console restore: %v", err)
		}
		if d.console.fd >= 0 {
			unix.Close(d.console.fd)
		}
	}
	if d.screen != nil && d.screen.fd >= 0 {
		if err := d.screen.close(); err != nil {
			log.Printf("fb close: %v", err)
		}
	}
	if d.signals != nil {
		signal.Stop(d.signals)
	}
}

func (d *Driver) Run() {
	d.mu.Lock()
	if d.closed {
		d.mu.Unlock()
		return
	}
	d.running = true
	d.mu.Unlock()
	defer func() {
		d.Close()
		if d.presentDone != nil {
			<-d.presentDone
		}
		d.presentMu.Lock()
		d.cleanup()
		d.presentMu.Unlock()
	}()
	cb := d.callbacks
	wakeFD, err := d.inputWake.init()
	if err != nil {
		log.Printf("fb eventfd: %v", err)
		cb.Lock()
		cb.Close()
		cb.Unlock()
		return
	}
	d.eventSignals = make(chan os.Signal, 16)
	relayDone := make(chan struct{})
	go func() {
		defer close(relayDone)
		for {
			select {
			case <-d.done:
				return
			case sig := <-d.signals:
				select {
				case d.eventSignals <- sig:
					d.inputWake.signal()
				case <-d.done:
					return
				}
			}
		}
	}()
	defer func() { d.Close(); <-relayDone }()
	watchFD := -1
	if d.mousePaths != "none" {
		watchFD = watchInputDevices()
	}
	defer func() {
		if watchFD >= 0 {
			unix.Close(watchFD)
		}
	}()
	fds := make([]unix.PollFd, 0, 19)
	cb.Lock()
	if cb.Closed() {
		cb.Unlock()
		return
	}
	w, h := d.ScreenSize()
	cb.Resize(w, h)
	cb.Focus(true)
	cb.PaintDirty()
	cb.Unlock()
	d.startPresenter()
	for {
		select {
		case <-d.done:
			return
		default:
		}
		// Process ownership signals before input or another scanout write.
		select {
		case sig := <-d.eventSignals:
			cb.Lock()
			if sig == unix.SIGUSR1 {
				d.release()
			} else if sig == unix.SIGUSR2 {
				if err := d.acquire(); err != nil {
					log.Printf("fb VT acquire: %v", err)
					cb.Close()
				}
			} else {
				cb.Close()
			}
			cb.Unlock()
			continue
		default:
		}
		d.mu.Lock()
		jobs := d.nativeJobs
		d.mu.Unlock()
		select {
		case job := <-jobs:
			err := d.runNativeVideo(job.request)
			d.mu.Lock()
			d.nativeBusy = false
			d.mu.Unlock()
			job.done(err)
			continue
		default:
		}
		if d.isActive() && d.refreshSystemBar(time.Now()) {
			cb.Lock()
			if !cb.Closed() {
				cb.PaintDirty()
			}
			cb.Unlock()
		}
		if d.isActive() && watchFD < 0 && d.mousePaths != "none" && time.Since(d.lastScan) >= systemBarInterval {
			if err := d.scanMice(false); err != nil {
				limits.Report("fb mouse", "%v", err)
			}
		}
		fds = append(fds[:0], unix.PollFd{Fd: int32(d.console.fd), Events: unix.POLLIN})
		for _, m := range d.mice {
			fds = append(fds, unix.PollFd{Fd: int32(m.fd), Events: unix.POLLIN})
		}
		wakeIndex := len(fds)
		fds = append(fds, unix.PollFd{Fd: int32(wakeFD), Events: unix.POLLIN}, unix.PollFd{Fd: int32(watchFD), Events: unix.POLLIN})
		now := time.Now()
		_, err := unix.Poll(fds, pollTimeout(now, d.maintenanceDeadline(now, watchFD >= 0)))
		if err == unix.EINTR {
			continue
		}
		if err != nil {
			log.Printf("fb input: %v", err)
			cb.Lock()
			cb.Close()
			cb.Unlock()
			return
		}
		// A switch signal may have arrived while poll was waiting. Defer input
		// until the signal is acknowledged; queued events are discarded on switch.
		if fds[wakeIndex].Revents != 0 {
			var data [8]byte
			unix.Read(wakeFD, data[:])
		}
		if len(d.eventSignals) != 0 {
			continue
		}
		cb.Lock()
		if cb.Closed() {
			cb.Unlock()
			return
		}
		if fds[0].Revents&(unix.POLLHUP|unix.POLLERR|unix.POLLNVAL) != 0 {
			cb.Close()
			cb.Unlock()
			return
		}
		if fds[0].Revents&unix.POLLIN != 0 {
			var buf [256]byte
			n, err := unix.Read(d.console.fd, buf[:])
			if n > 0 && d.isActive() {
				d.keyboard.feed(buf[:n], cb, func(vt int) {
					if err := d.console.ops.value(vtActivate, uintptr(vt)); err != nil {
						limits.Report("fb VT", "%v", err)
					}
				}, cb.Close)
			}
			if err != nil && err != unix.EAGAIN && err != unix.EINTR {
				cb.Close()
			}
		}
		for i := len(d.mice) - 1; i >= 0; i-- {
			if fds[i+1].Revents == 0 {
				continue
			}
			m := d.mice[i]
			if fds[i+1].Revents&(unix.POLLHUP|unix.POLLERR|unix.POLLNVAL) != 0 {
				d.reportMouseFailure(m.path, fmt.Sprintf("device disconnected (poll flags %#x)", fds[i+1].Revents))
				d.removeMouse(i)
			} else if err := m.read(d); err != nil {
				d.reportMouseFailure(m.path, fmt.Sprintf("input read/decode failed: %v", err))
				d.removeMouse(i)
			}
		}
		if !cb.Closed() {
			cb.PaintDirty()
		}
		cb.Unlock()
		if fds[wakeIndex+1].Revents != 0 {
			var events [4096]byte
			unix.Read(watchFD, events[:])
			// Recreate the watch too: /dev/input itself may have been replaced.
			unix.Close(watchFD)
			watchFD = watchInputDevices()
			if d.isActive() {
				if err := d.scanMice(false); err != nil {
					limits.Report("fb mouse", "%v", err)
				}
			}
		}
	}
}

func (d *Driver) isActive() bool { d.mu.Lock(); defer d.mu.Unlock(); return d.active && !d.closed }

func (d *Driver) release() {
	d.suspendPresentation()
	if d.callbacks.Visibility != nil {
		d.callbacks.Visibility(false)
	}
	for _, m := range d.mice {
		d.releaseMouse(m)
		m.grab(false)
		m.reset()
	}
	d.keyboard.reset()
	d.callbacks.Focus(false)
	if err := d.console.ops.value(vtRelease, 1); err != nil {
		// Failure means ownership is uncertain. Close safely instead of painting.
		limits.Report("fb VT release", "%v", err)
		d.callbacks.Close()
	}
}
func (d *Driver) acquire() error {
	d.suspendPresentation()
	active, err := d.console.active()
	if err != nil {
		return err
	}
	if !active {
		return fmt.Errorf("acquire for an inactive VT")
	}
	if err := d.console.ops.value(vtRelease, 2); err != nil {
		return err
	}
	l, err := d.screen.current()
	if err != nil {
		return err
	}
	if l != d.screen.layout {
		return fmt.Errorf("framebuffer mode changed while away; restart st-fb")
	}
	d.keyboard.reset()
	// Flush physical input queued while another console owned the seat.
	unix.IoctlSetInt(d.console.fd, unix.TCFLSH, unix.TCIFLUSH)
	for i := len(d.mice) - 1; i >= 0; i-- {
		m := d.mice[i]
		m.drain()
		m.reset()
		if d.grabMouse {
			if err := m.grab(true); err != nil {
				limits.Report("fb mouse regrab", "%s: %v", m.path, err)
				d.removeMouse(i)
			}
		}
	}
	d.mu.Lock()
	d.active = true
	d.restorePixels = true
	if d.systemBar != nil {
		d.systemBar.nextRefresh, d.systemBar.nextPoll = time.Time{}, time.Time{}
	}
	d.invalidate(image.Rect(0, 0, l.width, l.height))
	d.mu.Unlock()
	if err := d.scanMice(false); err != nil {
		limits.Report("fb mouse", "%v", err)
	}
	if d.callbacks.Visibility != nil {
		d.callbacks.Visibility(true)
	}
	d.callbacks.Focus(true)
	return nil
}
