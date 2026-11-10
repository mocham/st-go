//go:build linux

package fb

import (
	"bytes"
	"encoding/binary"
	"image"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"
	"unsafe"

	"st-go/platform_neutral/api"
)

func TestPaintRequestsOnlyRetainAndInvalidateUntilFlush(t *testing.T) {
	d := memoryDriver(t, 80, 80)
	before := bytes.Clone(d.screen.memory)
	pixels := make([]uint32, 80*80)
	for i := range pixels {
		pixels[i] = 0xff224466
	}
	d.Upload(pixels, 80, 0, 0, 80, 80)
	view, err := d.NewSurface(image.Rect(20, 20, 60, 60), 0xff557733)
	if err != nil {
		t.Fatal(err)
	}
	preview := make([]uint32, 40*40)
	for i := range preview {
		preview[i] = 0xffeeeeee
	}
	view.Present(preview, 40, 40, 0, false)
	d.SetTextCursor(api.TextCursor{Bounds: image.Rect(5, 5, 6, 6), Pixels: []uint32{0xffffffff}, Base: []uint32{0xff224466}})
	d.movePointer(image.Pt(70, 70), true)
	if !bytes.Equal(before, d.screen.memory) {
		t.Fatal("paint request wrote visible scanout before presentation")
	}
	if len(d.damage) != 1 || d.damage[0] != image.Rect(0, 0, 80, 80) {
		t.Fatal("damage did not coalesce", d.damage)
	}
	copies := 0
	d.screen.present = func(frame []byte, damage []image.Rectangle) {
		copies++
		if !bytes.Equal(before, d.screen.memory) {
			t.Fatal("intermediate layer was exposed before final-frame copy")
		}
		value := func(x, y int) uint32 { return binary.NativeEndian.Uint32(frame[(y*80+x)*4:]) }
		if value(0, 0) != 0x224466 || value(30, 30) != 0xeeeeee || value(5, 5) != 0xffffff || value(70, 70) != 0 {
			t.Fatal("presented frame was not completely composited")
		}
		d.screen.copyFrame(frame, damage)
	}
	d.flushPending()
	if copies != 1 || len(d.damage) != 0 || !slices.Equal(pixels, d.pixels) {
		t.Fatal("flush failed to commit one batch or contaminated retained content")
	}
	d.flushPending()
	if copies != 1 {
		t.Fatal("clean frame was presented again")
	}
}

func TestPreviewBurstPresentsNewestStateOnceWithoutFrameQueue(t *testing.T) {
	d := memoryDriver(t, 80, 40)
	view, err := d.NewSurface(image.Rect(0, 0, 80, 40), 0xff000000)
	if err != nil {
		t.Fatal(err)
	}
	pixels := make([]uint32, 80*40)
	for frame := uint32(1); frame <= 500; frame++ {
		for i := range pixels {
			pixels[i] = 0xff000000 | frame
		}
		view.Present(pixels, 80, 40, 1, true)
	}
	if len(d.damage) != 1 {
		t.Fatal("preview burst accumulated a queue of obsolete frames", len(d.damage))
	}
	d.flushPending()
	if got := binary.NativeEndian.Uint32(d.screen.memory); got != 500 {
		t.Fatal("presenter displayed an obsolete preview", got)
	}
}

func TestPaintCommitKeepsContentAndCaretAtomic(t *testing.T) {
	d := memoryDriver(t, 40, 40)
	before := bytes.Clone(d.screen.memory)
	d.BeginPaint()
	d.BeginPaint()
	pixels := make([]uint32, 1600)
	for i := range pixels {
		pixels[i] = 0xff224466
	}
	d.Upload(pixels, 40, 0, 0, 40, 40)
	d.flushPending()
	d.SetTextCursor(api.TextCursor{Bounds: image.Rect(2, 2, 3, 3), Pixels: []uint32{0xffffffff}, Base: []uint32{0xff224466}})
	d.EndPaint()
	d.flushPending()
	if !bytes.Equal(before, d.screen.memory) || len(d.damage) == 0 {
		t.Fatal("uncommitted/partially committed paint reached scanout")
	}
	d.EndPaint()
	d.flushPending()
	if binary.NativeEndian.Uint32(d.screen.memory) != 0x224466 || binary.NativeEndian.Uint32(d.screen.memory[(2*40+2)*4:]) != 0xffffff {
		t.Fatal("content and caret were not committed together")
	}
}

