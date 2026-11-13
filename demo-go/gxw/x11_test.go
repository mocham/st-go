package main

import (
	"bufio"
	"fmt"
	"image/png"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/jezek/xgb"
	"github.com/jezek/xgb/xproto"
	"github.com/jezek/xgb/xtest"
	"st-go/widgets/systembar"
)

// This test always starts an isolated X server; it never takes over DISPLAY.
func testDisplay(t *testing.T, geometry ...string) string {
	t.Helper()
	path, err := exec.LookPath("Xvfb")
	if err != nil {
		t.Fatal("test-gxw-x11 requires Xvfb")
	}
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	logPath := filepath.Join(t.TempDir(), "Xvfb.log")
	log, err := os.Create(logPath)
	if err != nil {
		t.Fatal(err)
	}
	defer log.Close()
	size := "1024x768x24"
	if len(geometry) > 0 {
		size = geometry[0]
	}
	cmd := exec.Command(path, "-displayfd", "3", "-screen", "0", size, "-nolisten", "tcp", "-noreset")
	cmd.ExtraFiles = []*os.File{w}
	cmd.Stdout, cmd.Stderr = log, log
	if err := cmd.Start(); err != nil {
		w.Close()
		t.Fatal(err)
	}
	w.Close()
	t.Cleanup(func() {
		cmd.Process.Kill()
		cmd.Wait()
		if t.Failed() {
			b, _ := os.ReadFile(logPath)
			t.Log(string(b))
		}
	})
	ready := make(chan string, 1)
	go func() { s, _ := bufio.NewReader(r).ReadString('\n'); ready <- strings.TrimSpace(s) }()
	select {
	case display := <-ready:
		if display != "" {
			return ":" + display
		}
	case <-time.After(8 * time.Second):
	}
	t.Fatal("isolated X server failed to start")
	return ""
}

func TestX11ScaledBarsConfigAndResize(t *testing.T) {
	if os.Getenv("GXW_TEST_X11") != "1" {
		t.Skip("run make test-gxw-x11")
	}
	display := testDisplay(t, "1920x1080x24")
	dir := t.TempDir()
	data, err := os.ReadFile("../../functionalities/latexpdf/pdf_assets/DroidSansFallbackFull.ttf")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "cjk.ttf"), data, 0600); err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(dir, "config.json")
	if err := os.WriteFile(configPath, []byte(`{"font":"cjk.ttf"}`), 0600); err != nil {
		t.Fatal(err)
	}
	w, err := newWM(options{display: display, fontSize: 16, configPath: configPath})
	if err != nil {
		t.Fatal(err)
	}
	defer w.close()
	if w.scale != 2 || w.fonts.fonts[0].Index('世') == 0 {
		t.Fatal("X screen/config did not select high-resolution CJK rendering")
	}
	h := &xHarness{t: t, conn: w.conn, root: w.root, atoms: w.atoms}
	id := h.window("世界 日本語", 0, false, false)
	w.manage(id, true)
	check := func() {
		t.Helper()
		area := w.workarea()
		for _, bar := range []xproto.Window{w.tabbar, w.systembar} {
			height := w.barHeight
			if bar == w.systembar {
				height = w.systemStyle.Height()
			}
			g, err := xproto.GetGeometry(w.conn, xproto.Drawable(bar)).Reply()
			if err != nil || int(g.Width) != w.width || int(g.Height) != height {
				t.Fatalf("scaled bar geometry: %+v %v", g, err)
			}
			if len(w.hits[bar]) == 0 {
				t.Fatal("scaled bar has no pointer targets")
			}
		}
		g, err := xproto.GetGeometry(w.conn, xproto.Drawable(id)).Reply()
		if err != nil || int(g.Y) != area.Min.Y || int(g.Height) != area.Dy() {
			t.Fatalf("client did not follow scaled workarea: %+v %v", g, err)
		}
		// The system bar uses exactly the framebuffer's cached cells, regardless
		// of the separately configured CJK tab font and its high-DPI multiplier.
		s := w.systemStyle
		pixels, err := xproto.GetImage(w.conn, xproto.ImageFormatZPixmap, xproto.Drawable(w.systembar), 0, 0, uint16(w.width), uint16(s.Height()), 0xffffffff).Reply()
		if err != nil {
			t.Fatal(err)
		}
		img, err := w.pixels.decode(pixels.Data, w.width, s.Height())
		if err != nil {
			t.Fatal(err)
		}
		for y := 0; y < s.CellHeight; y++ {
			for x := 0; x < s.CellWidth; x++ {
				got := img.RGBAAt(w.width-s.Padding-s.CellWidth+x, s.Padding+y)
				if got != systemColor(s.Glyphs[systembar.NewTerminalIcon][y*s.CellWidth+x]) {
					t.Fatal("system bar icon differs from framebuffer pixels")
				}
			}
		}
	}
	check()
	large := w.barHeight
	// Exercise the same final-geometry event path used by RandR/root resizes.
	w.event(xproto.ConfigureNotifyEvent{Window: w.root, Width: 1024, Height: 768})
	if w.scale != 1 || w.barHeight >= large {
		t.Fatal("screen shrink did not restore smaller bars")
	}
	check()
	w.event(xproto.ConfigureNotifyEvent{Window: w.root, Width: 1920, Height: 1080})
	if w.scale != 2 || w.barHeight != large {
		t.Fatal("screen growth did not restore larger bars")
	}
	check()
}

