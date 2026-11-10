package frontend

import (
	"encoding/binary"
	"image"
	"os"
	"testing"
	"time"

	"github.com/BurntSushi/xgb"
	"github.com/BurntSushi/xgb/xproto"
	"github.com/BurntSushi/xgb/xtest"
	"st-go/platform_dependent/x11"
	"st-go/term"
)

// Run against an isolated server with ST_TEST_PREVIEW_X11=1 DISPLAY=:N.
func TestPreviewX11RetainedScrollResizeAndMouse(t *testing.T) {
	if os.Getenv("ST_TEST_PREVIEW_X11") != "1" {
		t.Skip("requires an isolated X server")
	}
	conn, err := xgb.NewConn()
	if err != nil {
		t.Fatal(err)
	}
	trm := regionTestTerminal(t)
	screen := xproto.Setup(conn).DefaultScreen(conn)
	window, err := xproto.NewWindowId(conn)
	if err != nil {
		t.Fatal(err)
	}
	if err = xproto.CreateWindowChecked(conn, 24, window, screen.Root, 0, 0, uint16(trm.fbW), uint16(trm.fbH), 0, xproto.WindowClassInputOutput, screen.RootVisual,
		xproto.CwEventMask, []uint32{xproto.EventMaskButtonPress | xproto.EventMaskButtonRelease}).Check(); err != nil {
		t.Fatal(err)
	}
	gc, err := xproto.NewGcontextId(conn)
	if err != nil {
		t.Fatal(err)
	}
	if err = xproto.CreateGCChecked(conn, gc, xproto.Drawable(window), 0, nil).Check(); err != nil {
		t.Fatal(err)
	}
	trm.host = x11.Attach(conn, window, gc)
	trm.host.Bind(trm.eventCallbacks())
	xproto.MapWindow(conn, window)
	conn.Sync()
	trm.writeTerminalOutput(trm.termCore, []byte("\x1bPregion stack 1\x1b\\"))
	s := &trm.displayRegions
	s.mu.Lock()
	s.jobs = make(chan regionJob, 1)
	surface := s.secondary
	surface.request = &term.RegionImage{Continuous: true, Path: "fixture.pdf"}
	surface.viewWidth = s.viewport.Dx()
	surface.bitmap = regionBitmap{width: s.viewport.Dx(), height: 800, contentHeight: 800, pixels: make([]uint32, s.viewport.Dx()*800)}
	for i := range surface.bitmap.pixels {
		surface.bitmap.pixels[i] = 0xff000000 | uint32(i*37)&0xffffff
	}
	s.mu.Unlock()
	p := &s.presenter
	check := func(y float64) {
		t.Helper()
		deadline := time.Now().Add(3 * time.Second)
		for {
			p.mu.Lock()
			ready := p.last != nil && p.last.surface.scrollY == y && p.last.epoch == p.epoch
			if ready {
				frame := *p.last
				w, h := frame.pane.Dx(), frame.pane.Dy()
				pixels := make([]uint32, w*h)
				renderRegionPixels(frame.surface, frame.viewport.Sub(frame.pane.Min), image.Rect(0, 0, w, h), frame.background, pixels, w)
				got, err := xproto.GetImage(conn, xproto.ImageFormatZPixmap, xproto.Drawable(surfaceWindow(p)), 0, 0, uint16(w), uint16(h), 0xffffffff).Reply()
				p.mu.Unlock()
				if err != nil {
					t.Fatal(err)
				}
				for i, pixel := range pixels {
					if actual := binary.LittleEndian.Uint32(got.Data[i*4:]); actual&0xffffff != pixel&0xffffff {
						t.Fatalf("X preview differs at y=%g pixel=%d: %#x want %#x", y, i, actual, pixel)
					}
				}
				return
			}
			p.mu.Unlock()
			if time.Now().After(deadline) {
				t.Fatal("X preview did not publish")
			}
			time.Sleep(time.Millisecond)
		}
	}
	for _, y := range []float64{100, 105, 103, 103.4, 150} {
		s.mu.Lock()
		surface.scrollY, surface.targetY = y, y
		surface.dirty = true
		trm.requestRegionPaint()
		s.mu.Unlock()
		check(y)
	}
	// Parent terminal erases cannot overwrite the child surface.
	trm.writeTerminalOutput(trm.termCore, []byte("\x1b[2J\x1b[H"))
	check(150)
	// Expose during synchronized output restores the committed X pixmap.
	trm.writeTerminalOutput(trm.termCore, []byte("\x1b[?2026h"))
	p.mu.Lock()
	xproto.ClearArea(conn, false, surfaceWindow(p), 0, 0, 0, 0)
	previewWindow := surfaceWindow(p)
	p.mu.Unlock()
	trm.handleEvent(xproto.ExposeEvent{Window: previewWindow})
	check(150)
	trm.writeTerminalOutput(trm.termCore, []byte("\x1b[?2026l"))
	// Real pointer events on the child must propagate in parent coordinates.
	if err := xtest.Init(conn); err != nil {
		t.Fatal(err)
	}
	x, y := int16(s.viewport.Min.X+5), int16(s.viewport.Min.Y+5)
	xtest.FakeInput(conn, xproto.MotionNotify, 0, 0, screen.Root, x, y, 0)
	xtest.FakeInput(conn, xproto.ButtonPress, 5, 0, 0, 0, 0, 0)
	xtest.FakeInput(conn, xproto.ButtonRelease, 5, 0, 0, 0, 0, 0)
	conn.Sync()
	pressed, released := false, false
	for {
		event, err := conn.PollForEvent()
		if err != nil {
			t.Fatal(err)
		}
		if event == nil {
			break
		}
		switch e := event.(type) {
		case xproto.ButtonPressEvent:
			if e.Event != window || e.EventX != x || e.EventY != y {
				t.Fatalf("wrong propagated coordinates: %+v", e)
			}
			trm.bpress(e)
			pressed = true
		case xproto.ButtonReleaseEvent:
			trm.brelease(e)
			released = true
		}
	}
	if !pressed || !released || trm.regionButtons != 0 {
		t.Fatal("preview child lost wheel press/release")
	}
	s.mu.Lock()
	if surface.targetY <= 150 {
		t.Error("propagated wheel did not scroll")
	}
	surface.scrollY, surface.targetY = 150, 150
	s.mu.Unlock()
	xproto.ConfigureWindow(conn, window, xproto.ConfigWindowWidth|xproto.ConfigWindowHeight, []uint32{122, 66})
	conn.Sync()
	trm.resize(122, 66)
	s.mu.Lock()
	position := surface.scrollY
	s.mu.Unlock()
	check(position)
	if err := trm.SetRegions(nil); err != nil {
		t.Fatal(err)
	}
	tree, err := xproto.QueryTree(conn, window).Reply()
	if err != nil || len(tree.Children) != 0 {
		t.Fatalf("reset retained preview child: %v %+v", err, tree)
	}
	// Recreate after reset and close with an active painter/upload lifecycle.
	trm.writeTerminalOutput(trm.termCore, []byte("\x1bPregion split 50%\x1b\\"))
}
