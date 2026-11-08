package main

import (
	"image"
	"testing"
	"time"

	"st-go/limits"
	"st-go/term"
)

func TestPreviewPainterIndependentPacedAndCoalesced(t *testing.T) {
	trm := regionTestTerminal(t)
	splitRegionTest(t, trm)
	s := &trm.displayRegions
	s.wake = make(chan struct{}, 1)
	s.jobs = make(chan regionJob, 1) // isolate pacing from native decoding
	s.secondary.request = &term.RegionImage{Continuous: true}
	s.secondary.viewWidth = s.viewport.Dx()
	s.secondary.bitmap = regionBitmap{width: s.viewport.Dx(), height: 400, contentHeight: 400,
		pixels: make([]uint32, s.viewport.Dx()*400)}
	for i := range s.secondary.bitmap.pixels {
		s.secondary.bitmap.pixels[i] = 0xff000000 | uint32(i/s.viewport.Dx())
	}
	type publication struct {
		at    time.Time
		y     float64
		pixel uint32
	}
	frames := make(chan publication, 8)
	release, done, stopped := make(chan struct{}), make(chan struct{}), make(chan struct{})
	go func() {
		defer close(stopped)
		trm.regionPainter(s.wake, done, func(frame regionFrame, pixels []uint32) {
			frames <- publication{time.Now(), frame.surface.scrollY, pixels[3]}
			select {
			case <-release:
			case <-done:
			}
		})
	}()
	defer func() { close(done); <-stopped }()
	await := func() publication {
		t.Helper()
		select {
		case frame := <-frames:
			return frame
		case <-time.After(2 * time.Second):
			t.Fatal("preview painter waited for terminal/presentation lock")
			return publication{}
		}
	}
	// Even a held terminal mutex must not prevent preview frame preparation.
	trm.mu.Lock()
	defer trm.mu.Unlock()
	s.mu.Lock()
	s.secondary.dirty = true
	trm.requestRegionPaint()
	s.mu.Unlock()
	first := await()
	// Simulate a blocked X upload. Navigation must still acquire the preview
	// state lock, and a burst must collapse to the latest requested position.
	for i := 1; i <= 100; i++ {
		if err := trm.NavigateRegion(term.SecondaryRegion, term.RegionNavigation{Kind: "position", Value: i * 1000}); err != nil {
			t.Fatal(err)
		}
	}
	close(release)
	second := await()
	if second.at.Sub(first.at) < limits.PreviewFrameInterval-time.Millisecond {
		t.Fatalf("frames were not paced: %v", second.at.Sub(first.at))
	}
	want := float64(400-s.viewport.Dy()) / 10
	if second.y != want || second.pixel != 0xff000000|uint32(want) {
		t.Fatalf("burst did not publish latest view: y=%g pixel=%#x, want y=%g", second.y, second.pixel, want)
	}
}

func TestPreviewCachePressureDoesNotChurn(t *testing.T) {
	for _, size := range [][3]int{{1920, 1080, 200}, {2560, 1440, 100}, {3840, 2130, 100}, {3840, 2130, 200}} {
		w, h, zoom := size[0], size[1], size[2]
		width := w * zoom / 100
		rows := min(limits.PreviewScreens*h, limits.PreviewCacheBytes/4/width)
		surface := &regionSurface{request: &term.RegionImage{Continuous: true, Path: "fixture.pdf"}, zoom: zoom, viewWidth: width, scrollY: 10000, targetY: 10000,
			bitmap: regionBitmap{width: width, height: rows, top: 10000 - (rows-h)/2, contentHeight: 100000}}
		trm := &Terminal{}
		trm.displayRegions = displayRegionState{secondary: surface, viewport: image.Rect(0, 0, w, h), jobs: make(chan regionJob, 1), cellHeight: 30}
		for i := 0; i < 12; i++ {
			trm.scrollRegionBy(1)
			for f := 0; f < 30 && surface.scrollY != surface.targetY; f++ {
				trm.tickRegionScroll(surface.lastScroll.Add(limits.PreviewFrameInterval))
			}
		}
		if len(trm.displayRegions.jobs) != 0 {
			t.Fatalf("%dx%d zoom=%d: small scrolls triggered decoding", w, h, zoom)
		}
		// Force a cache crossing, then simulate recentering. A completed cache
		// fill must have enough hysteresis not to immediately request another.
		surface.scrollY = float64(surface.bitmap.top + rows - h - 1)
		surface.targetY = surface.scrollY
		trm.queueRegionScroll()
		select {
		case job := <-trm.displayRegions.jobs:
			surface.bitmap.top = job.position - (rows-h)/2
			surface.loading = false
			trm.queueRegionScroll()
			if len(trm.displayRegions.jobs) != 0 {
				t.Fatal("fresh cache immediately requeued")
			}
		default:
			t.Fatal("cache edge did not prefetch")
		}
	}
}

