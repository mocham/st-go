//go:build linux

package fb

import (
	"bytes"
	"crypto/sha256"
	"fmt"
	"image"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
	"unsafe"

	"golang.org/x/sys/unix"
	"st-go/platform_neutral/api"
	"st-go/platform_neutral/sharedclipboard"
)

func writePowerSupply(t *testing.T, root, name string, values map[string]string) string {
	t.Helper()
	base := filepath.Join(root, name)
	if err := os.MkdirAll(base, 0700); err != nil {
		t.Fatal(err)
	}
	for key, value := range values {
		if err := os.WriteFile(filepath.Join(base, key), []byte(value), 0600); err != nil {
			t.Fatal(err)
		}
	}
	return base
}

func TestSystemBatteryDiscoveryAndValidation(t *testing.T) {
	for _, tc := range []struct {
		name   string
		values map[string]string
		want   int
		wantOK bool
	}{
		{"normal", map[string]string{"type": "Battery\n", "capacity": "73\n", "present": "1", "scope": "System"}, 73, true},
		{"empty", map[string]string{"type": "Battery", "capacity": "0"}, 0, true},
		{"full", map[string]string{"type": "Battery", "capacity": "100"}, 100, true},
		{"absent", map[string]string{"type": "Battery", "capacity": "73", "present": "0"}, 0, false},
		{"peripheral", map[string]string{"type": "Battery", "capacity": "73", "scope": "Device"}, 0, false},
		{"adapter", map[string]string{"type": "Mains", "capacity": "100"}, 0, false},
		{"unknown type", map[string]string{"capacity": "73"}, 0, false},
		{"missing capacity", map[string]string{"type": "Battery"}, 0, false},
		{"invalid", map[string]string{"type": "Battery", "capacity": "unknown"}, 0, false},
		{"negative", map[string]string{"type": "Battery", "capacity": "-1"}, 0, false},
		{"overfull", map[string]string{"type": "Battery", "capacity": "101"}, 0, false},
		{"overflow", map[string]string{"type": "Battery", "capacity": "99999999999999999999999999"}, 0, false},
		{"oversized attribute", map[string]string{"type": "Battery", "capacity": "73" + strings.Repeat(" ", 1024)}, 0, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			writePowerSupply(t, root, "arbitrary-battery-name", tc.values)
			if got, ok := batteryPercentage(root); got != tc.want || ok != tc.wantOK {
				t.Fatalf("battery=(%d, %t), want (%d, %t)", got, ok, tc.want, tc.wantOK)
			}
		})
	}
	root := t.TempDir()
	if _, ok := batteryPercentage(filepath.Join(root, "missing")); ok {
		t.Fatal("missing power-supply tree produced a battery")
	}
	writePowerSupply(t, root, "AC", map[string]string{"type": "Mains", "capacity": "100"})
	writePowerSupply(t, root, "BAT0", map[string]string{"type": "Battery", "present": "0", "capacity": "100"})
	target := writePowerSupply(t, t.TempDir(), "device", map[string]string{"type": "Battery", "capacity": "42"})
	if err := os.Symlink(target, filepath.Join(root, "BAT1")); err != nil {
		t.Fatal(err)
	}
	writePowerSupply(t, root, "BAT2", map[string]string{"type": "Battery", "capacity": "91"})
	if percent, ok := batteryPercentage(root); !ok || percent != 42 {
		t.Fatal("discovery failed to follow sysfs symlink/skip absent battery in stable order", percent, ok)
	}
}

func TestSystemBarLocalDateTimeAndOptionalBattery(t *testing.T) {
	now := time.Date(2026, 11, 10, 21, 34, 56, 0, time.FixedZone("local", 9*60*60))
	root := t.TempDir()
	if got := systemBarText(now, root); got != "2026-11-10 21:34:56" {
		t.Fatal("wrong local time/no-battery format", got)
	}
	writePowerSupply(t, root, "BAT0", map[string]string{"type": "Battery", "capacity": "73"})
	text := systemBarText(now, root)
	if text != "2026-11-10 21:34:56  \x80 73%" {
		t.Fatal("wrong battery format", text)
	}
	for at := 0; at < len(text); at++ {
		ch := text[at]
		if ch >= 128 {
			if systemBarIcons[ch] == nil {
				t.Fatalf("missing icon glyph %#x", ch)
			}
		} else if systemBarGlyphs[ch] == nil {
			t.Fatalf("missing glyph %q", ch)
		}
	}
	for _, icon := range []byte{systemBarBatteryIcon, systemBarVolumeDownIcon, systemBarVolumeUpIcon, systemBarBrightnessDownIcon, systemBarBrightnessUpIcon, systemBarScreenshotIcon, systemBarCropIcon} {
		glyph := systemBarIcons[icon]
		ink := false
		for _, pixel := range glyph {
			ink = ink || pixel != systemBarBackground
		}
		if !ink {
			t.Fatalf("bitmap icon %#x rendered blank", icon)
		}
	}
}

