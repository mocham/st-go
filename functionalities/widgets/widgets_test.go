package widgets

import (
	"image"
	"slices"
	"sync"
	"testing"
	"time"

	"st-go/platform_neutral/api"
)

type testHost struct {
	api.Host
	callbacks            api.Callbacks
	pixels               Buffer
	surfaces             []*testSurface
	cursor               api.TextCursor
	closed               chan struct{}
	once                 sync.Once
	queries              chan func(string, bool)
	synchronousClipboard bool
	restoring, restored  chan struct{}
}

func newTestHost(t *testing.T) (*testHost, api.Widget) {
	t.Helper()
	n := &testHost{closed: make(chan struct{}), queries: make(chan func(string, bool), 8)}
	n.pixels.Resize(12, 8)
	h, err := Wrap(n)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(h.Close)
	return n, h
}
func (n *testHost) Bind(c api.Callbacks)           { n.callbacks = c }
func (n *testHost) ClientSize() (int, int, error)  { return n.pixels.Width, n.pixels.Height, nil }
func (n *testHost) SetTextCursor(c api.TextCursor) { n.cursor = c }
func (n *testHost) SetTitle(string)                {}
func (n *testHost) SetIconTitle(string)            {}
func (n *testHost) Close()                         { n.once.Do(func() { close(n.closed) }) }
func (n *testHost) Run() {
	<-n.closed
	if n.restoring != nil {
		close(n.restoring)
		<-n.restored
	}
}
func (n *testHost) Upload(p []uint32, stride, x, y, w, h int) {
	n.UploadAt(p, stride, image.Rect(x, y, x+w, y+h), image.Pt(x, y))
}
func (n *testHost) UploadAt(p []uint32, stride int, r image.Rectangle, at image.Point) {
	n.pixels.Copy(p, stride, r, at)
}
func (n *testHost) NewSurface(r image.Rectangle, bg uint32) (api.Surface, error) {
	s := &testSurface{bounds: r, visible: true}
	n.surfaces = append(n.surfaces, s)
	return s, nil
}
func (n *testHost) QueryClipboard(_ byte, reply func(string, bool)) {
	if n.synchronousClipboard {
		reply("synchronous", true)
		return
	}
	n.queries <- reply
}
func (n *testHost) dispatch(call func(api.Callbacks)) {
	n.callbacks.Lock()
	defer n.callbacks.Unlock()
	call(n.callbacks)
}

type testSurface struct {
	bounds          image.Rectangle
	visible, closed bool
	pixels          []uint32
	frames          int
	exposes         int
}

func (s *testSurface) Configure(r image.Rectangle, _ uint32) { s.bounds = r }
func (s *testSurface) Present(p []uint32, _, _, _ int, _ bool) {
	s.pixels = slices.Clone(p)
	s.frames++
}
func (s *testSurface) SetVisible(visible bool) { s.visible = visible }
func (s *testSurface) Expose() bool            { s.exposes++; return s.visible && !s.closed }
func (s *testSurface) Close()                  { s.closed = true }

func TestBuffersClipTranslateAndRemainIndependent(t *testing.T) {
	n, root := newTestHost(t)
	other, err := root.NewWidget(api.WidgetOptions{Kind: api.TerminalWidget, Bounds: image.Rect(5, 2, 9, 5)})
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	if root.ID() == other.ID() || root.ID() == 0 || other.Kind() != api.TerminalWidget {
		t.Fatal("widgets lack distinct identity")
	}
	p := []uint32{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12}
	other.Upload(p, 4, 1, 1, 10, 10)
	if n.pixels.Pixels[3*12+6] != 6 || n.pixels.Pixels[4*12+8] != 12 || n.pixels.Pixels[2*12+5] != 0 {
		t.Fatal("destination translation changed source indexing")
	}
	other.SetVisible(false)
	other.Upload([]uint32{99}, 1, 0, 0, 1, 1)
	other.SetVisible(true)
	if n.pixels.Pixels[3*12+6] != 6 || n.pixels.Pixels[2*12+5] != 0 {
		t.Fatal("hidden upload replaced retained committed pixels")
	}
	root.Upload([]uint32{77}, 1, 0, 0, 1, 1)
	if n.pixels.Pixels[0] != 77 || n.pixels.Pixels[3*12+6] != 6 {
		t.Fatal("widget buffers are aliased")
	}
}