func TestContinuousPDFOverlapReuse(t *testing.T) {
	p, err := openScrollPDF(continuousTestPDF(""))
	if err != nil {
		t.Fatal(err)
	}
	defer p.close()
	first, err := p.render(200, 40, 300, 0xfff7f5ef)
	if err != nil {
		t.Fatal(err)
	}
	before := p.renderedRows
	next, err := p.render(200, 40, 320, 0xfff7f5ef)
	if err != nil {
		t.Fatal(err)
	}
	if p.renderedRows-before != 20 {
		t.Fatalf("rendered %d rows for 20 new rows", p.renderedRows-before)
	}
	for y := next.top; y < first.top+first.height; y++ {
		for x := 0; x < first.width; x++ {
			if next.pixels[(y-next.top)*next.width+x] != first.pixels[(y-first.top)*first.width+x] {
				t.Fatal("overlap pixels changed")
			}
		}
	}
	before = p.renderedRows
	_, err = p.render(200, 40, 320, 0xfff7f5ef)
	if err != nil || p.renderedRows != before {
		t.Fatal("identical strip was rerendered")
	}
	changed, err := p.render(200, 40, 320, 0xffeaf2f8)
	if err != nil || changed.pixels[0] != 0xffeaf2f8 || p.renderedRows-before != uint64(changed.height) {
		t.Fatal("paper color change reused stale pixels")
	}
}

func TestPreviewScrollCopyMatchesFullFrame(t *testing.T) {
	for _, zoom := range []int{50, 100, 200} {
		pane, viewport := image.Rect(0, 0, 43, 30), image.Rect(3, 0, 43, 30)
		width := viewport.Dx() * zoom / 100
		surface := regionSurface{request: &term.RegionImage{Continuous: true}, zoom: zoom, pan: max(0, width-viewport.Dx()) / 2, scrollY: 50.4,
			bitmap: regionBitmap{width: width, height: 160, contentHeight: 160, pixels: make([]uint32, width*160)}}
		for i := range surface.bitmap.pixels {
			surface.bitmap.pixels[i] = 0xff000000 | uint32(i)
		}
		previous := regionFrame{surface: surface, pane: pane, viewport: viewport, background: 0xffffffff}
		before := make([]uint32, pane.Dx()*pane.Dy())
		renderRegionPixels(surface, viewport, pane, previous.background, before, pane.Dx())
		for _, y := range []float64{50.49, 56.7, 35.2} {
			next := previous
			next.surface.scrollY = y
			after := make([]uint32, len(before))
			renderRegionPixels(next.surface, viewport, pane, next.background, after, pane.Dx())
			dy, ok := regionScrollCopy(&previous, next)
			if !ok {
				t.Fatal("cached scroll lost pixmap reuse")
			}
			for row := max(0, -dy); row < min(pane.Dy(), pane.Dy()-dy); row++ {
				for x := 0; x < pane.Dx(); x++ {
					if before[(row+dy)*pane.Dx()+x] != after[row*pane.Dx()+x] {
						t.Fatalf("scroll copy mismatch at zoom=%d y=%g row=%d x=%d", zoom, y, row, x)
					}
				}
			}
		}
		next := previous
		next.surface.pan++
		if _, ok := regionScrollCopy(&previous, next); ok {
			t.Fatal("pan reused translated pixels")
		}
		next = previous
		next.epoch++
		if _, ok := regionScrollCopy(&previous, next); ok {
			t.Fatal("resize reused stale pixmap")
		}
	}
}