func TestSystemBarBrightnessButtonsUseRawAsymmetricSteps(t *testing.T) {
	d := memoryDriver(t, 960, 64)
	level := 20
	var deltas []int
	d.systemBar = &systemBar{
		powerSupplyPath: t.TempDir(),
		brightness:      func() (int, int, bool) { return level, 100, true },
		changeBrightness: func(delta int) (int, bool) {
			deltas = append(deltas, delta)
			level = max(0, min(100, level+delta))
			return level, true
		},
	}
	now := time.Now()
	d.refreshSystemBar(now)
	b := d.systemBar
	if !bytes.Contains([]byte(b.text), []byte{systemBarBrightnessDownIcon, ' ', '2', '0', ' ', systemBarBrightnessUpIcon}) {
		t.Fatal("brightness controls/level are missing from system bar", []byte(b.text))
	}
	d.systemBarClick(b.brightnessDownLeft-b.bounds.Min.X+1, b.bounds.Dy()/2)
	d.refreshSystemBar(now)
	if level != 16 || b.brightnessLevel != 16 || !slices.Equal(deltas, []int{-4}) {
		t.Fatal("brightness-down button did not subtract four raw units", level, b.brightnessLevel, deltas)
	}
	d.systemBarClick(b.brightnessUpLeft-b.bounds.Min.X+1, b.bounds.Dy()/2)
	if level != 21 || b.brightnessLevel != 21 || !slices.Equal(deltas, []int{-4, 5}) {
		t.Fatal("brightness-up button did not add five raw units", level, b.brightnessLevel, deltas)
	}
}

func TestSystemBarVolumeButtonsUseAsymmetricStepsAndLiveRange(t *testing.T) {
	d := memoryDriver(t, 960, 64)
	mode := 1
	var volumes []int
	d.systemBar = &systemBar{
		powerSupplyPath: t.TempDir(),
		audioOutput:     func() (int, bool) { return mode, true },
		setAudioOutput:  func(next int) bool { mode = next; return true },
		setAudioVolume: func(percent int) bool {
			volumes = append(volumes, percent)
			return true
		},
		volumeLevel: 33,
	}
	now := time.Now()
	d.refreshSystemBar(now)
	b := d.systemBar
	if !bytes.Contains([]byte(b.text), []byte{systemBarVolumeDownIcon, ' ', '3', '3', '%', ' ', systemBarVolumeUpIcon}) {
		t.Fatal("volume controls/level are missing from system bar", []byte(b.text))
	}
	click := func(left int) {
		t.Helper()
		d.systemBarClick(left-b.bounds.Min.X+1, b.bounds.Dy()/2)
		d.refreshSystemBar(now)
	}
	click(b.volumeDownLeft)
	if b.volumeLevel != 29 || !slices.Equal(volumes, []int{29}) {
		t.Fatal("volume-down button did not subtract four points", b.volumeLevel, volumes)
	}
	click(b.volumeUpLeft)
	if b.volumeLevel != 32 || !slices.Equal(volumes, []int{29, 32}) {
		t.Fatal("volume-up button did not add three points", b.volumeLevel, volumes)
	}

	// The 50% restriction applies only to configuration/startup. Live controls
	// may continue through 50 and clamp only at the ALSA percentage limit.
	b.volumeLevel = 49
	d.refreshSystemBar(now)
	click(b.volumeUpLeft)
	if b.volumeLevel != 52 {
		t.Fatal("live volume was incorrectly capped at the startup maximum", b.volumeLevel)
	}
	b.volumeLevel = 99
	d.refreshSystemBar(now)
	click(b.volumeUpLeft)
	if b.volumeLevel != 100 {
		t.Fatal("volume-up did not clamp at 100", b.volumeLevel)
	}
	b.volumeLevel = 2
	d.refreshSystemBar(now)
	click(b.volumeDownLeft)
	if b.volumeLevel != 0 {
		t.Fatal("volume-down did not clamp at zero", b.volumeLevel)
	}
}