func TestX11ShrinkPreservesClientsAndDialogs(t *testing.T) {
	if os.Getenv("GXW_TEST_X11") != "1" {
		t.Skip("run make test-gxw-x11")
	}
	w, err := newWM(options{display: testDisplay(t), fontSize: 16})
	if err != nil {
		t.Fatal(err)
	}
	defer w.close()
	h := &xHarness{t: t, conn: w.conn, root: w.root, atoms: w.atoms}
	a := h.window("Editor", 0, false, false)
	w.manage(a, true)
	b := h.window("JupyterLab service", 0, false, false)
	w.manage(b, true)
	dialog := h.window("Service dialog", b, true, false)
	w.manage(dialog, true)
	find := func(id xproto.Window, action string) hit {
		t.Helper()
		for _, target := range w.hits[w.tabbar] {
			if target.window == id && target.action == action {
				return target
			}
		}
		t.Fatalf("missing %s target for %x", action, id)
		return hit{}
	}
	click := func(target hit) {
		w.event(xproto.ButtonPressEvent{Event: w.tabbar, Detail: 1, EventX: int16(target.rect.Min.X + target.rect.Dx()/2), EventY: int16(target.rect.Dy() / 2)})
	}
	full := find(b, "tab").rect.Dx()
	click(find(b, "shrink"))
	if !w.clients[b].compact || w.active != a || h.mapped(b) || h.mapped(dialog) || len(w.clients) != 3 {
		t.Fatal("shrink did not preserve/background the client family")
	}
	if icon := find(b, "tab"); icon.rect.Dx() >= full || icon.rect.Dx() > 2*w.barHeight {
		t.Fatal("shrunk service did not become a small icon", icon.rect)
	}
	h.text(b, "_NET_WM_NAME", "Service still running")
	w.event(xproto.PropertyNotifyEvent{Window: b, Atom: w.atoms["_NET_WM_NAME"]})
	w.event(xproto.ConfigureNotifyEvent{Window: w.root, Width: 1920, Height: 1080})
	if !w.clients[b].compact || w.clients[b].title != "Service still running" {
		t.Fatal("title/resize lost the compact service")
	}
	click(find(b, "tab"))
	if w.clients[b].compact || w.active != b || !h.mapped(b) || !h.mapped(dialog) || len(w.clients) != 3 {
		t.Fatal("numbered icon did not restore the same client family")
	}
	click(find(b, "shrink"))
	xproto.DestroyWindow(w.conn, a)
	w.remove(a)
	if len(w.tabs) != 1 || w.tabs[0] != b || !w.clients[b].compact {
		t.Fatal("closing another tab lost compact state or ordering")
	}
	click(find(b, "tab"))
	click(find(b, "shrink"))
	if w.active != b || !h.mapped(b) {
		t.Fatal("shrinking the last tab left no active application")
	}
}

