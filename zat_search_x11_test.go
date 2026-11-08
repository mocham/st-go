//go:build webpcache

package main

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/BurntSushi/xgb"
	"github.com/BurntSushi/xgb/xproto"
	"github.com/BurntSushi/xgb/xtest"
	"st-go/config"
)

func typeZatX11(t *testing.T, conn *xgb.Conn, setup *xproto.SetupInfo, text string) {
	t.Helper()
	mapping, err := xproto.GetKeyboardMapping(conn, setup.MinKeycode, byte(setup.MaxKeycode-setup.MinKeycode+1)).Reply()
	if err != nil {
		t.Fatal(err)
	}
	shift := x11Keycode(t, conn, setup, 0xffe1)
	for _, r := range text {
		keysym := xproto.Keysym(r)
		if r == '\r' {
			keysym = 0xff0d
		}
		found := false
		for i := 0; i < len(mapping.Keysyms) && !found; i += int(mapping.KeysymsPerKeycode) {
			for column := 0; column < min(2, int(mapping.KeysymsPerKeycode)); column++ {
				if mapping.Keysyms[i+column] != keysym {
					continue
				}
				code := byte(setup.MinKeycode) + byte(i/int(mapping.KeysymsPerKeycode))
				if column == 1 {
					xtest.FakeInput(conn, xproto.KeyPress, byte(shift), 0, 0, 0, 0, 0)
				}
				xtest.FakeInput(conn, xproto.KeyPress, code, 0, 0, 0, 0, 0)
				xtest.FakeInput(conn, xproto.KeyRelease, code, 0, 0, 0, 0, 0)
				if column == 1 {
					xtest.FakeInput(conn, xproto.KeyRelease, byte(shift), 0, 0, 0, 0, 0)
				}
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("X server cannot type %q", r)
		}
	}
	conn.Sync()
}

func TestZatSearchX11(t *testing.T) {
	if os.Getenv("ST_TEST_ZAT_X11") != "1" {
		t.Skip("requires an isolated X server and built st/zat binaries")
	}
	dir := t.TempDir()
	file := filepath.Join(dir, "search.pdf")
	if err := os.WriteFile(file, metadataActualTextPDF("α café α café"), 0600); err != nil {
		t.Fatal(err)
	}
	cfg := config.Default()
	cfg.WebPCachePath = filepath.Join(dir, "cache.sqlite3")
	encoded, _ := json.Marshal(cfg)
	configPath := filepath.Join(dir, "st.json")
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
	log, err := os.Create(filepath.Join(dir, "terminal.log"))
	if err != nil {
		t.Fatal(err)
	}
	defer log.Close()
	clientLog := filepath.Join(dir, "zat.log")
	title := fmt.Sprintf("st-zat-search-%d", os.Getpid())
	process := exec.Command(testExecutable(t, "ST_TEST_BINARY", "./st"), "-config", configPath, "-g", "64x24", "-t", title,
		"-e", "bash", "-c", `"$1" "$2" | tee "$3"`, "zat-search", testExecutable(t, "ST_TEST_ZAT", "./zat"), file, clientLog)
	process.Env = append(envWithout(os.Environ(), "GCONV_PATH"), "GCONV_PATH="+unavailablePDFGconv(t))
	process.Stdout, process.Stderr = log, log
	if err := process.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { process.Process.Kill(); process.Wait() })
	window := waitForTitledWindow(t, conn, root, title, log.Name())
	xproto.SetInputFocus(conn, xproto.InputFocusPointerRoot, window, xproto.TimeCurrentTime)
	awaitOutput := func(from int, text string) {
		t.Helper()
		deadline := time.Now().Add(6 * time.Second)
		for time.Now().Before(deadline) {
			data, _ := os.ReadFile(clientLog)
			if from <= len(data) && strings.Contains(string(data[from:]), text) {
				return
			}
			time.Sleep(10 * time.Millisecond)
		}
		data, _ := os.ReadFile(clientLog)
		errors, _ := os.ReadFile(log.Name())
		t.Fatalf("waiting for %q: output=%q errors=%q", text, data, errors)
	}
	awaitOutput(0, "1/1")
	tree, err := xproto.QueryTree(conn, window).Reply()
	if err != nil || len(tree.Children) != 1 {
		t.Fatalf("preview child = %+v, %v", tree, err)
	}
	preview := tree.Children[0]
	frame := func() ([]byte, int) {
		t.Helper()
		geometry, err := xproto.GetGeometry(conn, xproto.Drawable(preview)).Reply()
		if err != nil {
			t.Fatal(err)
		}
		image, err := xproto.GetImage(conn, xproto.ImageFormatZPixmap, xproto.Drawable(preview), 0, 0, geometry.Width, geometry.Height, 0xffffffff).Reply()
		if err != nil {
			t.Fatal(err)
		}
		tinted := 0
		for i := 0; i+3 < len(image.Data); i += 4 {
			b, g, r := image.Data[i], image.Data[i+1], image.Data[i+2]
			if setup.ImageByteOrder != xproto.ImageOrderLSBFirst {
				r, g, b = image.Data[i+1], image.Data[i+2], image.Data[i+3]
			}
			if r > 170 && b < 130 && int(g)-int(b) > 60 {
				tinted++
			}
		}
		return image.Data, tinted
	}
	awaitFrame := func(check func([]byte, int) bool) []byte {
		t.Helper()
		deadline := time.Now().Add(4 * time.Second)
		for time.Now().Before(deadline) {
			data, n := frame()
			if check(data, n) {
				return data
			}
			time.Sleep(20 * time.Millisecond)
		}
		t.Fatal("server-side search highlight pixels did not update")
		return nil
	}
	send := func(keys, reply string) {
		t.Helper()
		data, _ := os.ReadFile(clientLog)
		typeZatX11(t, conn, setup, keys)
		awaitOutput(len(data), reply)
	}
	send("/{alpha}\r", "Match 1/2 on page 1")
	first := sha256.Sum256(awaitFrame(func(_ []byte, n int) bool { return n > 0 }))
	send("n", "Match 2/2 on page 1")
	second := sha256.Sum256(awaitFrame(func(data []byte, n int) bool { return n > 0 && sha256.Sum256(data) != first }))
	send("N", "Match 1/2 on page 1")
	awaitFrame(func(data []byte, n int) bool { return n > 0 && sha256.Sum256(data) != second })
	send("/caf\\'e\r", "Match 1/2 on page 1")
	awaitFrame(func(_ []byte, n int) bool { return n > 0 })
	send(":noh\r", "region secondary find clear")
	awaitFrame(func(_ []byte, n int) bool { return n == 0 })
}