func TestSystemBarUsesBundledMonacoAt28Pixels(t *testing.T) {
	font, err := os.ReadFile("../../Monaco_Linux.ttf")
	if err != nil {
		t.Fatal(err)
	}
	if fmt.Sprintf("%x", sha256.Sum256(font)) != systemBarFontSourceSHA256 {
		t.Fatal("Monaco atlas is stale; run make st-fb to regenerate it")
	}
	if systemBarFontSize != 28 {
		t.Fatal("bar did not retain its enlarged 28 px font size")
	}
	for _, ch := range systemBarFontAlphabet {
		glyph := systemBarGlyphs[ch]
		if len(glyph) != systemBarFontCellWidth*systemBarFontCellHeight {
			t.Fatalf("incorrect Monaco cell size for %q", ch)
		}
		ink, antialias := false, false
		for _, pixel := range glyph {
			if pixel>>24 != 255 {
				t.Fatal("bar glyphs must remain opaque")
			}
			ink = ink || pixel != systemBarBackground
			antialias = antialias || (pixel != systemBarBackground && pixel != systemBarForeground)
		}
		if ch == ' ' {
			if ink {
				t.Fatal("space glyph contains ink")
			}
		} else if !ink || !antialias {
			t.Fatalf("Monaco glyph %q is blank/not anti-aliased", ch)
		}
	}
	if systemBarBlend(0) != systemBarBackground || systemBarBlend(255) != systemBarForeground {
		t.Fatal("anti-alias blending changed the bar palette")
	}
}

func TestSystemBarCompositesAboveApplicationsWithoutChangingStoredPixels(t *testing.T) {
	const w, h = 960, 64
	d := memoryDriver(t, w, h)
	terminal := make([]uint32, w*h)
	for i := range terminal {
		terminal[i] = 0xff113355
	}
	d.Upload(terminal, w, 0, 0, w, h)
	view, err := d.NewSurface(image.Rect(0, 0, w, 40), 0xff557733)
	if err != nil {
		t.Fatal(err)
	}
	s := view.(*surface)
	savedPreview := slices.Clone(s.pixels)
	root := t.TempDir()
	writePowerSupply(t, root, "BAT0", map[string]string{"type": "Battery", "capacity": "73"})
	d.systemBar = &systemBar{powerSupplyPath: root}
	now := time.Date(2026, 11, 10, 12, 34, 56, 0, time.Local)
	d.refreshSystemBar(now)
	b := d.systemBar
	if b.bounds.Max != image.Pt(w, h) || b.bounds.Min.Y != h-2*systemBarPadding-systemBarFontCellHeight || b.bounds.Min.X <= 0 {
		t.Fatal("bar is not bounded to the bottom-right corner", b.bounds)
	}
	// The first character uses the anti-aliased Monaco cell, not the old
	// block-scaled bitmap alphabet, with padding outside its font metrics.
	for y := range systemBarFontCellHeight {
		for x := range systemBarFontCellWidth {
			want := systemBarGlyphs['2'][y*systemBarFontCellWidth+x] & 0xffffff
			if pixelAt(d, b.textLeft+x, b.bounds.Min.Y+systemBarPadding+y) != want {
				t.Fatal("first digit did not use the Monaco atlas")
			}
		}
	}
	for y := range h {
		for x := range w {
			want := uint32(0x113355)
			if y < 40 {
				want = 0x557733
			}
			if value, ok := b.pixel(x, y); ok {
				want = value & 0xffffff
			}
			if pixelAt(d, x, y) != want {
				t.Fatalf("composition mismatch at %d,%d", x, y)
			}
		}
	}
	if !slices.Equal(d.pixels, terminal) || !slices.Equal(s.pixels, savedPreview) {
		t.Fatal("system bar contaminated retained application pixels")
	}
	d.Upload(terminal, w, 0, 0, w, h)
	s.Present(savedPreview, w, 40, 0, false)
	x, y := b.bounds.Min.X, b.bounds.Min.Y
	if pixelAt(d, x, y) != systemBarBackground&0xffffff {
		t.Fatal("application repaint erased bar")
	}
	d.movePointer(image.Pt(x, y), true)
	if pixelAt(d, x, y) != 0 {
		t.Fatal("bar obscured software pointer")
	}
	d.movePointer(image.Pt(0, 40), false)
	if pixelAt(d, x, y) != systemBarBackground&0xffffff {
		t.Fatal("moving pointer erased bar/left a trail")
	}
	s.Close()
	if pixelAt(d, x, y) != systemBarBackground&0xffffff {
		t.Fatal("preview close erased bar")
	}
	// A clock refresh must not touch clean pixels outside the overlay damage.
	d.screen.layout.pixel(d.screen.memory, x-1, 0, 0xffbbaa99)
	d.refreshSystemBar(now.Add(systemBarInterval))
	if pixelAt(d, x-1, 0) != 0xbbaa99 {
		t.Fatal("bar refresh bridged unrelated clean pixels")
	}
}