type xHarness struct {
	t     *testing.T
	conn  *xgb.Conn
	root  xproto.Window
	atoms map[string]xproto.Atom
}

func (h *xHarness) atom(name string) xproto.Atom {
	if a := h.atoms[name]; a != 0 {
		return a
	}
	r, err := xproto.InternAtom(h.conn, false, uint16(len(name)), name).Reply()
	if err != nil {
		h.t.Fatal(err)
	}
	h.atoms[name] = r.Atom
	return r.Atom
}
func (h *xHarness) words(id xproto.Window, name string) []uint32 {
	r, err := xproto.GetProperty(h.conn, false, id, h.atom(name), xproto.GetPropertyTypeAny, 0, 4096).Reply()
	if err != nil || r.Format != 32 {
		return nil
	}
	v := make([]uint32, len(r.Value)/4)
	for i := range v {
		v[i] = xgb.Get32(r.Value[i*4:])
	}
	return v
}
func (h *xHarness) setWords(id xproto.Window, name string, typ xproto.Atom, words ...uint32) {
	b := make([]byte, len(words)*4)
	for i, v := range words {
		xgb.Put32(b[i*4:], v)
	}
	xproto.ChangeProperty(h.conn, xproto.PropModeReplace, id, h.atom(name), typ, 32, uint32(len(words)), b)
}
func (h *xHarness) text(id xproto.Window, name, value string) {
	xproto.ChangeProperty(h.conn, xproto.PropModeReplace, id, h.atom(name), h.atom("UTF8_STRING"), 8, uint32(len(value)), []byte(value))
}
func (h *xHarness) window(name string, parent xproto.Window, dialog, override bool) xproto.Window {
	id, err := xproto.NewWindowId(h.conn)
	if err != nil {
		h.t.Fatal(err)
	}
	ov := uint32(0)
	if override {
		ov = 1
	}
	xproto.CreateWindow(h.conn, 0, id, h.root, 50, 80, 320, 180, 3, xproto.WindowClassInputOutput, 0, xproto.CwBackPixel|xproto.CwOverrideRedirect|xproto.CwEventMask, []uint32{0x28465c, ov, xproto.EventMaskStructureNotify | xproto.EventMaskButtonPress | xproto.EventMaskButtonRelease})
	h.text(id, "_NET_WM_NAME", name)
	h.setWords(id, "WM_PROTOCOLS", xproto.AtomAtom, uint32(h.atom("WM_DELETE_WINDOW")))
	if parent != 0 {
		h.setWords(id, "WM_TRANSIENT_FOR", xproto.AtomWindow, uint32(parent))
	}
	if dialog {
		h.setWords(id, "_NET_WM_WINDOW_TYPE", xproto.AtomAtom, uint32(h.atom("_NET_WM_WINDOW_TYPE_DIALOG")))
	}
	return id
}
func (h *xHarness) mapWindow(id xproto.Window) { xproto.MapWindow(h.conn, id); h.conn.Sync() }
func (h *xHarness) mapped(id xproto.Window) bool {
	a, e := xproto.GetWindowAttributes(h.conn, id).Reply()
	return e == nil && a.MapState == xproto.MapStateViewable
}
func (h *xHarness) wait(label string, condition func() bool) {
	h.t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	h.t.Fatalf("timeout: %s; clients=%v active=%v", label, h.words(h.root, "_NET_CLIENT_LIST"), h.words(h.root, "_NET_ACTIVE_WINDOW"))
}
func (h *xHarness) active(id xproto.Window) bool {
	a := h.words(h.root, "_NET_ACTIVE_WINDOW")
	return len(a) == 1 && a[0] == uint32(id)
}
func (h *xHarness) message(id xproto.Window, name string, data ...uint32) {
	d := make([]uint32, 5)
	copy(d, data)
	e := xproto.ClientMessageEvent{Format: 32, Window: id, Type: h.atom(name), Data: xproto.ClientMessageDataUnionData32New(d)}
	if err := xproto.SendEventChecked(h.conn, false, h.root, xproto.EventMaskSubstructureRedirect|xproto.EventMaskSubstructureNotify, string(e.Bytes())).Check(); err != nil {
		h.t.Fatal(err)
	}
}
func (h *xHarness) keycode(sym uint32) byte {
	s := xproto.Setup(h.conn)
	r, err := xproto.GetKeyboardMapping(h.conn, s.MinKeycode, byte(int(s.MaxKeycode)-int(s.MinKeycode)+1)).Reply()
	if err != nil {
		h.t.Fatal(err)
	}
	for i, v := range r.Keysyms {
		if uint32(v) == sym {
			return byte(int(s.MinKeycode) + i/int(r.KeysymsPerKeycode))
		}
	}
	h.t.Fatalf("missing keysym %x", sym)
	return 0
}
func (h *xHarness) key(symbols ...uint32) {
	for _, sym := range symbols {
		if err := xtest.FakeInputChecked(h.conn, xproto.KeyPress, h.keycode(sym), 0, h.root, 0, 0, 0).Check(); err != nil {
			h.t.Fatal(err)
		}
	}
	for i := len(symbols) - 1; i >= 0; i-- {
		xtest.FakeInput(h.conn, xproto.KeyRelease, h.keycode(symbols[i]), 0, h.root, 0, 0, 0)
	}
	h.conn.Sync()
}
func (h *xHarness) click(x, y int) {
	xtest.FakeInput(h.conn, xproto.MotionNotify, 0, 0, h.root, int16(x), int16(y), 0)
	xtest.FakeInput(h.conn, xproto.ButtonPress, 1, 0, h.root, 0, 0, 0)
	xtest.FakeInput(h.conn, xproto.ButtonRelease, 1, 0, h.root, 0, 0, 0)
	h.conn.Sync()
}