func TestFocusPointerCaptureAndHiddenInput(t *testing.T) {
	n, root := newTestHost(t)
	root.SetBounds(image.Rect(0, 0, 6, 8))
	other, err := root.NewWidget(api.WidgetOptions{Bounds: image.Rect(6, 0, 12, 8)})
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	var a, b string
	var motion, release image.Point
	root.Bind(api.Callbacks{Text: func(s string) { a += s }})
	other.Bind(api.Callbacks{Text: func(s string) { b += s }, PointerMotion: func(_ uint16, x, y int) { motion = image.Pt(x, y) }, PointerRelease: func(_ byte, _ uint16, x, y int) { release = image.Pt(x, y) }})
	n.dispatch(func(c api.Callbacks) {
		c.Text("a")
		c.PointerPress(1, 0, 8, 3)
		c.Text("b")
		c.PointerMotion(0, 2, 4)
		c.PointerRelease(1, 0, 2, 4)
	})
	if a != "a" || b != "b" || motion != image.Pt(-4, 4) || release != motion {
		t.Fatal("input focus/local coordinates/capture lost", a, b, motion, release)
	}
	n.dispatch(func(c api.Callbacks) { c.PointerPress(1, 0, 8, 3) })
	other.SetVisible(false)
	root.FocusWidget()
	n.dispatch(func(c api.Callbacks) { c.Text("c"); c.PointerMotion(0, 2, 5); c.PointerRelease(1, 0, 2, 5) })
	other.SetVisible(true)
	if a != "ac" || b != "b" || release != image.Pt(-4, 4) {
		t.Fatal("hidden widget received events or unmatched release was redirected")
	}
}

func TestPreviewVisibilityAndClippingFollowOwner(t *testing.T) {
	n, root := newTestHost(t)
	root.SetBounds(image.Rect(4, 2, 8, 6))
	s, err := root.NewSurface(image.Rect(-1, 0, 3, 2), 0xff000000)
	if err != nil {
		t.Fatal(err)
	}
	s.Present([]uint32{1, 2, 3, 4, 5, 6, 7, 8}, 4, 2, 0, false)
	native := n.surfaces[0]
	if native.bounds != image.Rect(4, 2, 7, 4) || !slices.Equal(native.pixels, []uint32{2, 3, 4, 6, 7, 8}) {
		t.Fatal("preview was not clipped in widget-local coordinates", native.bounds, native.pixels)
	}
	root.SetVisible(false)
	frames := native.frames
	s.Present(make([]uint32, 8), 4, 2, 0, false)
	if native.visible || s.Expose() || native.frames != frames {
		t.Fatal("hidden preview remained mapped or accepted painting")
	}
	generation, _ := root.(api.WidgetPresentation).PresentationState()
	root.SetVisible(true)
	frames = native.frames // activation restores only the previously committed preview
	if s.(api.GuardedSurface).PresentIfCurrent(generation, make([]uint32, 8), 4, 2, 0, false) || native.frames != frames {
		t.Fatal("stale preview painted after reactivation")
	}
	if !native.visible || !s.Expose() {
		t.Fatal("preview did not reactivate")
	}
	root.Close()
	if !native.closed {
		t.Fatal("preview outlived terminal widget")
	}
}

