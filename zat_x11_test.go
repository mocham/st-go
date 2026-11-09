//go:build webpcache

package main

import (
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

func TestZatPageTextX11(t *testing.T) {
	if os.Getenv("ST_TEST_ZAT_X11") != "1" {
		t.Skip("requires an isolated X server and built st/zat binaries")
	}
	terminal := testExecutable(t, "ST_TEST_BINARY", "./BINARIES/st")
	client := testExecutable(t, "ST_TEST_ZAT", "./BINARIES/zat")
	dir := t.TempDir()
	file, expected := os.Getenv("ST_TEST_ZAT_PDF"), os.Getenv("ST_TEST_ZAT_TEXT")
	generated := file == ""
	if file == "" {
		file, expected = filepath.Join(dir, "document.pdf"), "Selectable café 世界 😀"
		if err := os.WriteFile(file, metadataActualTextPDF(expected), 0600); err != nil {
			t.Fatal(err)
		}
	}
	file, err := filepath.Abs(file)
	if err != nil {
		t.Fatal(err)
	}
	if expected == "" {
		t.Fatal("ST_TEST_ZAT_TEXT is required for a custom PDF")
	}
	cfg := config.Default()
	cfg.WebPCachePath = filepath.Join(dir, "webp.sqlite3")
	data, _ := json.Marshal(cfg)
	configPath := filepath.Join(dir, "st.json")
	if err := os.WriteFile(configPath, data, 0600); err != nil {
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
	title := fmt.Sprintf("st-zat-page-text-%d", os.Getpid())
	// tee observes the real client output without replacing terminal replies,
	// its stdin PTY, X input, or the built terminal's rendering/worker threads.
	process := exec.Command(terminal, "-config", configPath, "-g", "100x32", "-t", title,
		"-e", "bash", "-c", `"$1" "$2" | tee "$3"`, "zat-test", client, file, clientLog)
	process.Env = append(envWithout(os.Environ(), "GCONV_PATH"), "GCONV_PATH="+unavailablePDFGconv(t))
	process.Stdout, process.Stderr = log, log
	if err := process.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { process.Process.Kill(); process.Wait() })
	window := waitForTitledWindow(t, conn, root, title, log.Name())
	xproto.SetInputFocus(conn, xproto.InputFocusPointerRoot, window, xproto.TimeCurrentTime)
	await := func(description, text string) {
		t.Helper()
		deadline := time.Now().Add(6 * time.Second)
		for time.Now().Before(deadline) {
			data, _ := os.ReadFile(clientLog)
			if strings.Contains(string(data), text) {
				return
			}
			time.Sleep(10 * time.Millisecond)
		}
		output, _ := os.ReadFile(clientLog)
		errors, _ := os.ReadFile(log.Name())
		t.Fatalf("waiting for %s: client=%q terminal=%q", description, output, errors)
	}
	await("initial PDF request", "region secondary info")
	shift, tab := x11Keycode(t, conn, setup, 0xffe1), x11Keycode(t, conn, setup, 0xff09)
	xtest.FakeInput(conn, xproto.KeyPress, byte(shift), 0, 0, 0, 0, 0)
	xtest.FakeInput(conn, xproto.KeyPress, byte(tab), 0, 0, 0, 0, 0)
	xtest.FakeInput(conn, xproto.KeyRelease, byte(tab), 0, 0, 0, 0, 0)
	xtest.FakeInput(conn, xproto.KeyRelease, byte(shift), 0, 0, 0, 0, 0)
	conn.Sync()
	await("page-text query", "region secondary text ")
	await("extracted page text", expected)
	if generated {
		typeZatX11(t, conn, setup, "/Selectable\r")
		await("occurrence search", "Match 1/1 on page 1")
		await("selectable search highlight", "\x1b[30;48;5;214mSelectable\x1b[0m")
	}
	// The tee log precedes PTY parsing/painting. Select real terminal cells,
	// then use the real shortcut and retrieve CLIPBOARD from a second X client.
	time.Sleep(200 * time.Millisecond)
	origin, err := xproto.TranslateCoordinates(conn, window, root, 0, 0).Reply()
	if err != nil {
		t.Fatal(err)
	}
	x1, y1 := int16(cfg.Borderpx+1)+origin.DstX, int16(cfg.Borderpx+2*int(cfg.GlyphHeight)+1)+origin.DstY
	x2, y2 := int16(cfg.Borderpx+60*int(cfg.GlyphWidth)-3)+origin.DstX, int16(cfg.Borderpx+30*int(cfg.GlyphHeight)-1)+origin.DstY
	xtest.FakeInput(conn, xproto.MotionNotify, 0, 0, root, x1, y1, 0)
	xtest.FakeInput(conn, xproto.ButtonPress, 1, 0, root, 0, 0, 0)
	xtest.FakeInput(conn, xproto.MotionNotify, 0, 0, root, x2, y2, 0)
	xtest.FakeInput(conn, xproto.ButtonRelease, 1, 0, root, 0, 0, 0)
	conn.Sync()
	// Wait for release handling and PRIMARY ownership before sending the copy
	// shortcut; an X round trip alone only synchronizes the server, not st.
	selectionDeadline := time.Now().Add(3 * time.Second)
	for {
		owner, err := xproto.GetSelectionOwner(conn, xproto.AtomPrimary).Reply()
		if err != nil {
			t.Fatal(err)
		}
		if owner.Owner == window {
			break
		}
		if time.Now().After(selectionDeadline) {
			t.Fatal("mouse selection did not claim PRIMARY")
		}
		time.Sleep(10 * time.Millisecond)
	}
	control, copy := x11Keycode(t, conn, setup, 0xffe3), x11Keycode(t, conn, setup, 'C')
	xtest.FakeInput(conn, xproto.KeyPress, byte(control), 0, 0, 0, 0, 0)
	xtest.FakeInput(conn, xproto.KeyPress, byte(shift), 0, 0, 0, 0, 0)
	xtest.FakeInput(conn, xproto.KeyPress, byte(copy), 0, 0, 0, 0, 0)
	xtest.FakeInput(conn, xproto.KeyRelease, byte(copy), 0, 0, 0, 0, 0)
	xtest.FakeInput(conn, xproto.KeyRelease, byte(shift), 0, 0, 0, 0, 0)
	xtest.FakeInput(conn, xproto.KeyRelease, byte(control), 0, 0, 0, 0, 0)
	conn.Sync()
	atom := func(name string) xproto.Atom {
		reply, err := xproto.InternAtom(conn, false, uint16(len(name)), name).Reply()
		if err != nil {
			t.Fatal(err)
		}
		return reply.Atom
	}
	clipboard, utf8, property := atom("CLIPBOARD"), atom("UTF8_STRING"), atom("ST_ZAT_TEST_CLIPBOARD")
	deadline := time.Now().Add(3 * time.Second)
	for {
		owner, err := xproto.GetSelectionOwner(conn, clipboard).Reply()
		if err != nil {
			t.Fatal(err)
		}
		if owner.Owner == window {
			break
		}
		if time.Now().After(deadline) {
			primary, _ := xproto.GetSelectionOwner(conn, xproto.AtomPrimary).Reply()
			pointer, _ := xproto.QueryPointer(conn, window).Reply()
			geometry, _ := xproto.GetGeometry(conn, xproto.Drawable(window)).Reply()
			clientOutput, _ := os.ReadFile(clientLog)
			t.Fatalf("Ctrl+Shift+C did not claim clipboard: primary=%+v pointer=%+v geometry=%+v drag=%d,%d..%d,%d keys=%d,%d,%d output=%q", primary, pointer, geometry, x1, y1, x2, y2, control, shift, copy, clientOutput)
		}
		time.Sleep(10 * time.Millisecond)
	}
	requestor, err := xproto.NewWindowId(conn)
	if err != nil {
		t.Fatal(err)
	}
	screen := setup.DefaultScreen(conn)
	if err := xproto.CreateWindowChecked(conn, screen.RootDepth, requestor, root, 0, 0, 1, 1, 0,
		xproto.WindowClassInputOutput, screen.RootVisual, 0, nil).Check(); err != nil {
		t.Fatal(err)
	}
	defer xproto.DestroyWindow(conn, requestor)
	xproto.ConvertSelection(conn, requestor, clipboard, utf8, property, xproto.TimeCurrentTime)
	deadline = time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		data, err := xproto.GetProperty(conn, false, requestor, property, utf8, 0, 1<<18).Reply()
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(data.Value), expected) {
			t.Logf("copied %d bytes of extracted text through X11", len(data.Value))
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("clipboard did not contain the selected PDF text")
}
