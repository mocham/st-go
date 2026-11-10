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
	"st-go/limits"
	"st-go/platform_neutral/api"
)

type Options struct {
	Device, TTY, Mouse string
	GrabMouse          bool
}

func RegisterFlags(fs *flag.FlagSet) api.OpenHost {
	o := Options{}
	fs.StringVar(&o.Device, "fb", "/dev/fb0", "Linux framebuffer device")
	fs.StringVar(&o.TTY, "tty", "/dev/tty", "active Linux virtual console (not a PTY)")
	fs.StringVar(&o.Mouse, "mouse", "auto", "evdev mouse paths (comma separated), auto, or none")
	fs.BoolVar(&o.GrabMouse, "mouse-grab", false, "exclusively grab selected mice while the VT is active")
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
	return func(window api.WindowOptions) (api.Host, error) { return New(window, o) }
}

type Driver struct {
	mu                      sync.Mutex
	callbacks               api.Callbacks
	window                  api.WindowOptions
	screen                  *framebuffer
	console                 *console
	keyboard                *keyboard
	pixels                  []uint32
	surfaces                []*surface
	clipboard               map[byte]string
	pointer                 image.Point
	pointerVisible          bool
	systemBar               *systemBar
	active, closed, running bool
	restorePixels           bool
	done                    chan struct{}
	signals                 chan os.Signal
	mice                    []*mouse
	mousePaths              string
	grabMouse               bool
	lastScan                time.Time
	buttonCounts            [12]int
	mouseFailures           map[string]string
}

var _ api.Host = (*Driver)(nil)

func New(window api.WindowOptions, o Options) (driver *Driver, err error) {
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
	d := &Driver{window: window, screen: f, pointer: image.Pt(f.layout.width/2, f.layout.height/2), systemBar: &systemBar{powerSupplyPath: "/sys/class/power_supply"}, done: make(chan struct{}), signals: make(chan os.Signal, 16), clipboard: make(map[byte]string), mousePaths: o.Mouse, grabMouse: o.GrabMouse}
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

func (d *Driver) Bind(c api.Callbacks)          { d.callbacks = c }
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
	defer d.mu.Unlock()
	if !d.closed {
		d.clipboard[selection] = text
	}
}
func (d *Driver) QueryClipboard(selection byte, reply func(string, bool)) {
	d.mu.Lock()
	text, ok := d.clipboard[selection]
	closed := d.closed
	d.mu.Unlock()
	reply(text, ok && !closed)
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
	if !d.running {
		d.cleanup()
	}
	d.mu.Unlock()
}

func (d *Driver) cleanup() {
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
	defer func() { d.mu.Lock(); d.cleanup(); d.closed = true; d.mu.Unlock() }()
	cb := d.callbacks
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
	for {
		select {
		case <-d.done:
			return
		default:
		}
		// Process ownership signals before input or another scanout write.
		select {
		case sig := <-d.signals:
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
		d.refreshSystemBar(time.Now())
		if time.Since(d.lastScan) > 2*time.Second {
			if err := d.scanMice(false); err != nil {
				limits.Report("fb mouse", "%v", err)
			}
		}
		fds := []unix.PollFd{{Fd: int32(d.console.fd), Events: unix.POLLIN}}
		for _, m := range d.mice {
			fds = append(fds, unix.PollFd{Fd: int32(m.fd), Events: unix.POLLIN})
		}
		_, err := unix.Poll(fds, 40)
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
		if len(d.signals) != 0 {
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
	}
}

func (d *Driver) isActive() bool { d.mu.Lock(); defer d.mu.Unlock(); return d.active && !d.closed }

func (d *Driver) release() {
	d.mu.Lock()
	d.active = false
	d.restorePixels = false
	d.mu.Unlock()
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
	d.mu.Lock()
	d.active = false
	d.restorePixels = false
	d.mu.Unlock()
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
	d.paintRect(image.Rect(0, 0, l.width, l.height))
	d.mu.Unlock()
	d.callbacks.Focus(true)
	return nil
}