func TestX11WindowManager(t *testing.T) {
	if os.Getenv("GXW_TEST_X11") != "1" {
		t.Skip("run make test-gxw-x11 for isolated Xvfb integration")
	}
	display := testDisplay(t)
	conn, err := xgb.NewConnDisplay(display)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		conn.Close()
		// Finish XGB's asynchronous close before the isolated server is killed.
		for {
			e, err := conn.WaitForEvent()
			if e == nil && err == nil {
				return
			}
		}
	}()
	h := &xHarness{t: t, conn: conn, root: xproto.Setup(conn).DefaultScreen(conn).Root, atoms: make(map[string]xproto.Atom)}
	if err := xtest.Init(conn); err != nil {
		t.Fatal(err)
	}
	a := h.window("First", 0, false, false)
	h.mapWindow(a)
	b := h.window("Second", 0, false, false)
	h.mapWindow(b)
	// A fixed-size hint on a normal window must not turn it into a dialog.
	hints := make([]uint32, 18)
	hints[0] = (1 << 4) | (1 << 5)
	hints[5], hints[6], hints[7], hints[8] = 320, 180, 320, 180
	h.setWords(b, "WM_NORMAL_HINTS", h.atom("WM_SIZE_HINTS"), hints...)
	binary := os.Getenv("GXW_TEST_BINARY")
	if binary == "" {
		binary, err = filepath.Abs("../../BINARIES/gxw")
		if err != nil {
			t.Fatal(err)
		}
	}
	logPath := filepath.Join(t.TempDir(), "gxw.log")
	log, err := os.Create(logPath)
	if err != nil {
		t.Fatal(err)
	}
	defer log.Close()
	shot := filepath.Join(t.TempDir(), "screenshot.png")
	cmd := exec.Command(binary, "--gxw-session", "-display", display, "-no-autostart", "-screenshot", shot)
	cmd.Env = append(os.Environ(), "GXW_XINIT_CHILD=1")
	cmd.Stdout, cmd.Stderr = log, log
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	exited := false
	t.Cleanup(func() {
		if !exited {
			cmd.Process.Kill()
			<-done
		}
		if t.Failed() {
			b, _ := os.ReadFile(logPath)
			t.Log(string(b))
		}
	})
	h.wait("adopt existing windows", func() bool { return len(h.words(h.root, "_NET_CLIENT_LIST")) == 2 })
	area := h.words(h.root, "_NET_WORKAREA")
	if len(area) != 4 || area[0] != 0 || area[1] == 0 || area[2] != 1024 || area[3] >= 768 {
		t.Fatalf("workarea %v", area)
	}
	assertFull := func(id xproto.Window) {
		t.Helper()
		g, err := xproto.GetGeometry(conn, xproto.Drawable(id)).Reply()
		if err != nil {
			t.Fatal(err)
		}
		if int(g.X) != int(area[0]) || int(g.Y) != int(area[1]) || uint32(g.Width) != area[2] || uint32(g.Height) != area[3] || g.BorderWidth != 0 {
			t.Fatalf("not fullscreen: %+v", g)
		}
	}
	assertFull(a)
	assertFull(b)
	// A second manager must fail without disturbing the incumbent.
	second := exec.Command(binary, "--gxw-session", "-display", display, "-no-autostart")
	second.Env = append(os.Environ(), "GXW_XINIT_CHILD=1")
	if out, err := second.CombinedOutput(); err == nil || !strings.Contains(string(out), "another window manager") {
		t.Fatalf("second WM: %v %s", err, out)
	}
	if len(h.words(h.root, "_NET_CLIENT_LIST")) != 2 {
		t.Fatal("second manager damaged root properties")
	}
	c := h.window("Third", 0, false, false)
	h.mapWindow(c)
	h.wait("new window focused", func() bool { return h.active(c) && h.mapped(c) && !h.mapped(a) && !h.mapped(b) })
	assertFull(c)
	xproto.ConfigureWindow(conn, c, xproto.ConfigWindowX|xproto.ConfigWindowY|xproto.ConfigWindowWidth|xproto.ConfigWindowHeight, []uint32{91, 92, 111, 112})
	conn.Sync()
	assertFull(c)
	h.key(0xffe9, '1') // Alt+1
	h.wait("Alt+1 switches tabs", func() bool { return h.active(a) && h.mapped(a) && !h.mapped(c) })
	h.key(0xffe5) // Caps Lock must not break shortcuts.
	h.key(0xffe9, '2')
	h.wait("Alt+2 with Caps Lock", func() bool { return h.active(b) })
	h.key(0xffe5)
	h.key(0xff7f) // Num Lock too.
	h.key(0xffe9, '3')
	h.wait("Alt+3 with Num Lock", func() bool { return h.active(c) })
	h.key(0xff7f)
	h.key(0xffe9, 'n')
	h.wait("Alt+n cycles", func() bool { return h.active(a) })
	h.click(1024*5/6, int(area[1])/2)
	h.wait("click tab", func() bool { return h.active(c) })
	d := h.window("Dialog", a, true, false)
	h.setWords(d, "_NET_WM_STATE", xproto.AtomAtom, uint32(h.atom("_NET_WM_STATE_MODAL")))
	h.mapWindow(d)
	h.wait("dialog activates its parent tab", func() bool { return h.active(d) && h.mapped(d) && h.mapped(a) && !h.mapped(c) })
	g, err := xproto.GetGeometry(conn, xproto.Drawable(d)).Reply()
	if err != nil {
		t.Fatal(err)
	}
	if g.Width != 320 || g.Height != 180 || g.BorderWidth != 2 || int(g.X) != (1024-324)/2 || int(g.Y) != int(area[1])+(int(area[3])-184)/2 {
		t.Fatalf("dialog not centered: %+v", g)
	}
	h.click(10, int(area[1])+10)
	h.wait("modal dialog retains focus", func() bool { f, err := xproto.GetInputFocus(conn).Reply(); return err == nil && f.Focus == d })
	h.key(0xffe9, '2')
	h.wait("dialog hidden with parent", func() bool { return h.active(b) && !h.mapped(d) && !h.mapped(a) })
	h.key(0xffe9, '1')
	h.wait("dialog restored with parent", func() bool { return h.active(d) && h.mapped(a) && h.mapped(d) })
	popup := h.window("Menu", 0, false, true)
	h.mapWindow(popup)
	if slices.Contains(h.words(h.root, "_NET_CLIENT_LIST"), uint32(popup)) {
		t.Fatal("managed override-redirect popup")
	}
	xproto.DestroyWindow(conn, popup)
	// Close sends WM_DELETE_WINDOW; it does not destroy a cooperating client.
	protocols := make(chan xproto.ClientMessageEvent, 32)
	go func() {
		for {
			e, err := conn.WaitForEvent()
			if e == nil && err == nil {
				return
			}
			if m, ok := e.(xproto.ClientMessageEvent); ok {
				select {
				case protocols <- m:
				default:
				}
			}
		}
	}()
	h.key(0xffe9, 0xffc1)
	select {
	case message := <-protocols:
		if message.Window != d || message.Type != h.atom("WM_PROTOCOLS") || message.Data.Data32[0] != uint32(h.atom("WM_DELETE_WINDOW")) {
			t.Fatalf("close protocol: %+v", message)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("missing WM_DELETE_WINDOW")
	}
	if !h.mapped(d) {
		t.Fatal("WM killed cooperating client")
	}
	xproto.DestroyWindow(conn, d)
	h.wait("dialog destruction restores parent", func() bool { return h.active(a) && len(h.words(h.root, "_NET_CLIENT_LIST")) == 3 })
	// Repeated switch/unmap notifications must not withdraw our own clients.
	for i := 0; i < 20; i++ {
		h.message([]xproto.Window{a, b, c}[i%3], "_NET_ACTIVE_WINDOW", 1, 0)
	}
	h.message(b, "_NET_ACTIVE_WINDOW", 1, 0)
	h.wait("rapid switches keep clients", func() bool { return h.active(b) && len(h.words(h.root, "_NET_CLIENT_LIST")) == 3 })
	xproto.UnmapWindow(conn, b)
	h.wait("client withdrawal", func() bool { return !slices.Contains(h.words(h.root, "_NET_CLIENT_LIST"), uint32(b)) })
	h.mapWindow(b)
	h.wait("withdrawn client remaps", func() bool { return h.active(b) && len(h.words(h.root, "_NET_CLIENT_LIST")) == 3 })
	h.message(b, "_NET_WM_STATE", 1, uint32(h.atom("_NET_WM_STATE_FULLSCREEN")), 0)
	h.wait("fullscreen request acknowledged", func() bool {
		return slices.Contains(h.words(b, "_NET_WM_STATE"), uint32(h.atom("_NET_WM_STATE_FULLSCREEN")))
	})
	assertFull(b)
	h.text(b, "_NET_WM_NAME", "Renamed Café Δ")
	h.key(0xff61)
	h.wait("screenshot", func() bool { _, err := os.Stat(shot); return err == nil })
	f, err := os.Open(shot)
	if err != nil {
		t.Fatal(err)
	}
	img, err := png.Decode(f)
	f.Close()
	if err != nil {
		t.Fatal(err)
	}
	if img.Bounds().Dx() != 1024 || img.Bounds().Dy() != 768 {
		t.Fatalf("screenshot size: %v", img.Bounds())
	}
	colors := map[string]bool{}
	for y := 0; y < int(area[1]); y++ {
		for x := 0; x < 1024; x++ {
			colors[fmt.Sprint(img.At(x, y))] = true
		}
	}
	if len(colors) < 8 {
		t.Fatalf("tab bar/font not rasterized: %d colors", len(colors))
	}
	if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		exited = true
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("WM did not exit")
	}
	h.wait("cleanup maps inactive clients", func() bool {
		return h.mapped(a) && h.mapped(b) && h.mapped(c) && len(h.words(h.root, "_NET_SUPPORTING_WM_CHECK")) == 0
	})
}
