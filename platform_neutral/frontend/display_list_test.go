package frontend

import (
	"bytes"
	"fmt"
	"image"
	"testing"
	"time"

	"st-go/term"
)

func TestRetainedListSmoothScrollHitTestingAndPreviewRestore(t *testing.T) {
	trm := regionTestTerminal(t)
	splitRegionTest(t, trm)
	setRegionTestPixels(trm, 0xff913b57)
	original := trm.displayRegions.secondary
	original.scrollY, original.targetY = 17, 17
	generation := trm.displayRegions.generation
	list := term.RegionList{Token: 17, Title: "Table of contents"}
	for i := 1; i <= 80; i++ {
		list.Items = append(list.Items, term.RegionListItem{ID: i, Text: fmt.Sprintf("Section %d", i), Level: i % 5})
	}
	cols, rows := trm.termCore.Cols(), trm.termCore.Rows()
	if err := trm.ShowRegionList(term.SecondaryRegion, &list); err != nil {
		t.Fatal(err)
	}
	if trm.termCore.Cols() != cols || trm.termCore.Rows() != rows {
		t.Fatal("list changed the main grid")
	}
	active := trm.displayRegions.secondary
	if active == original || trm.displayRegions.listPreview != original {
		t.Fatal("preview was not retained")
	}
	// A completion from the hidden PDF must not overwrite the list.
	trm.displayRegions.mu.Lock()
	trm.acceptRegionImage(regionJob{generation: generation}, regionBitmap{}, nil)
	trm.displayRegions.mu.Unlock()
	if trm.displayRegions.secondary != active || active.list == nil {
		t.Fatal("old decoder result replaced the list")
	}
	before := append([]uint32(nil), trm.framebuf...)
	trm.paintSecondary()
	main := image.Rect(trm.borderpx, trm.borderpx, trm.borderpx+cols*trm.cw, trm.borderpx+rows*trm.ch)
	for y := main.Min.Y; y < main.Max.Y; y++ {
		for x := main.Min.X; x < main.Max.X; x++ {
			if trm.framebuf[y*trm.fbW+x] != before[y*trm.fbW+x] {
				t.Fatal("list paint touched main editor pixels")
			}
		}
	}
	r := trm.secondaryPixels()
	trm.regionMousePress(5, r.Min.X+2, r.Min.Y+2)
	if active.scrollY <= 0 || active.scrollY >= active.targetY {
		t.Fatalf("wheel did not ease: %v -> %v", active.scrollY, active.targetY)
	}
	var replies bytes.Buffer
	trm.termCore.SetWriter(func(data []byte) { replies.Write(data) })
	y := r.Min.Y + active.list.headerHeight() + active.list.rowHeight()/2
	trm.regionMousePress(1, r.Min.X+2, y)
	want := "region secondary event activate 17 1"
	if !bytes.Contains(replies.Bytes(), []byte(want)) {
		t.Fatalf("click used uncommitted/target scroll position: %q want %q", replies.String(), want)
	}
	// A fractional offset rounds to a full row in the renderer. Hit-testing
	// must use that same rounded origin at the exact top-of-body boundary.
	active.scrollY = float64(active.list.rowHeight()) - 0.4
	active.targetY, active.dirty = active.scrollY, true
	trm.paintSecondary()
	replies.Reset()
	trm.regionMousePress(1, r.Min.X+2, r.Min.Y+active.list.headerHeight())
	if !bytes.Contains(replies.Bytes(), []byte("region secondary event activate 17 2")) {
		t.Fatalf("fractional row-boundary click selected wrong item: %q", replies.String())
	}
	trm.scrollRegionBy(1)
	for i := 0; i < 30; i++ {
		trm.tickRegionScroll(time.Now().Add(time.Duration(i+1) * 50 * time.Millisecond))
	}
	if active.scrollY != active.targetY {
		t.Fatalf("scroll did not settle: %v -> %v", active.scrollY, active.targetY)
	}
	if err := trm.ShowRegionList(term.SecondaryRegion, nil); err != nil {
		t.Fatal(err)
	}
	if trm.displayRegions.secondary != original || original.scrollY != 17 || original.targetY != 17 || original.bitmap.pixels[0] != 0xff913b57 {
		t.Fatal("restore lost PDF content/scroll position")
	}
}

func TestRetainedListResizeAndClear(t *testing.T) {
	trm := regionTestTerminal(t)
	splitRegionTest(t, trm)
	list := term.RegionList{Token: 1, Title: "Table of contents", Items: []term.RegionListItem{{ID: 1, Level: 4, Text: "世界 e\u0301"}}}
	if err := trm.ShowRegionList(term.SecondaryRegion, &list); err != nil {
		t.Fatal(err)
	}
	trm.resize(162, 82)
	trm.paintSecondary()
	if trm.displayRegions.secondary.list == nil {
		t.Fatal("resize lost list")
	}
	if err := trm.ClearRegion(term.SecondaryRegion); err != nil {
		t.Fatal(err)
	}
	if trm.displayRegions.secondary.list != nil || trm.displayRegions.listPreview != nil {
		t.Fatal("clear retained old list/PDF")
	}
}

func TestRetainedListPainterUsesImmutableSnapshots(t *testing.T) {
	trm := regionTestTerminal(t)
	splitRegionTest(t, trm)
	s := &trm.displayRegions
	s.wake = make(chan struct{}, 1)
	list := term.RegionList{Token: 1, Title: "Table of contents", Items: []term.RegionListItem{{ID: 1, Text: "Original"}}}
	if err := trm.ShowRegionList(term.SecondaryRegion, &list); err != nil {
		t.Fatal(err)
	}
	frames := make(chan regionFrame, 4)
	release, done, stopped := make(chan struct{}), make(chan struct{}), make(chan struct{})
	go func() {
		defer close(stopped)
		trm.regionPainter(s.wake, done, func(frame regionFrame, pixels []uint32) {
			select {
			case frames <- frame:
			case <-done:
				return
			}
			select {
			case <-release:
			case <-done:
			}
		})
	}()
	defer func() { close(done); <-stopped }()
	await := func() regionFrame {
		t.Helper()
		select {
		case frame := <-frames:
			return frame
		case <-time.After(2 * time.Second):
			t.Fatal("list painter blocked")
			return regionFrame{}
		}
	}
	first := await()
	// Updating the application-owned slice must not mutate the old painter
	// snapshot, even while its simulated X upload is blocked.
	list.Token, list.Items[0].Text = 2, "Changed 世界"
	trm.mu.Lock()
	if err := trm.ShowRegionList(term.SecondaryRegion, &list); err != nil {
		trm.mu.Unlock()
		t.Fatal(err)
	}
	trm.mu.Unlock()
	if first.surface.list.content.Items[0].Text != "Original" {
		t.Fatal("published list changed in place")
	}
	close(release)
	next := await()
	if next.surface.list.content.Token != 2 || next.surface.list.content.Items[0].Text != "Changed 世界" {
		t.Fatal("list update was not published")
	}
}
