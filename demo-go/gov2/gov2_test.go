package main

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"st-go/platform_neutral/api"
)

func TestOptionsAndControls(t *testing.T) {
	path := filepath.Join(t.TempDir(), "movie ' ; 世界.mp4")
	if err := os.WriteFile(path, nil, 0600); err != nil {
		t.Fatal(err)
	}
	o, err := parseOptions([]string{"-mute", "-volume=35", "-drm-device=/dev/dri/card1", "-connector=42", "--", path}, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	request := o.request()
	if request.Player != "gov2" || request.File != path || !request.Muted || request.Volume != 35 || request.DRMDevice != "/dev/dri/card1" || request.Connector != 42 {
		t.Fatal(request)
	}
	for _, args := range [][]string{{"-volume=101", path}, {"-drm-device=/dev/dri/renderD128", path}, {filepath.Dir(path)}, {}} {
		if _, err := parseOptions(args, io.Discard); err == nil {
			t.Fatal("accepted invalid options", args)
		}
	}
	c := controls{volume: 35, speed: 1}
	status := api.VideoStatus{Volume: 35, Speed: 1, PositionMS: 2500}
	var r api.VideoRequest
	for range 3 {
		r, _ = c.key("+", status)
	}
	if r.Volume != 50 {
		t.Fatal("volume keys did not accumulate", r)
	}
	for range 100 {
		r, _ = c.key("]", status)
	}
	if r.Speed != 4 {
		t.Fatal("speed did not clamp", r)
	}
	r, _ = c.key("left", status)
	if r.PositionMS != 0 || r.Action != "seek" {
		t.Fatal(r)
	}
	if _, quit := c.key("\x03", status); !quit {
		t.Fatal("Ctrl-C did not quit")
	}
}

func TestInputFragmentationAndBounds(t *testing.T) {
	var p inputParser
	var keys, replies []string
	key := func(s string) { keys = append(keys, s) }
	reply := func(s string) { replies = append(replies, s) }
	now := time.Now()
	for _, b := range []byte(" \x1b[D\x1bPnative-video id done\x1b\\q") {
		p.feed([]byte{b}, now, key, reply)
	}
	if !reflect.DeepEqual(keys, []string{" ", "left", "q"}) || !reflect.DeepEqual(replies, []string{"native-video id done"}) {
		t.Fatal(keys, replies)
	}
	p.feed([]byte{27}, now, key, reply)
	p.expire(now.Add(200*time.Millisecond), key)
	if keys[len(keys)-1] != "escape" {
		t.Fatal(keys)
	}
	p.feed(make([]byte, 40000), now, key, reply)
	if len(p.pending) != 0 {
		t.Fatal("input buffer exceeded its bound")
	}
}

type fakeScanout struct {
	buffers          [2][]uint32
	shadow           []uint32
	front, flips     int
	events           []string
	flipErr          error
	backErr          error
	started, release chan struct{}
}

func newFakeScanout() *fakeScanout {
	d := &fakeScanout{shadow: make([]uint32, 336*180+16)}
	for i := range d.buffers {
		d.buffers[i] = make([]uint32, 336*180+16)
		for j := range d.buffers[i] {
			d.buffers[i][j] = 0xdeadbeef
		}
	}
	return d
}
func (d *fakeScanout) back() ([]uint32, int, int, int, error) {
	return d.shadow[:336*180], 320, 180, 336, d.backErr
}
func (d *fakeScanout) flip() error {
	if d.started != nil {
		close(d.started)
		<-d.release
	}
	if d.flipErr != nil {
		return d.flipErr
	}
	for y := range 180 {
		copy(d.buffers[1-d.front][y*336:y*336+320], d.shadow[y*336:y*336+320])
	}
	d.flips++
	d.front = 1 - d.front
	d.events = append(d.events, "flip")
	return nil
}
func (d *fakeScanout) suspend() error { d.events = append(d.events, "suspend"); return nil }
func (d *fakeScanout) resume() error  { d.events = append(d.events, "resume"); return nil }
func (d *fakeScanout) close() error   { d.events = append(d.events, "close"); return nil }

func TestShadowBufferOwnershipAndVTSerialization(t *testing.T) {
	native := newFakeScanout()
	native.started, native.release = make(chan struct{}), make(chan struct{})
	d := &display{device: native, active: true}
	painted := make(chan error, 1)
	go func() {
		painted <- d.Present(func(p []uint32, w, h, stride int) error {
			if &p[0] != &native.shadow[topHeight*336] || w != 320 || h != 120 || stride != 336 {
				return errors.New("renderer did not receive the cacheable RAM buffer")
			}
			for y := range h {
				for x := range w {
					p[y*stride+x] = 0xff123456
				}
			}
			return nil
		}, api.VideoStatus{State: "playing", Speed: 1, DurationMS: 2000, PositionMS: 1000})
	}()
	<-native.started
	for _, pixels := range native.buffers {
		for _, pixel := range pixels {
			if pixel != 0xdeadbeef {
				t.Fatal("rendering touched scanout before upload")
			}
		}
	}
	suspended := make(chan error, 1)
	go func() { suspended <- d.suspend() }()
	close(native.release)
	if err := <-painted; err != nil {
		t.Fatal(err)
	}
	if err := <-suspended; err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(native.events, []string{"flip", "suspend"}) {
		t.Fatal("released while page flip was in flight", native.events)
	}
	if err := d.Present(func([]uint32, int, int, int) error { return errors.New("painted inactive VT") }, api.VideoStatus{}); err != nil {
		t.Fatal(err)
	}
	for y := range 180 {
		for x := 320; x < 336; x++ {
			if native.buffers[1][y*336+x] != 0xdeadbeef {
				t.Fatal("OSD/renderer overwrote pitch padding")
			}
		}
	}
	if native.buffers[1][50*336+100] != 0xff123456 || native.buffers[0][0] != 0xdeadbeef {
		t.Fatal("wrong buffer or picture overwritten")
	}
	if err := d.resume(); err != nil {
		t.Fatal(err)
	}
	if err := d.close(); err != nil {
		t.Fatal(err)
	}
	if err := d.close(); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(native.events, []string{"flip", "suspend", "resume", "close"}) {
		t.Fatal(native.events)
	}
}

func TestFailedRenderingDoesNotSubmitFrame(t *testing.T) {
	native := newFakeScanout()
	d := &display{device: native, active: true}
	failure := errors.New("scaler failed")
	if err := d.Present(func([]uint32, int, int, int) error { return failure }, api.VideoStatus{}); !errors.Is(err, failure) {
		t.Fatal(err)
	}
	if native.flips != 0 {
		t.Fatal("submitted incomplete frame")
	}
	native.backErr = failure
	if err := d.Present(func([]uint32, int, int, int) error {
		t.Fatal("rendered after buffer acquisition failed")
		return nil
	}, api.VideoStatus{}); !errors.Is(err, failure) {
		t.Fatal("lost buffer acquisition error", err)
	}
	native.backErr = nil
	native.flipErr = failure
	if err := d.Present(func([]uint32, int, int, int) error { return nil }, api.VideoStatus{}); !errors.Is(err, failure) {
		t.Fatal("lost device error", err)
	}
}
