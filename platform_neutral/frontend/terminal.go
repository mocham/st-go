package frontend

import (
	"fmt"
	"log"
	"os"
	"strings"
	"sync"
	"sync/atomic"

	"st-go/audio"
	"st-go/config"
	"st-go/platform_neutral/api"
	term "st-go/platform_neutral/stterminal"
	"st-go/widgets"
)

const (
	ColDefaultFg  = 258
	ColDefaultBg  = 259
	ColDefaultCs  = 256
	ColDefaultRcs = 257
)

type Color struct{ argb, pixel uint32 }

// Terminal owns backend-neutral terminal presentation and protocol state.
// Native display resources and event decoding are accessed only through api.Host;
// session-owned audio is a separate reusable service.
type Terminal struct {
	host                              api.Host
	fbW, fbH                          int
	framebuf                          []uint32
	imageAtlas                        []uint32
	imageSlots                        []*imageSlot
	imageStampSeq                     uint64
	msel                              mouseSel
	buttons                           uint
	session                           *ptySession
	tabs                              terminalTabManager
	sessionCleanup                    func(error)
	cols, rows, windowCols            int
	cw, ch, baseline, borderpx        int
	ratio                             float64
	x, y                              int
	geomMask                          XGeometryMask
	isFixed                           bool
	colors                            []Color
	cfg                               *config.Config
	webpCache                         *webPCache
	webpAnimDecoder                   *webPAnimDecoder
	audio                             nativeAudioPlayer
	title, iconTitle                  string
	lockTitle                         bool
	cursorShape, cursorThick          int
	textCursor                        api.TextCursor
	winMode, ignoreMod                uint
	forceMouseMod                     uint16
	doubleClick, tripleClick, blinkMs uint
	termCore                          *term.Term
	inPTYWrite, ptyPaintRequested     bool
	done                              chan struct{}
	ttyResize                         func(rows, cols int)
	displayRegions                    displayRegionState
	pdfSearch                         pdfSearchState
	keys                              []keyDef
	shortcuts                         []shortcut
	mshortcuts                        []mShortcut
	geometryTags                      map[string]windowRect
	restoreGeometryTag                string
	suppressRestoreButton             bool
	regionButtons                     uint
	mu                                sync.Mutex
	isClosed                          int32
}

type shortcut struct {
	keysym      uint
	mask        int
	action, arg string
}
type mShortcut struct {
	mask        int
	button      byte
	action, arg string
	release     bool
}

var openWindow api.OpenHost

func NewTerminal(cfg *config.Config) (*Terminal, error) {
	return NewTerminalOpts(cfg, 1, 0, 0, 0, "st", "", "", false)
}

func NewTerminalOpts(cfg *config.Config, ratio float64, x, y int, geomMask XGeometryMask, title, instance, className string, fixed bool) (*Terminal, error) {
	if ratio <= 0 {
		ratio = 1
	}
	if title == "" {
		title = "st"
	}
	if instance == "" {
		instance = cfg.Termname
	}
	if className == "" {
		className = cfg.Termname
	}
	t := newTerminalState(cfg, ratio, title)
	t.x, t.y, t.geomMask, t.isFixed = x, y, geomMask, fixed
	if openWindow == nil {
		return nil, fmt.Errorf("no display backend configured")
	}
	host, err := openWindow(api.WindowOptions{Cols: t.cols, Rows: t.rows, CellWidth: t.cw, CellHeight: t.ch, Border: t.borderpx,
		X: x, Y: y, GeometryMask: int(geomMask), Title: title, Instance: instance, Class: className, Fixed: fixed,
		InitialAudioVolume: cfg.SystemBar.InitialVolume})
	if err != nil {
		return nil, err
	}
	widget, err := widgets.Wrap(host)
	if err != nil {
		host.Close()
		return nil, err
	}
	if err := t.bindWidget(widget); err != nil {
		widget.Close()
		return nil, err
	}
	widget.Show()
	return t, nil
}

// NewTerminalWidget binds a terminal to an existing rectangular widget. It
// does not create another native window or select any tab/tiling policy.
// Fonts are the process's existing shared font bundle; all mutable terminal,
// image, PDF, mouse and PTY state is private to the returned terminal.
func NewTerminalWidget(cfg *config.Config, widget api.Widget, ratio float64) (*Terminal, error) {
	if widget == nil || widget.Kind() != api.TerminalWidget {
		return nil, fmt.Errorf("terminal widget required")
	}
	if ratio <= 0 {
		ratio = 1
	}
	t := newTerminalState(cfg, ratio, "st")
	t.attachTerm(term.NewTerm(cfg, t))
	t.loadInputConfig(cfg)
	if err := t.bindWidget(widget); err != nil {
		return nil, err
	}
	w, h, _ := widget.ClientSize()
	t.mu.Lock()
	t.resize(w, h)
	t.mu.Unlock()
	return t, nil
}

