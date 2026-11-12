package stterminal

import (
	"testing"
	"time"

	"st-go/config"
)

type refreshHooks struct {
	mockHooks
	requests [][2]time.Duration
}

func (h *refreshHooks) RequestRefresh(interval, duration time.Duration) {
	h.requests = append(h.requests, [2]time.Duration{interval, duration})
}

func TestRefreshLeaseBoundsAndFragmentation(t *testing.T) {
	h := &refreshHooks{}
	core := NewTerm(config.Default(), h)
	for _, command := range []string{"refresh 16 120000", "refresh 33 2000", "refresh 0 0", "refresh -1 10", "refresh 1 100", "refresh 16 120001", "refresh 16 0", "refresh 1001 10", "refresh x 10", "refresh 16 100 extra"} {
		for _, b := range []byte("\x1bP" + command + "\x1b\\") {
			core.Twrite([]byte{b}, false)
		}
	}
	if len(h.requests) != 3 || h.requests[0] != [2]time.Duration{16 * time.Millisecond, 2 * time.Minute} || h.requests[2] != [2]time.Duration{} {
		t.Fatal("invalid lease accepted or fragmented request lost", h.requests)
	}
}

func TestAnimationDeadlinesCatchUpAndStop(t *testing.T) {
	core, hooks := newTestTerm(t, 10, 8)
	if !core.NextAnimDeadline().IsZero() {
		t.Fatal("idle animation deadline")
	}
	hooks.imageCols, hooks.imageRows = 1, 1
	hooks.animFrameGlyphs = [][]Glyph{{{U: 'A'}}, {{U: 'B'}}, {{U: 'C'}}}
	core.SetAnim([]byte("animation"), ImageDecodeOptions{}, []int{100, 200, 300}, 1, 1, 0, 0, 4, 3)
	start := core.anim.startAt
	if got := core.NextAnimDeadline(); !got.Equal(start.Add(100 * time.Millisecond)) {
		t.Fatal(got)
	}
	core.TickAnim(start.Add(400 * time.Millisecond))
	if core.anim.idx != 2 || !core.NextAnimDeadline().Equal(start.Add(600*time.Millisecond)) {
		t.Fatal("did not skip obsolete frame")
	}
	core.TickAnim(start.Add(time.Second))
	if !core.NextAnimDeadline().IsZero() {
		t.Fatal("finished animation still wakes")
	}
}