func TestSystemBarRefreshIntervalAndShrinkingDamage(t *testing.T) {
	d := memoryDriver(t, 960, 64)
	if _, err := d.NewSurface(image.Rect(0, 0, 960, 40), 0xff446688); err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	writePowerSupply(t, root, "BAT0", map[string]string{"type": "Battery", "capacity": "100"})
	d.systemBar = &systemBar{powerSupplyPath: root}
	now := time.Date(2026, 11, 10, 12, 34, 56, 0, time.Local)
	d.refreshSystemBar(now)
	oldBounds, oldText := d.systemBar.bounds, d.systemBar.text
	d.flushPending()
	before := slices.Clone(d.screen.memory)
	writePowerSupply(t, root, "BAT0", map[string]string{"capacity": "9"})
	d.refreshSystemBar(now.Add(30*time.Second - time.Nanosecond))
	if d.systemBar.text != oldText || !bytes.Equal(before, d.screen.memory) {
		t.Fatal("system bar refreshed before 30 seconds elapsed")
	}
	d.refreshSystemBar(now.Add(30 * time.Second))
	if !bytes.Contains([]byte(d.systemBar.text), []byte{systemBarBatteryIcon, ' ', '9', '%'}) || d.systemBar.bounds.Min.X <= oldBounds.Min.X {
		t.Fatal("system bar did not refresh/shrink on its deadline")
	}
	if pixelAt(d, oldBounds.Min.X, oldBounds.Min.Y) != 0x446688 {
		t.Fatal("shorter bar did not restore uncovered preview pixels")
	}
	oldBounds = d.systemBar.bounds
	writePowerSupply(t, root, "BAT0", map[string]string{"present": "0"})
	d.refreshSystemBar(now.Add(60 * time.Second))
	if strings.Contains(d.systemBar.text, "BAT") || pixelAt(d, oldBounds.Min.X, oldBounds.Min.Y) != 0x446688 {
		t.Fatal("disappearing battery left stale text/background")
	}
}

func TestSystemBarInactiveVTReacquireAndClosedSuppression(t *testing.T) {
	d := memoryDriver(t, 480, 64)
	root := t.TempDir()
	writePowerSupply(t, root, "BAT0", map[string]string{"type": "Battery", "capacity": "73"})
	d.systemBar = &systemBar{powerSupplyPath: root}
	now := time.Now()
	d.refreshSystemBar(now)
	d.flushPending()
	d.active = false
	before := slices.Clone(d.screen.memory)
	writePowerSupply(t, root, "BAT0", map[string]string{"capacity": "72"})
	d.refreshSystemBar(now.Add(systemBarInterval))
	if !bytes.Equal(before, d.screen.memory) || !bytes.Contains([]byte(d.systemBar.text), []byte{systemBarBatteryIcon, ' ', '7', '2', '%'}) {
		t.Fatal("inactive update wrote scanout or lost new system information")
	}
	d.console = &console{fd: -1, number: 1, ops: consoleOps{
		pointer: func(req uintptr, p unsafe.Pointer) error {
			if req == vtGetState {
				(*vtState)(p).Active = 1
			}
			return nil
		}, value: func(uintptr, uintptr) error { return nil },
	}}
	d.callbacks.Focus = func(bool) {}
	if err := d.acquire(); err != nil {
		t.Fatal(err)
	}
	b := d.systemBar
	for y := b.bounds.Min.Y; y < b.bounds.Max.Y; y++ {
		for x := b.bounds.Min.X; x < b.bounds.Max.X; x++ {
			want, _ := b.pixel(x, y)
			if pixelAt(d, x, y) != want&0xffffff {
				t.Fatal("VT reacquire did not restore latest system bar")
			}
		}
	}
	d.Close()
	before = slices.Clone(d.screen.memory)
	d.refreshSystemBar(now.Add(2 * systemBarInterval))
	if !bytes.Equal(before, d.screen.memory) {
		t.Fatal("bar updated after console/framebuffer cleanup")
	}
}

