package frontend

import (
	"encoding/binary"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/BurntSushi/xgb"
	"github.com/BurntSushi/xgb/xproto"
	"st-go/platform_dependent/x11"
)

// Keep old regression inputs intact while production code no longer accepts
// native events or exposes native resources in Terminal.
func native(t *Terminal) *x11.Driver {
	var d *x11.Driver
	if t.host == nil {
		d = x11.Attach(nil, 0, 0)
		d.Bind(t.eventCallbacks())
	} else {
		d = t.host.(*x11.Driver)
	}
	return d
}
func (t *Terminal) handleEvent(e xgb.Event) { native(t).Dispatch(e) }
func (t *Terminal) bpress(e xproto.ButtonPressEvent) {
	t.pointerPress(byte(e.Detail), e.State, int(e.EventX), int(e.EventY))
}
func (t *Terminal) brelease(e xproto.ButtonReleaseEvent) {
	t.pointerRelease(byte(e.Detail), e.State, int(e.EventX), int(e.EventY))
}
func (t *Terminal) bmotion(e xproto.MotionNotifyEvent) {
	t.pointerMotion(e.State, int(e.EventX), int(e.EventY))
}
func (t *Terminal) selrequest(e xproto.SelectionRequestEvent) { native(t).SelectionRequest(e) }
func (t *Terminal) selnotify(e xproto.SelectionNotifyEvent)   { native(t).SelectionNotify(e) }
func (t *Terminal) propnotify(e xproto.PropertyNotifyEvent)   { native(t).PropertyNotify(e) }
func (t *Terminal) verifyBarData(instance, class string)      { native(t).VerifyIdentity(instance, class) }
func getImage(t *Terminal) (*xproto.GetImageReply, error)     { return native(t).Image(fbW, fbH) }
func uint32Bytes(value uint32) []byte {
	var b [4]byte
	binary.LittleEndian.PutUint32(b[:], value)
	return b[:]
}
func surfaceWindow(p *regionPresenter) xproto.Window { return p.native.(*x11.Surface).WindowID() }

func TestMain(m *testing.M) {
	// Existing process/integration fixtures are repository-relative. Preserve
	// their original working directory after relocating this test package.
	root, err := os.Getwd()
	if _, statErr := os.Stat(filepath.Join(root, "Monaco_Linux.ttf")); statErr != nil && err == nil {
		root, err = filepath.Abs("../..")
	}
	if err == nil {
		err = os.Chdir(root)
	}
	if err == nil {
		embeddedFont, err = os.ReadFile("Monaco_Linux.ttf")
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	openWindow = x11.Open
	os.Exit(m.Run())
}