func TestCloseDrainsInFlightPresentationBeforeConsoleCleanup(t *testing.T) {
	d := memoryDriver(t, 40, 40)
	entered, allow := make(chan struct{}), make(chan struct{})
	var once sync.Once
	t.Cleanup(func() { once.Do(func() { close(allow) }) })
	var restored atomic.Int32
	d.console = &console{fd: -1, changedKD: true, ops: consoleOps{pointer: func(uintptr, unsafe.Pointer) error { return nil }, value: func(uintptr, uintptr) error { restored.Add(1); return nil }}}
	d.screen.present = func(frame []byte, damage []image.Rectangle) {
		close(entered)
		<-allow
		d.screen.copyFrame(frame, damage)
	}
	d.Upload(make([]uint32, 1600), 40, 0, 0, 40, 40)
	d.startPresenter()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("presenter did not start")
	}
	closed := make(chan struct{})
	go func() { d.Close(); close(closed) }()
	select {
	case <-d.done:
	case <-time.After(time.Second):
		t.Fatal("close did not signal the presenter")
	}
	select {
	case <-closed:
		t.Fatal("close restored/unmapped while a frame was in flight")
	case <-time.After(20 * time.Millisecond):
	}
	if restored.Load() != 0 {
		t.Fatal("console cleanup ran before hardware copy drained")
	}
	once.Do(func() { close(allow) })
	select {
	case <-closed:
	case <-time.After(time.Second):
		t.Fatal("close failed to drain presentation")
	}
	select {
	case <-d.presentDone:
	case <-time.After(time.Second):
		t.Fatal("presentation worker survived close")
	}
	if restored.Load() != 1 {
		t.Fatal("console was not restored after draining presentation")
	}
}

func TestDirtyBatchDoesNotLoseUpdatesAndVTReleaseWaitsForTransport(t *testing.T) {
	d := memoryDriver(t, 40, 40)
	pixels := make([]uint32, 1600)
	for i := range pixels {
		pixels[i] = 0xff112233
	}
	d.Upload(pixels, 40, 0, 0, 40, 40)
	entered, allow := make(chan struct{}), make(chan struct{})
	var once sync.Once
	t.Cleanup(func() { once.Do(func() { close(allow) }) })
	var calls atomic.Int32
	d.screen.present = func(frame []byte, damage []image.Rectangle) {
		if calls.Add(1) == 1 {
			close(entered)
			<-allow
		}
		d.screen.copyFrame(frame, damage)
	}
	flushed := make(chan struct{})
	go func() { defer close(flushed); d.flushPending() }()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("presenter did not reach transport")
	}
	updated := make(chan struct{})
	go func() {
		for i := range pixels {
			pixels[i] = 0xffaabbcc
		}
		d.Upload(pixels, 40, 0, 0, 40, 40)
		close(updated)
	}()
	select {
	case <-updated:
	case <-time.After(time.Second):
		t.Fatal("slow hardware copy held the producer lock")
	}
	suspended := make(chan struct{})
	go func() { d.suspendPresentation(); close(suspended) }()
	select {
	case <-suspended:
		t.Fatal("VT was released while a scanout copy was in flight")
	case <-time.After(20 * time.Millisecond):
	}
	once.Do(func() { close(allow) })
	select {
	case <-flushed:
	case <-time.After(time.Second):
		t.Fatal("transport did not complete")
	}
	select {
	case <-suspended:
	case <-time.After(time.Second):
		t.Fatal("VT release failed to drain transport")
	}
	if len(d.damage) == 0 || binary.NativeEndian.Uint32(d.screen.memory) != 0x112233 {
		t.Fatal("in-flight frame was mutated or completion cleared newer damage")
	}
	d.flushPending()
	if calls.Load() != 1 {
		t.Fatal("presented into an inactive VT")
	}
	d.mu.Lock()
	d.active = true
	d.invalidate(image.Rect(0, 0, 40, 40))
	d.mu.Unlock()
	d.flushPending()
	if calls.Load() != 2 || binary.NativeEndian.Uint32(d.screen.memory) != 0xaabbcc {
		t.Fatal("latest pending state was not restored on VT acquire")
	}
}

