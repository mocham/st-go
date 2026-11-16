//go:build linux && ffmpeg_static && cgo && !purego

package playback

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"st-go/platform_neutral/api"
)

func TestNativeDrawIntoPitchedCallerBuffer(t *testing.T) {
	d, err := openNative(context.Background(), testMovie(t))
	if err != nil {
		t.Fatal(err)
	}
	defer d.close()
	for {
		kind, _, err := d.next()
		if err != nil {
			t.Fatal(err)
		}
		if kind == 1 {
			break
		}
		if kind == 0 {
			t.Fatal("missing picture")
		}
	}
	const w, h, stride = 160, 120, 192
	p := make([]uint32, stride*h+16)
	for i := range p {
		p[i] = 0xdeadbeef
	}
	if err := d.draw(p[:stride*h], w, h, stride); err != nil {
		t.Fatal(err)
	}
	want, err := d.pixels(w, h)
	if err != nil {
		t.Fatal(err)
	}
	for y := range h {
		for x := range stride {
			v := uint32(0xdeadbeef)
			if x < w {
				v = want[y*w+x]
			}
			if p[y*stride+x] != v {
				t.Fatalf("pixel %d,%d = %x, want %x", x, y, p[y*stride+x], v)
			}
		}
	}
	if err := d.draw(p[:100], w, h, stride); err == nil {
		t.Fatal("accepted short buffer")
	}
	if err := d.draw(p, w, h, w-1); err == nil {
		t.Fatal("accepted invalid pitch")
	}
	if err := d.seek(0); err != nil {
		t.Fatal(err)
	}
	if err := d.draw(p, w, h, stride); err != nil {
		t.Fatal(err)
	}
	for y := range h {
		for x := range w {
			if p[y*stride+x] != 0xff000000 {
				t.Fatal("stale picture after seek")
			}
		}
	}
}

type testOutput struct {
	mu     sync.Mutex
	frames int
	err    error
}

func (o *testOutput) Present(render FrameRenderer, _ api.VideoStatus) error {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.err != nil {
		return o.err
	}
	p := make([]uint32, 176*120+16)
	if err := render(p, 160, 120, 176); err != nil {
		return err
	}
	o.frames++
	return nil
}

func TestDirectOutputSuspendPreservesUserPause(t *testing.T) {
	o := &testOutput{}
	p := StartOutput(testMovie(t), 100, true, o)
	defer func() { p.Close(); <-p.Done() }()
	awaitPlayer(t, p, func(s api.VideoStatus) bool { return s.PositionMS >= 100 })
	p.Suspend(true)
	s := awaitPlayer(t, p, func(s api.VideoStatus) bool { return s.State == "paused" })
	time.Sleep(120 * time.Millisecond)
	if p.Status().PositionMS != s.PositionMS {
		t.Fatal("playback advanced on inactive VT")
	}
	p.Suspend(false)
	awaitPlayer(t, p, func(s api.VideoStatus) bool { return s.State == "playing" && s.PositionMS > 100 })
	if err := p.Control(api.VideoRequest{Action: "toggle"}); err != nil {
		t.Fatal(err)
	}
	awaitPlayer(t, p, func(s api.VideoStatus) bool { return s.State == "paused" })
	p.Suspend(true)
	p.Suspend(false)
	if err := p.Control(api.VideoRequest{Action: "speed", Speed: 1.1}); err != nil {
		t.Fatal(err)
	}
	s = awaitPlayer(t, p, func(s api.VideoStatus) bool { return s.Speed == 1.1 })
	if s.State != "paused" {
		t.Fatal("VT acquire undid user pause")
	}
	// A device failure must propagate and stop the worker before its owner frees
	// native buffers; no callbacks may outlive Done.
	o.mu.Lock()
	o.err = errors.New("scanout lost")
	o.mu.Unlock()
	p.Resize(0, 0)
	select {
	case <-p.Done():
	case <-time.After(time.Second):
		t.Fatal("output failure did not stop worker")
	}
	if p.Status().Error != "scanout lost" {
		t.Fatal(p.Status())
	}
}