func TestSystemBarClipsToSmallScreens(t *testing.T) {
	for _, size := range []image.Point{{1, 1}, {10, 5}, {40, 40}, {240, 20}} {
		t.Run(fmt.Sprint(size), func(t *testing.T) {
			d := memoryDriver(t, size.X, size.Y)
			d.systemBar = &systemBar{powerSupplyPath: t.TempDir()}
			d.refreshSystemBar(time.Now())
			if b := d.systemBar.bounds; b.Min.X < 0 || b.Min.Y < 0 || b.Max != size {
				t.Fatal("bar escaped display", b)
			}
			if _, ok := d.systemBar.pixel(-1, 0); ok {
				t.Fatal("bar paints outside visible bounds")
			}
		})
	}
}

func TestSystemBarRefreshesInRunWithoutApplicationDamage(t *testing.T) {
	d := memoryDriver(t, 480, 64)
	root := t.TempDir()
	writePowerSupply(t, root, "BAT0", map[string]string{"type": "Battery", "capacity": "73"})
	d.systemBar = &systemBar{powerSupplyPath: root}
	// Match New: collect system information before entering graphics mode.
	d.active = false
	d.refreshSystemBar(time.Now())
	d.active = true
	d.Show()
	if pixelAt(d, d.systemBar.bounds.Min.X, d.systemBar.bounds.Min.Y) != systemBarBackground&0xffffff {
		t.Fatal("Show did not paint initial bar without application damage")
	}
	var pipe [2]int
	if err := unix.Pipe2(pipe[:], unix.O_NONBLOCK|unix.O_CLOEXEC); err != nil {
		t.Fatal(err)
	}
	d.console = &console{fd: pipe[0], ops: consoleOps{pointer: func(uintptr, unsafe.Pointer) error { return nil }}}
	d.signals = make(chan os.Signal, 1)
	d.mousePaths = "none"
	var lock sync.Mutex
	var closed atomic.Bool
	updates := make(chan string, 8)
	d.callbacks = api.Callbacks{Lock: lock.Lock, Unlock: lock.Unlock, Closed: closed.Load,
		Close: func() { closed.Store(true); d.Close() }, Resize: func(int, int) {}, Focus: func(bool) {},
		PaintDirty: func() {
			d.mu.Lock()
			text := d.systemBar.text
			d.mu.Unlock()
			select {
			case updates <- text:
			default:
			}
		}}
	done := make(chan struct{})
	go func() { defer close(done); d.Run() }()
	t.Cleanup(func() {
		d.Close()
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			t.Error("bar/input loop did not stop")
		}
		unix.Close(pipe[1])
	})
	awaitBattery := func(want string) {
		t.Helper()
		deadline := time.After(2 * time.Second)
		for {
			select {
			case text := <-updates:
				if strings.Contains(text, want) {
					return
				}
			case <-deadline:
				t.Fatal("idle event loop did not refresh system bar", want)
			}
		}
	}
	awaitBattery(string([]byte{systemBarBatteryIcon, ' ', '7', '3', '%'}))
	writePowerSupply(t, root, "BAT0", map[string]string{"capacity": "72"})
	// Advance the deadline rather than sleeping for a real 30-second period.
	d.mu.Lock()
	d.systemBar.nextRefresh = time.Time{}
	d.mu.Unlock()
	awaitBattery(string([]byte{systemBarBatteryIcon, ' ', '7', '2', '%'}))
}

