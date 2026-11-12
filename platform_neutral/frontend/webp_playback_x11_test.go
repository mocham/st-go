//go:build webpcache && !backend_web && cgo && !purego

package frontend

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/BurntSushi/xgb"
	"github.com/BurntSushi/xgb/xproto"
	"github.com/BurntSushi/xgb/xtest"
	"st-go/config"
)

// TestWebPAnimationX11FileBrowserPlayback exercises the shipped terminal and
// browser together, including the PTY, DCS parser, timer and X framebuffer.
// Run it against an isolated server after building both binaries:
//
//	DISPLAY=:N make test-webp-x11
func TestWebPAnimationX11FileBrowserPlayback(t *testing.T) {
	if os.Getenv("ST_TEST_WEBP_X11") != "1" {
		t.Skip("requires an isolated X server and built st/file-browser binaries")
	}
	ffmpeg, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("ffmpeg is unavailable")
	}
	terminal := testExecutable(t, "ST_TEST_BINARY", "./BINARIES/st")
	browser := testExecutable(t, "ST_TEST_FILE_BROWSER", "./BINARIES/file-browser")

	dir := t.TempDir()
	animation := filepath.Join(dir, "animation.webp")
	cmd := exec.Command(ffmpeg, "-loglevel", "error", "-y", "-f", "lavfi",
		"-i", "testsrc=duration=12:size=64x64:rate=5", "-loop", "0", "-f", "webp", animation)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("generate animated WebP: %v: %s", err, output)
	}

	cfg := config.Default()
	cfg.WebPCachePath = filepath.Join(dir, "cache", "webp.sqlite3")
	configPath := filepath.Join(dir, "st.json")
	encoded, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(configPath, encoded, 0600); err != nil {
		t.Fatal(err)
	}

	conn, err := xgb.NewConn()
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if err := xtest.Init(conn); err != nil {
		t.Fatal(err)
	}
	setup := xproto.Setup(conn)
	root := setup.DefaultScreen(conn).Root
	down := x11Keycode(t, conn, setup, 0xff54) // XK_Down

	logFile, err := os.Create(filepath.Join(dir, "terminal.log"))
	if err != nil {
		t.Fatal(err)
	}
	defer logFile.Close()
	title := fmt.Sprintf("st-webp-playback-%d", os.Getpid())
	process := exec.Command(terminal, "-config", configPath, "-g", "80x24", "-t", title, "-e", browser, dir)
	process.Stdout, process.Stderr = logFile, logFile
	if err := process.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = process.Process.Kill()
		_ = process.Wait()
	})

	window := waitForTitledWindow(t, conn, root, title, logFile.Name())
	xproto.SetInputFocus(conn, xproto.InputFocusPointerRoot, window, xproto.TimeCurrentTime)
	// The physical-directory browser starts on "..". Select its sole file;
	// the default sidebar remains visible and floating throughout the test.
	xtest.FakeInput(conn, xproto.KeyPress, byte(down), 0, 0, 0, 0, 0)
	xtest.FakeInput(conn, xproto.KeyRelease, byte(down), 0, 0, 0, 0, 0)
	conn.Sync()
	// Let the browser complete its one selection repaint. A frame-1-only
	// regression is stable from this point, while the 12-second fixture keeps
	// valid playback running long enough for slow test hosts.
	time.Sleep(500 * time.Millisecond)

	geometry, err := xproto.GetGeometry(conn, xproto.Drawable(window)).Reply()
	if err != nil {
		t.Fatal(err)
	}
	width, height := int(geometry.Width), int(geometry.Height)
	// Sample only the unobscured preview body: never the floating sidebar,
	// header/status rows or cursor. Successive animation frames must produce
	// several distinct server-side pixel buffers here.
	x, y := width/8, height/4
	w, h := min(width/4, 256), min(height/2, 256)
	if w < 1 || h < 1 {
		t.Fatalf("terminal window is too small: %dx%d", width, height)
	}
	frames := make(map[[sha256.Size]byte]struct{})
	deadline := time.Now().Add(4 * time.Second)
	for time.Now().Before(deadline) {
		image, err := xproto.GetImage(conn, xproto.ImageFormatZPixmap, xproto.Drawable(window),
			int16(x), int16(y), uint16(w), uint16(h), 0xffffffff).Reply()
		if err != nil {
			t.Fatal(err)
		}
		frames[sha256.Sum256(image.Data)] = struct{}{}
		if len(frames) >= 3 {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	data, _ := os.ReadFile(logFile.Name())
	t.Fatalf("animated WebP produced only %d distinct X11 preview frames: %s", len(frames), data)
}

func testExecutable(t *testing.T, environment, fallback string) string {
	t.Helper()
	path := os.Getenv(environment)
	if path == "" {
		path = fallback
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(absolute)
	if err != nil {
		t.Fatalf("%s: %v (build the required binary first)", absolute, err)
	}
	if !info.Mode().IsRegular() || info.Mode()&0111 == 0 {
		t.Fatalf("%s is not an executable file", absolute)
	}
	return absolute
}

func x11Keycode(t *testing.T, conn *xgb.Conn, setup *xproto.SetupInfo, keysym xproto.Keysym) xproto.Keycode {
	t.Helper()
	mapping, err := xproto.GetKeyboardMapping(conn, setup.MinKeycode, byte(setup.MaxKeycode-setup.MinKeycode+1)).Reply()
	if err != nil {
		t.Fatal(err)
	}
	for index, candidate := range mapping.Keysyms {
		if candidate == keysym {
			return setup.MinKeycode + xproto.Keycode(index/int(mapping.KeysymsPerKeycode))
		}
	}
	t.Fatalf("X server has no keycode for keysym %#x", keysym)
	return 0
}

func waitForTitledWindow(t *testing.T, conn *xgb.Conn, root xproto.Window, title, logPath string) xproto.Window {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		tree, err := xproto.QueryTree(conn, root).Reply()
		if err != nil {
			t.Fatal(err)
		}
		for _, window := range tree.Children {
			property, err := xproto.GetProperty(conn, false, window, xproto.AtomWmName, xproto.AtomString, 0, 128).Reply()
			if err == nil && string(property.Value) == title {
				return window
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	data, _ := os.ReadFile(logPath)
	t.Fatalf("terminal window %q did not appear: %s", title, data)
	return 0
}
