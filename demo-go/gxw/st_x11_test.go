package main

import (
	"bytes"
	"context"
	"fmt"
	"image"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"

	"github.com/jezek/xgb"
	"github.com/jezek/xgb/xproto"
	"github.com/jezek/xgb/xtest"
)

func TestX11STStartsAtFinalWorkarea(t *testing.T) {
	if os.Getenv("GXW_TEST_X11") != "1" {
		t.Skip("run make test-gxw-x11")
	}
	binary := os.Getenv("GXW_TEST_ST_BINARY")
	if binary == "" {
		var err error
		binary, err = filepath.Abs("../../BINARIES/st-min")
		if err != nil {
			t.Fatal(err)
		}
	}
	display := testDisplay(t)
	w, err := newWM(options{display: display, fontSize: 16})
	if err != nil {
		t.Fatal(err)
	}
	defer w.close()
	area := w.workarea()
	observer, err := xgb.NewConnDisplay(display)
	if err != nil {
		t.Fatal(err)
	}
	defer observer.Close()
	if err := xtest.Init(observer); err != nil {
		t.Fatal(err)
	}
	if err := xproto.ChangeWindowAttributesChecked(observer, w.root, xproto.CwEventMask, []uint32{xproto.EventMaskSubstructureNotify}).Check(); err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	created := make(map[xproto.Window]image.Rectangle)
	configured := make(map[xproto.Window][]image.Point)
	eventsDone := make(chan struct{})
	go func() {
		defer close(eventsDone)
		for {
			e, err := observer.WaitForEvent()
			if err != nil || e == nil {
				return
			}
			mu.Lock()
			switch e := e.(type) {
			case xproto.CreateNotifyEvent:
				created[e.Window] = image.Rect(int(e.X), int(e.Y), int(e.X)+int(e.Width), int(e.Y)+int(e.Height))
			case xproto.ConfigureNotifyEvent:
				configured[e.Window] = append(configured[e.Window], image.Pt(int(e.Width), int(e.Height)))
			}
			mu.Unlock()
		}
	}()
	defer func() { observer.Close(); <-eventsDone }()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- w.run(ctx) }()
	defer func() { cancel(); <-done }()
	h := &xHarness{t: t, conn: observer, root: w.root, atoms: map[string]xproto.Atom{}}
	dir := t.TempDir()
	cfg := filepath.Join(dir, "st.json")
	if err := os.WriteFile(cfg, []byte(`{"cols":27,"rows":9,"glyphwidth":8,"glyphheight":16,"glyphbaseline":12,"borderpx":1,"webp_cache_path":""}`), 0600); err != nil {
		t.Fatal(err)
	}
	launch := func(name string, want image.Rectangle) xproto.Window {
		t.Helper()
		sizeFile := filepath.Join(dir, name+".size")
		cmd := exec.Command(binary, "-config", cfg, "-t", name, "-c", "custom-st-class", "-e", "/bin/sh", "-c", `stty size > "$1"; IFS= read -r stop`, "st-test", sizeFile)
		cmd.Env = append(os.Environ(), "DISPLAY="+display)
		var logs bytes.Buffer
		cmd.Stderr = &logs
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		deferCleanup := func() {
			cmd.Process.Kill()
			cmd.Wait()
			if t.Failed() {
				t.Log(logs.String())
			}
		}
		t.Cleanup(deferCleanup)
		var window xproto.Window
		h.wait("st mapped", func() bool {
			for _, id := range h.words(h.root, "_NET_CLIENT_LIST") {
				r, err := xproto.GetProperty(observer, false, xproto.Window(id), h.atom("_NET_WM_NAME"), h.atom("UTF8_STRING"), 0, 256).Reply()
				if err == nil && string(r.Value) == name {
					window = xproto.Window(id)
					return h.mapped(window)
				}
			}
			return false
		})
		h.wait("first PTY geometry", func() bool { return readValue(sizeFile) != "" })
		// The terminal's helper band is 16+4 pixels; it is reserved before exec.
		expected := fmt.Sprintf("%d %d", (want.Dy()-20-2)/16, (want.Dx()-2)/8)
		if got := readValue(sizeFile); got != expected {
			t.Fatalf("first PTY size %q, want %q", got, expected)
		}
		h.wait("initial CreateNotify", func() bool { mu.Lock(); defer mu.Unlock(); _, ok := created[window]; return ok })
		mu.Lock()
		initial := created[window]
		mu.Unlock()
		if initial != want {
			t.Fatalf("st created at %v, then tiled to %v", initial, want)
		}
		if direct := h.words(window, "_ST_GO_DIRECT_INPUT"); len(direct) != 1 || direct[0] != 1 {
			t.Fatal("custom-class st was not identified")
		}
		return window
	}
	st := launch("gxw-st-initial", area)
	other := h.window("other app", 0, false, false)
	h.mapWindow(other)
	h.wait("other app active", func() bool { return !h.mapped(st) })
	h.key(0xffe9, '1')
	h.wait("st restored", func() bool { return h.mapped(st) })
	mu.Lock()
	sizes := append([]image.Point(nil), configured[st]...)
	mu.Unlock()
	for _, size := range sizes {
		if size != area.Size() {
			t.Fatalf("startup/tab switch resized st: %v, want %v", sizes, area.Size())
		}
	}
	// Exercise the real root ConfigureNotify route and verify a later terminal
	// queries fresh allocation, rather than inheriting stale startup dimensions.
	e := xproto.ConfigureNotifyEvent{Event: w.root, Window: w.root, Width: 900, Height: 640}
	xproto.SendEvent(observer, false, w.root, xproto.EventMaskStructureNotify, string(e.Bytes()))
	var smaller image.Rectangle
	h.wait("changed workarea", func() bool {
		a := h.words(h.root, "_NET_WORKAREA")
		if len(a) != 4 || a[2] != 900 {
			return false
		}
		smaller = image.Rect(int(a[0]), int(a[1]), int(a[0]+a[2]), int(a[1]+a[3]))
		return true
	})
	h.wait("existing st follows real screen resize", func() bool {
		g, err := xproto.GetGeometry(observer, xproto.Drawable(st)).Reply()
		return err == nil && int(g.Width) == smaller.Dx() && int(g.Height) == smaller.Dy()
	})
	launch("gxw-st-after-resize", smaller)
}