func TestSystemBarAudioClickTogglesSelectedMode(t *testing.T) {
	d := memoryDriver(t, 960, 64)
	mode := 1
	setCalls := 0
	d.systemBar = &systemBar{
		powerSupplyPath: t.TempDir(),
		audioOutput:     func() (int, bool) { return mode, true },
		setAudioOutput: func(next int) bool {
			setCalls++
			mode = next
			return true
		},
	}
	d.refreshSystemBar(time.Now())
	b := d.systemBar
	d.systemBarClick(b.audioLeft-b.bounds.Min.X, (b.bounds.Dy())/2)
	if mode != 0 || setCalls != 1 || !d.systemBar.nextRefresh.IsZero() {
		t.Fatal("audio checker click did not switch earphone to speaker", mode, setCalls)
	}
	// The whole status bar is the output checker, not only the bitmap cell.
	d.systemBarClick(0, b.bounds.Dy()/2)
	if mode != 1 || setCalls != 2 {
		t.Fatal("status-bar click did not toggle ALSA mode", mode, setCalls)
	}
}

func TestSystemBarClipboardSyncStartsOffAndToggles(t *testing.T) {
	d := memoryDriver(t, 900, 200)
	d.systemBar = &systemBar{powerSupplyPath: t.TempDir()}
	path := filepath.Join(t.TempDir(), "cache.sqlite3")
	d.clipboardSync = sharedclipboard.New(path, sharedclipboard.Hooks{Changed: d.sharedClipboardChanged, Status: d.sharedClipboardStatus})
	d.refreshSystemBar(time.Now())
	b := d.systemBar
	if b.clipboardLeft < 0 || !bytes.Contains([]byte(b.text), []byte{'S', ' ', systemBarUncheckedIcon}) || d.ClipboardSyncState().Enabled {
		t.Fatal("clipboard synchronization did not start visibly disabled", []byte(b.text))
	}
	d.systemBarClick(b.clipboardLeft-b.bounds.Min.X+1, b.bounds.Dy()/2)
	deadline := time.Now().Add(2 * time.Second)
	for !d.ClipboardSyncState().Ready && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if state := d.ClipboardSyncState(); !state.Enabled || !state.Ready {
		t.Fatal("system bar did not enable clipboard synchronization", state)
	}
	d.refreshSystemBar(time.Now())
	if !bytes.Contains([]byte(b.text), []byte{'S', ' ', systemBarCheckedIcon}) {
		t.Fatal("enabled clipboard state is not shown", []byte(b.text))
	}
	d.systemBarClick(b.clipboardLeft-b.bounds.Min.X+1, b.bounds.Dy()/2)
	deadline = time.Now().Add(2 * time.Second)
	for d.ClipboardSyncState().Ready && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if d.ClipboardSyncState().Enabled {
		t.Fatal("system bar did not disable clipboard synchronization")
	}
}

func TestSystemBarReadsSpeakerEarphoneAndUnknownWithoutChangingPlayback(t *testing.T) {
	d := memoryDriver(t, 960, 64)
	mode, known, calls := 1, true, 0
	d.systemBar = &systemBar{powerSupplyPath: t.TempDir(), audioOutput: func() (int, bool) {
		calls++
		return mode, known
	}}
	now := time.Now()
	d.refreshSystemBar(now)
	if len(d.systemBar.text) == 0 || d.systemBar.text[len(d.systemBar.text)-1] != systemBarCheckedIcon || calls != 1 {
		t.Fatal("selected earphone mode was not shown", d.systemBar.text, calls)
	}
	mode = 0
	d.refreshSystemBar(now.Add(systemBarInterval - time.Nanosecond))
	if d.systemBar.text[len(d.systemBar.text)-1] != systemBarCheckedIcon || calls != 1 {
		t.Fatal("audio checker ignored the refresh interval")
	}
	d.refreshSystemBar(now.Add(systemBarInterval))
	if d.systemBar.text[len(d.systemBar.text)-1] != systemBarUncheckedIcon || calls != 2 {
		t.Fatal("speaker selector change was not shown", d.systemBar.text, calls)
	}
	known = false
	d.refreshSystemBar(now.Add(2 * systemBarInterval))
	if d.systemBar.text[len(d.systemBar.text)-1] != systemBarUnknownIcon || calls != 3 {
		t.Fatal("unreadable mixer was misreported as speaker", d.systemBar.text, calls)
	}
	for at := 0; at < len(d.systemBar.text); at++ {
		ch := d.systemBar.text[at]
		if ch >= 128 {
			if systemBarIcons[ch] == nil {
				t.Fatalf("missing audio icon glyph %#x", ch)
			}
		} else if systemBarGlyphs[ch] == nil {
			t.Fatalf("missing audio indicator glyph %q", ch)
		}
	}
}