func TestPresentationClockCoalescesAndSleepsWhenClean(t *testing.T) {
	now := time.Now()
	for _, elapsed := range []time.Duration{0, presentFrameInterval / 2, presentFrameInterval, 2*presentFrameInterval + time.Millisecond} {
		want := presentFrameInterval - elapsed%presentFrameInterval
		if got := presentDelay(now, now.Add(elapsed)); got != want || got <= 0 {
			t.Fatal("presentation did not target next frame tick", elapsed, got, want)
		}
	}
	if presentDelay(time.Time{}, now) != presentFrameInterval {
		t.Fatal("first paint bypassed the coalescing window")
	}
	d := memoryDriver(t, 40, 40)
	presented := make(chan struct{}, 8)
	d.screen.present = func(frame []byte, damage []image.Rectangle) {
		d.screen.copyFrame(frame, damage)
		presented <- struct{}{}
	}
	d.Upload(make([]uint32, 1600), 40, 0, 0, 40, 40)
	d.startPresenter()
	t.Cleanup(func() {
		d.Close()
		select {
		case <-d.presentDone:
		case <-time.After(time.Second):
			t.Error("presentation worker did not exit")
		}
	})
	select {
	case <-presented:
	case <-time.After(time.Second):
		t.Fatal("independent presenter did not flush pending damage")
	}
	select {
	case <-presented:
		t.Fatal("clean output kept repainting")
	case <-time.After(2 * presentFrameInterval):
	}
	d.movePointer(image.Pt(10, 10), true)
	select {
	case <-presented:
	case <-time.After(time.Second):
		t.Fatal("overlay-only damage did not wake presenter")
	}
}

func TestDamageStorageIsBoundedAndSeparateRowsStaySeparate(t *testing.T) {
	d := memoryDriver(t, 300, 3)
	d.mu.Lock()
	defer d.mu.Unlock()
	d.invalidate(image.Rect(0, 0, 300, 1))
	d.invalidate(image.Rect(0, 2, 300, 3))
	if len(d.damage) != 2 {
		t.Fatal("damage bridged clean row", d.damage)
	}
	d.damage = nil
	for i := 0; i <= maxDamageRectangles; i++ {
		d.invalidate(image.Rect(i*2, 1, i*2+1, 2))
	}
	if len(d.damage) != 1 || d.damage[0] != image.Rect(0, 0, 300, 3) {
		t.Fatal("fragmented damage was not bounded", d.damage)
	}
}

func BenchmarkPDFScrollPresentation(b *testing.B) {
	const w, h = 1280, 720
	f, v := info(w, h)
	l, err := screenLayout(f, v)
	if err != nil {
		b.Fatal(err)
	}
	d := &Driver{screen: &framebuffer{fd: -1, layout: l, memory: make([]byte, l.length)}, pixels: make([]uint32, w*h), active: true, done: make(chan struct{})}
	b.Cleanup(d.Close)
	view, err := d.NewSurface(image.Rect(0, 20, w, h), 0xffeeeeee)
	if err != nil {
		b.Fatal(err)
	}
	pixels := make([]uint32, w*(h-20))
	for i := range pixels {
		pixels[i] = 0xffeeeeee
	}
	d.flushPending()
	b.SetBytes(int64(len(pixels) * 4))
	b.ReportAllocs()
	for b.Loop() {
		view.Present(pixels, w, h-20, 1, true)
		d.flushPending()
	}
}

func TestNativePresentationPreservesPaddingOffsetsAndUntouchedPixels(t *testing.T) {
	for _, depth := range []uint32{16, 24, 32} {
		f, v := info(5, 4)
		v.BitsPerPixel, v.VirtualWidth, v.VirtualHeight, v.XOffset, v.YOffset = depth, 7, 6, 1, 1
		if depth == 16 {
			v.Red, v.Green, v.Blue = bitfield{Offset: 11, Length: 5}, bitfield{Offset: 5, Length: 6}, bitfield{Length: 5}
		}
		f.LineLength, f.MemoryLength = 7*(depth/8)+8, (7*(depth/8)+8)*6
		l, err := screenLayout(f, v)
		if err != nil {
			t.Fatal(err)
		}
		screen := &framebuffer{layout: l, memory: bytes.Repeat([]byte{0xcc}, l.length)}
		frame, want := make([]byte, l.length), bytes.Clone(screen.memory)
		damage := []image.Rectangle{image.Rect(1, 1, 4, 3)}
		for y := 1; y < 3; y++ {
			for x := 1; x < 4; x++ {
				l.pixel(frame, x, y, 0xff123456)
				l.pixel(want, x, y, 0xff123456)
			}
		}
		screen.copyFrame(frame, damage)
		if !bytes.Equal(screen.memory, want) {
			t.Fatal("presentation changed native padding, virtual offsets, or clean pixels", depth)
		}
	}
}