func TestBarIsPassiveAndKeepsOwnPixels(t *testing.T) {
	n, root := newTestHost(t)
	bar, err := root.NewWidget(api.WidgetOptions{Kind: api.BarWidget, Bounds: image.Rect(8, 0, 12, 2), PointerTransparent: true})
	if err != nil {
		t.Fatal(err)
	}
	presses := 0
	root.Bind(api.Callbacks{PointerPress: func(byte, uint16, int, int) { presses++ }})
	bar.Upload([]uint32{1, 2, 3, 4, 5, 6, 7, 8}, 4, 0, 0, 4, 2)
	n.dispatch(func(c api.Callbacks) { c.PointerPress(4, 0, 10, 1) })
	n.dispatch(func(c api.Callbacks) { c.ExposeSurface(n.surfaces[0]) })
	n.dispatch(func(c api.Callbacks) { c.ExposeSurface(nil) }) // unknown/stale native child
	if n.surfaces[0].exposes != 1 {
		t.Fatal("native bar expose did not restore its retained surface")
	}
	if presses != 1 || !slices.Equal(n.surfaces[0].pixels, []uint32{1, 2, 3, 4, 5, 6, 7, 8}) {
		t.Fatal("bar changed passive overlay behavior")
	}
	bar.Close()
	select {
	case <-n.closed:
		t.Fatal("closing bar closed terminal")
	default:
	}
}

func TestClipboardReplyKeepsOwnerAndPasteEpoch(t *testing.T) {
	n, root := newTestHost(t)
	other, err := root.NewWidget(api.WidgetOptions{Bounds: image.Rect(6, 0, 12, 8)})
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	var lock sync.Mutex
	pastes := make(chan string, 4)
	root.Bind(api.Callbacks{Lock: lock.Lock, Unlock: lock.Unlock, Paste: func(s string) { pastes <- s }})
	// RequestPaste is called with the terminal lock held. Even a synchronous
	// backend reply must not recursively acquire that lock.
	lock.Lock()
	root.RequestPaste()
	lock.Unlock()
	var reply func(string, bool)
	select {
	case reply = <-n.queries:
	case <-time.After(time.Second):
		t.Fatal("clipboard request blocked")
	}
	other.FocusWidget()
	root.FocusWidget()
	reply("stale", true)
	select {
	case <-pastes:
		t.Fatal("paste survived a focus change")
	default:
	}
	answer := make(chan string, 1)
	root.QueryClipboard('c', func(s string, _ bool) { answer <- s })
	select {
	case reply = <-n.queries:
	case <-time.After(time.Second):
		t.Fatal("query blocked")
	}
	root.SetVisible(false)
	other.FocusWidget()
	reply("protocol", true)
	select {
	case s := <-answer:
		if s != "protocol" {
			t.Fatal(s)
		}
	case <-time.After(time.Second):
		t.Fatal("hidden protocol reply was lost")
	}
}

func TestSynchronousPasteReplyDoesNotReenterClientLock(t *testing.T) {
	n, root := newTestHost(t)
	n.synchronousClipboard = true
	var mu sync.Mutex
	pasted := make(chan string, 1)
	root.Bind(api.Callbacks{Lock: mu.Lock, Unlock: mu.Unlock, Paste: func(text string) { pasted <- text }})
	mu.Lock()
	root.RequestPaste()
	mu.Unlock()
	select {
	case text := <-pasted:
		if text != "synchronous" {
			t.Fatal(text)
		}
	case <-time.After(time.Second):
		t.Fatal("synchronous clipboard reply deadlocked")
	}
}

func TestLastTerminalRunWaitsForNativeRestoration(t *testing.T) {
	n, root := newTestHost(t)
	other, err := root.NewWidget(api.WidgetOptions{Bounds: image.Rect(6, 0, 12, 8)})
	if err != nil {
		t.Fatal(err)
	}
	n.restoring, n.restored = make(chan struct{}), make(chan struct{})
	done := make(chan struct{})
	go func() { root.Run(); close(done) }()
	other.Close()
	select {
	case <-n.closed:
		t.Fatal("one PTY closed another")
	default:
	}
	root.Close()
	select {
	case <-n.restoring:
	case <-time.After(time.Second):
		t.Fatal("native host did not stop")
	}
	select {
	case <-done:
		t.Fatal("Run returned before console restoration")
	default:
	}
	close(n.restored)
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Run did not return after restoration")
	}
}
