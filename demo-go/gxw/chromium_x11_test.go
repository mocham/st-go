package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"testing"
	"time"

	"github.com/jezek/xgb"
	"github.com/jezek/xgb/xproto"
	"github.com/jezek/xgb/xtest"
)

// Uses a fresh Chromium profile and real XTest keys. CDP only focuses/reads the
// HTML input; injecting text through CDP would bypass GTK/XIM and prove nothing.
func TestX11XIMWithChromium(t *testing.T) {
	for _, version := range []string{"default", "3", "no-xim"} {
		t.Run(version, func(t *testing.T) { testChromiumIME(t, version) })
	}
}

func testChromiumIME(t *testing.T, version string) {
	if os.Getenv("GXW_TEST_CHROMIUM") != "1" {
		t.Skip("set GXW_TEST_CHROMIUM=1 with make test-gxw-x11")
	}
	name := os.Getenv("CHROMIUM")
	if name == "" {
		name = "chromium"
	}
	path, err := exec.LookPath(name)
	if err != nil {
		t.Fatal(err)
	}
	display := testDisplay(t)
	w, err := newWM(options{display: display, fontSize: 16})
	if err != nil {
		t.Fatal(err)
	}
	defer w.close()
	buttonX, buttonY := -1, -1
	for _, hit := range w.hits[w.systembar] {
		if hit.action == "ime" {
			buttonX = hit.rect.Min.X + hit.rect.Dx()/2
			buttonY = w.workarea().Max.Y + hit.rect.Dy()/2
		}
	}
	if buttonX < 0 {
		t.Fatal("IME button missing")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- w.run(ctx) }()
	defer func() { cancel(); <-done }()
	inR, inW, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer inR.Close()
	defer inW.Close()
	outR, outW, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer outR.Close()
	defer outW.Close()
	args := []string{"--no-sandbox", "--disable-gpu", "--disable-dev-shm-usage", "--no-first-run", "--no-default-browser-check", "--disable-background-networking", "--ozone-platform=x11", "--remote-debugging-pipe", "--user-data-dir=" + t.TempDir(), "about:blank"}
	if version != "default" && version != "no-xim" {
		args = append(args, "--gtk-version="+version)
	}
	cmd := exec.CommandContext(ctx, path, args...)
	cmd.ExtraFiles = []*os.File{inR, outW}
	cmd.Env = append(os.Environ(), "DISPLAY="+display, "XMODIFIERS=@im=gxw", "GTK_IM_MODULE=xim", "LC_ALL=C.UTF-8")
	if version == "no-xim" {
		cmd.Env = append(cmd.Env, "XMODIFIERS=@im=none", "GTK_IM_MODULE=gtk-im-context-simple")
	}
	var logs bytes.Buffer
	cmd.Stderr = &logs
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	inR.Close()
	outW.Close()
	defer func() {
		cmd.Process.Kill()
		cmd.Wait()
		if t.Failed() {
			t.Log(logs.String())
		}
	}()
	reader := bufio.NewReader(outR)
	id := 0
	call := func(session, method string, params any) map[string]any {
		t.Helper()
		id++
		request := map[string]any{"id": id, "method": method, "params": params}
		if session != "" {
			request["sessionId"] = session
		}
		data, _ := json.Marshal(request)
		data = append(data, 0)
		if _, err := inW.Write(data); err != nil {
			t.Fatal(err)
		}
		for {
			data, err := reader.ReadBytes(0)
			if err != nil {
				t.Fatalf("CDP %s: %v", method, err)
			}
			var reply map[string]any
			if json.Unmarshal(data[:len(data)-1], &reply) != nil {
				continue
			}
			if reply["id"] != float64(id) {
				continue
			}
			if reply["error"] != nil {
				t.Fatal(reply)
			}
			result, _ := reply["result"].(map[string]any)
			return result
		}
	}
	page := "data:text/html," + url.PathEscape(`<meta charset="utf-8"><input id="entry" autofocus style="font-size:30px;width:90%">`)
	target := call("", "Target.createTarget", map[string]any{"url": page})["targetId"]
	session := call("", "Target.attachToTarget", map[string]any{"targetId": target, "flatten": true})["sessionId"].(string)
	call(session, "Page.bringToFront", map[string]any{})
	call(session, "Runtime.evaluate", map[string]any{"expression": `new Promise(resolve => { function ready() { let e=document.getElementById('entry'); if(!e) {setTimeout(ready,10);return} e.focus(); resolve(document.activeElement===e) } ready() })`, "awaitPromise": true, "returnByValue": true})
	conn, err := xgb.NewConnDisplay(display)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if err := xtest.Init(conn); err != nil {
		t.Fatal(err)
	}
	setup := xproto.Setup(conn)
	mapping, err := xproto.GetKeyboardMapping(conn, setup.MinKeycode, byte(int(setup.MaxKeycode)-int(setup.MinKeycode)+1)).Reply()
	if err != nil {
		t.Fatal(err)
	}
	code := func(sym xproto.Keysym) byte {
		for i, key := range mapping.Keysyms {
			if key == sym {
				return byte(setup.MinKeycode) + byte(i/int(mapping.KeysymsPerKeycode))
			}
		}
		t.Fatalf("key not mapped: %x", sym)
		return 0
	}
	typeKeys := func(text string) {
		for _, ch := range text {
			shift := ch >= 'A' && ch <= 'Z'
			if shift {
				xtest.FakeInput(conn, xproto.KeyPress, code(0xffe1), 0, 0, 0, 0, 0)
				ch += 32
			}
			if ch == 0x1b {
				ch = 0xff1b
			}
			key := code(xproto.Keysym(ch))
			xtest.FakeInput(conn, xproto.KeyPress, key, 0, 0, 0, 0, 0)
			xtest.FakeInput(conn, xproto.KeyRelease, key, 0, 0, 0, 0, 0)
			if shift {
				xtest.FakeInput(conn, xproto.KeyRelease, code(0xffe1), 0, 0, 0, 0, 0)
			}
		}
	}
	check := func(suffix, want string) {
		t.Helper()
		expression := fmt.Sprintf(`new Promise(resolve => {const e=document.getElementById('entry');const check=()=>{if(e.value.endsWith(%q))resolve(e.value)};e.addEventListener('input',check);check();setTimeout(()=>resolve(e.value),10000)})`, suffix)
		result := call(session, "Runtime.evaluate", map[string]any{"expression": expression, "awaitPromise": true, "returnByValue": true})
		value := fmt.Sprint(result["result"].(map[string]any)["value"])
		if value != want {
			t.Fatalf("Chromium committed %q, wanted %q", value, want)
		}
	}
	toggle := func() {
		xtest.FakeInput(conn, xproto.MotionNotify, 0, 0, 0, int16(buttonX), int16(buttonY), 0)
		xtest.FakeInput(conn, xproto.ButtonPress, 1, 0, 0, 0, 0, 0)
		xtest.FakeInput(conn, xproto.ButtonRelease, 1, 0, 0, 0, 0, 0)
	}
	for range 2 {
		typeKeys("[")
		result := call(session, "Runtime.evaluate", map[string]any{"expression": `new Promise(resolve => setTimeout(()=>resolve(document.getElementById('entry').value),150))`, "awaitPromise": true, "returnByValue": true})
		if value := result["result"].(map[string]any)["value"]; value != "" {
			t.Fatalf("captured bracket reached Chromium: %q", value)
		}
		grab, err := xproto.GrabKeyboard(conn, false, setup.DefaultScreen(conn).Root, xproto.TimeCurrentTime, xproto.GrabModeAsync, xproto.GrabModeAsync).Reply()
		if err != nil || grab.Status != xproto.GrabStatusAlreadyGrabbed {
			t.Fatal("gxw does not own the keyboard while holding bracket", grab, err)
		}
		state, err := xproto.GetProperty(conn, false, w.systembar, w.atoms["_NET_WM_NAME"], w.atoms["UTF8_STRING"], 0, 100).Reply()
		if err != nil || !bytes.Contains(state.Value, []byte("IME [")) {
			t.Fatal("first bracket did not ready system bar", state, err)
		}
	}
	typeKeys("[\x1b")
	typeKeys("a[b[[c[[[ni1]]]Z")
	check("Z", "a[b[[c你Z")
	toggle()
	typeKeys("[[[ni1]]]F")
	check("F", "a[b[[c你Z[[[ni1]]]F")
	toggle()
	typeKeys("[[[ABC\x1bQ")
	check("Q", "a[b[[c你Z[[[ni1]]]FABCQ")
	typeKeys("[")
	toggle()
	check("[", "a[b[[c你Z[[[ni1]]]FABCQ[")
	h := &xHarness{t: t, conn: conn, root: setup.DefaultScreen(conn).Root, atoms: w.atoms}
	h.wait("disabled helper releases keyboard", func() bool {
		grab, err := xproto.GrabKeyboard(conn, false, h.root, xproto.TimeCurrentTime, xproto.GrabModeAsync, xproto.GrabModeAsync).Reply()
		if err != nil || grab.Status != xproto.GrabStatusSuccess {
			return false
		}
		xproto.UngrabKeyboard(conn, xproto.TimeCurrentTime)
		return true
	})
	toggle()
	if version == "no-xim" {
		focus, err := xproto.GetInputFocus(conn).Reply()
		if err != nil {
			t.Fatal(err)
		}
		xproto.ChangeProperty(conn, xproto.PropModeReplace, focus.Focus, w.atoms["_ST_GO_DIRECT_INPUT"], xproto.AtomCardinal, 32, 1, []byte{1, 0, 0, 0})
		typeKeys("[[[literal")
		check("literal", "a[b[[c你Z[[[ni1]]]FABCQ[[[[literal")
	}
	t.Log("Chromium: WM grab, pending system bar, Chinese commit, bracket recovery, Escape and on/off button passed")
}
