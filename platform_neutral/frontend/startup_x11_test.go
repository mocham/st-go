package frontend

import (
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/BurntSushi/xgb"
	"github.com/BurntSushi/xgb/xproto"
	"github.com/BurntSushi/xgb/xtest"
	"st-go/config"
)

// Exercise the shipped executable, config discovery and PTY/event loop together.
// Use an isolated X server and ST_TEST_STARTUP_X11=1; ST_TEST_BINARY selects the
// binary variant (defaults to ./BINARIES/st). The copy has no source-tree resources.
func TestRelocatedStartupX11(t *testing.T) {
	if os.Getenv("ST_TEST_STARTUP_X11") != "1" {
		t.Skip("requires an isolated X server and a built terminal")
	}
	bin := os.Getenv("ST_TEST_BINARY")
	if bin == "" {
		bin = "./BINARIES/st"
	}
	src, err := os.Open(bin)
	if err != nil {
		t.Fatal(err)
	}
	defer src.Close()
	dir := filepath.Join(t.TempDir(), ".local", "bin")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	installed := filepath.Join(dir, "st")
	dst, err := os.OpenFile(installed, os.O_WRONLY|os.O_CREATE, 0700)
	if err != nil {
		t.Fatal(err)
	}
	_, err = io.Copy(dst, src)
	closeErr := dst.Close()
	if err != nil {
		t.Fatal(err)
	}
	if closeErr != nil {
		t.Fatal(closeErr)
	}
	if err := os.WriteFile(filepath.Join(dir, "broken.ttf"), []byte("not a font"), 0600); err != nil {
		t.Fatal(err)
	}
	c, err := xgb.NewConn()
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if err := xtest.Init(c); err != nil {
		t.Fatal(err)
	}
	setup := xproto.Setup(c)
	root := setup.DefaultScreen(c).Root
	mapping, err := xproto.GetKeyboardMapping(c, setup.MinKeycode, byte(setup.MaxKeycode-setup.MinKeycode+1)).Reply()
	if err != nil {
		t.Fatal(err)
	}
	var enter xproto.Keycode
	for i, sym := range mapping.Keysyms {
		if sym == 0xff0d {
			enter = setup.MinKeycode + xproto.Keycode(i/int(mapping.KeysymsPerKeycode))
			break
		}
	}
	if enter == 0 {
		t.Fatal("no Return key")
	}
	palette16, _ := json.Marshal(map[string]any{"colorname": config.Default().Colorname[:16]})
	sandboxCache := filepath.Join(dir, "webp-cache", "cache.sqlite3")
	type startupCase struct {
		name, legacy, preferred string
		cols                    int
		fifo                    bool
	}
	cases := []startupCase{
		{name: "standalone", cols: 80},
		{name: "legacy", legacy: `{"cols":37}`, cols: 37},
		{name: "installed", legacy: `{"cols":37}`, preferred: `{"cols":42}`, cols: 42},
		{name: "incomplete palette", legacy: string(palette16), cols: 80},
		{name: "malformed", preferred: `{"cols":1,`, cols: 80},
		{name: "invalid dimensions", preferred: `{"glyphwidth":0,"cols":0}`, cols: 80},
		{name: "missing font", preferred: `{"font":"fonts/missing.ttf"}`, cols: 80},
		{name: "corrupt font", preferred: `{"font":"broken.ttf"}`, cols: 80},
		{name: "config FIFO", cols: 80, fifo: true},
		{name: "custom cache", preferred: fmt.Sprintf(`{"webp_cache_path":%q}`, sandboxCache), cols: 80},
	}
	// Exercise a locally supplied reproduction config when present. Redirect
	// only its cache path to our sandbox; never migrate the user's actual cache.
	if _, err := os.Stat("TEST-ASSETS/config.json"); err == nil {
		cfg, err := config.Load("TEST-ASSETS/config.json")
		if err != nil {
			t.Fatal(err)
		}
		cfg.WebPCachePath = sandboxCache
		data, err := json.Marshal(cfg)
		if err != nil {
			t.Fatal(err)
		}
		cases = append(cases, startupCase{name: "provided config", legacy: string(data), cols: int(cfg.Cols)})
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			for _, name := range []string{"config.json", "st.json"} {
				if err := os.Remove(filepath.Join(dir, name)); err != nil && !os.IsNotExist(err) {
					t.Fatal(err)
				}
			}
			for name, data := range map[string]string{"config.json": tc.legacy, "st.json": tc.preferred} {
				if data != "" {
					if err := os.WriteFile(filepath.Join(dir, name), []byte(data), 0600); err != nil {
						t.Fatal(err)
					}
				}
			}
			if tc.fifo {
				if err := syscall.Mkfifo(filepath.Join(dir, "st.json"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			log, err := os.CreateTemp(t.TempDir(), "startup-*.log")
			if err != nil {
				t.Fatal(err)
			}
			defer log.Close()
			title := fmt.Sprintf("st-startup-%d-%s", os.Getpid(), tc.name)
			cmd := exec.Command(installed, "-t", title, "-e", "/bin/sh", "-c",
				"printf 'PORTABLE TERMINAL STARTED\n'; read reply; printf '\033[2;1HKEYBOARD INPUT WORKS\n'; read again")
			cmd.Dir = t.TempDir() // unrelated to executable, config and font paths
			cmd.Stdout, cmd.Stderr = log, log
			if err := cmd.Start(); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { cmd.Process.Kill(); cmd.Wait() })
			var window xproto.Window
			wait := func(row int) {
				t.Helper()
				deadline := time.Now().Add(5 * time.Second)
				for time.Now().Before(deadline) {
					if window == 0 {
						tree, err := xproto.QueryTree(c, root).Reply()
						if err != nil {
							t.Fatal(err)
						}
						for _, w := range tree.Children {
							p, err := xproto.GetProperty(c, false, w, xproto.AtomWmName, xproto.AtomString, 0, 128).Reply()
							if err == nil && string(p.Value) == title {
								window = w
								break
							}
						}
					}
					if window != 0 {
						// Exclude column zero, where an initial cursor could look like text.
						p, err := xproto.GetImage(c, xproto.ImageFormatZPixmap, xproto.Drawable(window), 81, int16(1+row*28), 224, 28, 0xffffff).Reply()
						if err == nil {
							ink := 0
							for i := 0; i+4 <= len(p.Data); i += 4 {
								pixel := binary.LittleEndian.Uint32(p.Data[i:]) & 0xffffff
								if pixel != 0 && pixel != 0x181818 {
									ink++
								}
							}
							if ink > 100 {
								return
							}
						}
					}
					time.Sleep(10 * time.Millisecond)
				}
				data, _ := os.ReadFile(log.Name())
				t.Fatalf("relocated terminal did not paint row %d: %s", row, data)
			}
			wait(0)
			geometry, err := xproto.GetGeometry(c, xproto.Drawable(window)).Reply()
			if err != nil {
				t.Fatal(err)
			}
			if int(geometry.Width) != tc.cols*16+2 {
				t.Fatalf("wrong config selected: width=%d", geometry.Width)
			}
			xproto.SetInputFocus(c, xproto.InputFocusPointerRoot, window, xproto.TimeCurrentTime)
			xtest.FakeInput(c, xproto.KeyPress, byte(enter), 0, 0, 0, 0, 0)
			xtest.FakeInput(c, xproto.KeyRelease, byte(enter), 0, 0, 0, 0, 0)
			wait(1)
		})
	}
}