func newTerminalState(cfg *config.Config, ratio float64, title string) *Terminal {
	return &Terminal{
		cfg: cfg, cols: int(cfg.Cols), rows: int(cfg.Rows), ratio: ratio,
		cw: max(1, int(float64(cfg.GlyphWidth)*ratio)), ch: max(1, int(float64(cfg.GlyphHeight)*ratio)),
		baseline: int(float64(cfg.GlyphBaseline) * ratio), borderpx: cfg.Borderpx,
		title: title, iconTitle: title,
		cursorShape: int(cfg.CursorShape), cursorThick: int(cfg.CursorThick),
		ignoreMod: cfg.IgnoreMod, forceMouseMod: uint16(cfg.ForceMouseMod),
		doubleClick: cfg.DoubleClickMs, tripleClick: cfg.TripleClickMs, blinkMs: cfg.BlinkTimeout,
		geometryTags: make(map[string]windowRect), done: make(chan struct{}), audio: &audio.Player{},
	}
}

func (t *Terminal) bindWidget(widget api.Widget) error {
	if !t.ensureFramebuffer(t.borderpx*2+t.cols*t.cw, t.borderpx*2+t.rows*t.ch) {
		return fmt.Errorf("invalid terminal framebuffer size")
	}
	t.host = widget
	t.loadColors(t.cfg)
	t.clearFramebuffer()
	if t.cfg.WebPCachePath != "" {
		cachePath := strings.ReplaceAll(t.cfg.WebPCachePath, "{uid}", fmt.Sprint(os.Getuid()))
		cache, err := openWebPCache(expandHome(cachePath))
		if err != nil {
			log.Printf("webp cache: %v", err)
		} else {
			t.webpCache = cache
		}
	}
	widget.Bind(t.eventCallbacks())
	widget.SetTitle(t.title)
	widget.SetIconTitle(t.iconTitle)
	return nil
}

func (t *Terminal) Widget() api.Widget { widget, _ := t.host.(api.Widget); return widget }

// Run services this terminal's timers until its widget/session is closed.
func (t *Terminal) Run() { t.run() }

func (t *Terminal) closed() bool { return atomic.LoadInt32(&t.isClosed) != 0 }
func (t *Terminal) actualRowsCols() (rows, cols int) {
	w, h, err := t.host.ClientSize()
	if err != nil {
		return t.rows, t.cols
	}
	return max(1, (h-2*t.borderpx)/t.ch), max(1, (w-2*t.borderpx)/t.cw)
}
func (t *Terminal) setTitle(s string) {
	if s == "" || t.lockTitle {
		return
	}
	t.title = s
	t.host.SetTitle(s)
	if t.tabs != nil {
		t.tabs.titleChanged(t.Widget().ID(), s)
	}
}
func (t *Terminal) SetMode(set bool, mode uint) { t.setWinMode(set, mode) }
func (t *Terminal) SetPointerMotion(on bool)    {}
func (t *Terminal) LoadCols() {
	if t.cfg != nil {
		t.loadColors(t.cfg)
	}
}
func (t *Terminal) SetCursor(shape int) bool {
	if shape < 0 || shape > 7 {
		return true
	}
	t.cursorShape = shape
	return false
}
func (t *Terminal) setWinMode(set bool, mode uint) {
	old := t.winMode
	if set {
		t.winMode |= mode
	} else {
		t.winMode &^= mode
	}
	if (t.winMode&term.ModeReverse) != (old&term.ModeReverse) && t.termCore != nil {
		t.termCore.Redraw()
	}
}
func (t *Terminal) winModeIs(flag uint) bool { return t.winMode&flag != 0 }
func (t *Terminal) StartDraw() bool          { return true }

// Background PTY exit paths must return through Main's deferred cleanup, not
// os.Exit: console hosts have keyboard/display modes to restore.
func (t *Terminal) closeSession() { t.mu.Lock(); defer t.mu.Unlock(); t.Close() }

func (t *Terminal) Close() {
	if !atomic.CompareAndSwapInt32(&t.isClosed, 0, 1) {
		return
	}
	if t.done != nil {
		close(t.done)
	}
	if t.session != nil {
		t.session.close()
	}
	if t.audio != nil {
		t.audio.Close()
	}
	t.closeRegionPresenter()
	if t.webpCache != nil {
		t.webpCache.close()
	}
	if t.webpAnimDecoder != nil {
		t.webpAnimDecoder.close()
	}
	if t.host != nil {
		t.host.Close()
	}
	if t.tabs != nil {
		t.tabs.terminalClosed(t.Widget().ID())
	}
}
func expandHome(p string) string {
	if strings.HasPrefix(p, "~") {
		home, _ := os.UserHomeDir()
		return home + p[1:]
	}
	return p
}
