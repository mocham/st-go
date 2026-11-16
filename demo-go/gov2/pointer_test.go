package main

import (
	"image"
	"slices"
	"testing"
	"time"

	"st-go/platform_neutral/api"
)

func TestStatusPointerControlsAndDrag(t *testing.T) {
	for _, w := range []int{320, 1920} {
		s := api.VideoStatus{DurationMS: 60000}
		track, buttons := statusLayout(w, 180)
		var got []string
		key := func(k string) { got = append(got, k) }
		for _, b := range buttons {
			c := pointerControls{position: b.rect.Min.Add(image.Pt(2, 2))}
			c.event(pointerEvent{button: 1, down: true}, w, 180, s, key)
			if c.pressed != b.key {
				t.Fatal("button hit mismatch", b)
			}
			before := len(got)
			c.event(pointerEvent{button: 1}, w, 180, s, key)
			if len(got) != before+1 || got[before] != b.key {
				t.Fatal("click did not activate control", got)
			}
			c.event(pointerEvent{button: 1, down: true}, w, 180, s, key)
			c.event(pointerEvent{dy: -100}, w, 180, s, key)
			c.event(pointerEvent{button: 1}, w, 180, s, key)
			if len(got) != before+1 {
				t.Fatal("release outside button activated it")
			}
		}
		c := pointerControls{position: track.Min.Add(image.Pt(0, 5))}
		c.event(pointerEvent{button: 1, down: true}, w, 180, s, key)
		c.event(pointerEvent{dx: w / 2}, w, 180, s, key)
		if c.pendingSeek == nil || c.pendingSeek.PositionMS < 29000 || c.pendingSeek.PositionMS > 32000 {
			t.Fatal("drag did not seek to middle", c.pendingSeek)
		}
		c.event(pointerEvent{dx: w, dy: -100}, w, 180, s, key)
		c.event(pointerEvent{button: 1}, w, 180, s, key)
		if c.dragging || c.pendingSeek.PositionMS != 60000 {
			t.Fatal("drag did not clamp on release", c)
		}
		c.event(pointerEvent{cancel: true}, w, 180, s, key)
		if c.pendingSeek != nil || c.visible {
			t.Fatal("device/VT loss retained a seek or cursor")
		}
		c.position = buttons[3].rect.Min.Add(image.Pt(2, 2))
		c.event(pointerEvent{wheel: -1}, w, 180, s, key)
		if got[len(got)-1] != "[" {
			t.Fatal("speed scroll", got)
		}
		c.position = buttons[6].rect.Min.Add(image.Pt(2, 2))
		c.event(pointerEvent{wheel: 1}, w, 180, s, key)
		if got[len(got)-1] != "+" {
			t.Fatal("volume scroll", got)
		}
	}
}

func TestPausedCursorRefreshRestoresPixels(t *testing.T) {
	native := newFakeScanout()
	d := &display{device: native, active: true}
	s := api.VideoStatus{State: "paused", DurationMS: 60000, Speed: 1}
	renders := 0
	if err := d.Present(func(p []uint32, w, h, stride int) error {
		renders++
		for y := range h {
			for x := range w {
				p[y*stride+x] = 0xff123456
			}
		}
		return nil
	}, s); err != nil {
		t.Fatal(err)
	}
	before := slices.Clone(native.shadow)
	for _, at := range []image.Point{image.Pt(100, 80), image.Pt(319, 179), image.Pt(4, 140)} {
		d.movePointer(at, true)
		d.lastFlip = time.Time{}
		if err := d.refreshUI(s); err != nil {
			t.Fatal(err)
		}
		if slices.Equal(before, native.shadow) {
			t.Fatal("cursor did not draw")
		}
	}
	d.movePointer(image.Point{}, false)
	d.lastFlip = time.Time{}
	if err := d.refreshUI(s); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(before, native.shadow) {
		t.Fatal("cursor left a trail or changed padding")
	}
	if renders != 1 {
		t.Fatal("pointer re-decoded/scaled the paused picture")
	}
	if err := d.suspend(); err != nil {
		t.Fatal(err)
	}
	d.movePointer(image.Pt(80, 80), true)
	d.lastFlip = time.Time{}
	flips := native.flips
	if err := d.refreshUI(s); err != nil || native.flips != flips {
		t.Fatal("pointer painted inactive VT", err)
	}
}

func TestPointerCommandCoalescing(t *testing.T) {
	c := controls{}
	for i := range 1000 {
		c.queue(api.VideoRequest{Action: "seek", PositionMS: int64(i)})
		c.queue(api.VideoRequest{Action: "volume", Volume: i % 101})
	}
	if len(c.pending) != 2 || c.pending[0].PositionMS != 999 {
		t.Fatal("drag/scroll backlog", c.pending)
	}
	c.queue(api.VideoRequest{Action: "toggle"})
	c.queue(api.VideoRequest{Action: "seek", PositionMS: 42})
	if len(c.pending) != 4 {
		t.Fatal("coalesced across pause transition", c.pending)
	}
}
