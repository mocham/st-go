package frontend

import (
	"errors"
	"image"
	"os"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"st-go/config"
	"st-go/platform_neutral/api"
	term "st-go/platform_neutral/stterminal"
	"st-go/widgets"
)

type widgetTestHost struct {
	api.Host
	closed atomic.Bool
	mu     sync.Mutex
	pixels widgets.Buffer
}

func (*widgetTestHost) ClientSize() (int, int, error)            { return 164, 42, nil }
func (*widgetTestHost) Bind(api.Callbacks)                       {}
func (*widgetTestHost) Upload([]uint32, int, int, int, int, int) {}
func (h *widgetTestHost) UploadAt(p []uint32, stride int, r image.Rectangle, at image.Point) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.pixels.Copy(p, stride, r, at)
}
func (*widgetTestHost) SetTextCursor(api.TextCursor) {}
func (*widgetTestHost) SetupKeys()                   {}
func (*widgetTestHost) SetTitle(string)              {}
func (*widgetTestHost) SetIconTitle(string)          {}
func (h *widgetTestHost) Close()                     { h.closed.Store(true) }
func (*widgetTestHost) NewSurface(image.Rectangle, uint32) (api.Surface, error) {
	return nil, errors.New("raster-only test host has no surfaces")
}

func terminalWidgets(t *testing.T) (*widgetTestHost, *Terminal, *Terminal) {
	t.Helper()
	native := &widgetTestHost{}
	native.pixels.Resize(164, 42)
	root, err := widgets.Wrap(native)
	if err != nil {
		t.Fatal(err)
	}
	root.SetBounds(image.Rect(0, 0, 82, 42))
	other, err := root.NewWidget(api.WidgetOptions{Bounds: image.Rect(82, 0, 164, 42)})
	if err != nil {
		root.Close()
		t.Fatal(err)
	}
	cfg := config.Default()
	cfg.Cols, cfg.Rows = 20, 5
	cfg.GlyphWidth, cfg.GlyphHeight, cfg.GlyphBaseline = 4, 8, 6
	cfg.Borderpx = 1
	cfg.WebPCachePath = ""
	a, err := NewTerminalWidget(cfg, root, 1)
	if err != nil {
		t.Fatal(err)
	}
	b, err := NewTerminalWidget(cfg, other, 1)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		a.mu.Lock()
		a.Close()
		a.mu.Unlock()
		b.mu.Lock()
		b.Close()
		b.mu.Unlock()
	})
	return native, a, b
}

func TestTerminalWidgetsOwnRasterAtlasAndMouseState(t *testing.T) {
	_, a, b := terminalWidgets(t)
	a.fillRect(0xff112233, 0, 0, a.fbW, a.fbH)
	aPixels := slices.Clone(a.framebuf)
	b.fillRect(0xffaabbcc, 0, 0, b.fbW, b.fbH)
	if !slices.Equal(aPixels, a.framebuf) {
		t.Fatal("terminal raster was shared")
	}
	_, _, glyphs := a.imageToGlyphs(1, 1, []byte{20, 40, 60, 255}, term.ImageDecodeOptions{})
	atlas := slices.Clone(a.imageAtlas)
	_, _, _ = b.imageToGlyphs(1, 1, []byte{200, 150, 100, 255}, term.ImageDecodeOptions{})
	b.ImageClearAll()
	if len(glyphs) == 0 || !slices.Equal(atlas, a.imageAtlas) {
		t.Fatal("another terminal reset/recycled the image/PDF atlas")
	}
	a.pointerPress(1, 0, 2, 2)
	b.pointerMotion(0, 4, 4)
	if !a.msel.active || b.msel.active || b.buttons != 0 || a.buttons != 1 {
		t.Fatal("mouse selection/button state leaked between terminals")
	}
}

func TestCoreAttachmentUsesAlreadySizedWidget(t *testing.T) {
	trm := regionTestTerminal(t)
	trm.resize(30*trm.cw+2, 8*trm.ch+2)
	trm.attachTerm(term.NewTerm(trm.cfg, trm))
	if trm.termCore.Cols() != 30 || trm.termCore.Rows() != 8 {
		t.Fatal("new core retained configured grid after widget sizing")
	}
}

func TestWidgetReactivationKeepsCommittedSynchronizedPixels(t *testing.T) {
	native, a, _ := terminalWidgets(t)
	paint := func(output string) {
		a.writeTerminalOutput(a.termCore, []byte(output))
		a.paintTerminalOutput(a.termCore)
	}
	pixel := func() uint32 { native.mu.Lock(); defer native.mu.Unlock(); return native.pixels.Pixels[164+1] }
	paint("\x1b[?25l\x1b[48;2;17;34;51m\x1b[2J")
	if pixel() != 0xff112233 {
		t.Fatal("initial frame did not commit", pixel())
	}
	a.Widget().SetVisible(false)
	paint("\x1b[?2026h\x1b[48;2;170;187;204m\x1b[2J")
	a.Widget().SetVisible(true)
	if pixel() != 0xff112233 {
		t.Fatal("activation exposed partial synchronized output", pixel())
	}
	paint("\x1b[?2026l")
	if pixel() != 0xffaabbcc {
		t.Fatal("synchronized reset failed to commit", pixel())
	}
}

func TestHiddenTerminalPTYDrainsRepliesAndExitIsWidgetLocal(t *testing.T) {
	native, a, b := terminalWidgets(t)
	file := t.TempDir() + "/reply"
	b.Widget().SetVisible(false)
	if err := b.StartSession(SessionOptions{Command: []string{"bash", "-c", `stty -echo -icanon min 1 time 0; printf 'SECOND\033[6n'; IFS= read -r -d R reply; printf '%s' "$reply" > "$1"; IFS= read -r -n 1`, "widget-test", file}}); err != nil {
		t.Fatal(err)
	}
	if err := a.StartSession(SessionOptions{Command: []string{"bash", "-c", "printf FIRST"}}); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		data, err := os.ReadFile(file)
		if err == nil && len(data) > 0 && a.closed() {
			if string(data) != "\x1b[1;7" {
				t.Fatalf("hidden PTY got incorrect cursor reply: %q", data)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("background PTY stopped draining/replying or foreground PTY did not exit", err, a.closed())
		}
		time.Sleep(5 * time.Millisecond)
	}
	if native.closed.Load() || b.closed() {
		t.Fatal("one child's exit closed another terminal or the native window")
	}
	b.mu.Lock()
	text := b.termCore.LineText(0)
	b.mu.Unlock()
	if !strings.HasPrefix(text, "SECOND") {
		t.Fatalf("hidden PTY output was not parsed: %q", text)
	}
	b.Widget().SetVisible(true)
	b.Widget().FocusWidget()
	b.mu.Lock()
	b.Close()
	b.mu.Unlock()
	if !native.closed.Load() {
		t.Fatal("last terminal failed to close native host")
	}
}
