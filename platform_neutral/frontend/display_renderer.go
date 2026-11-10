package frontend

import (
	"image"
	"math"
	"sync"
	"time"

	"st-go/limits"
	"st-go/platform_neutral/api"
)

// The child drawable isolates preview uploads from terminal painting. It uses
// the same X connection/event loop, but has its own GC, pixels and presentation
// lock. Only layout changes and shutdown wait for an upload; key/PTY handling
// and terminal paints never take this lock.
type regionPresenter struct {
	mu     sync.Mutex
	native api.Surface
	epoch  uint64
	last   *regionFrame
}

type regionFrame struct {
	surface        regionSurface // immutable bitmap reference and navigation snapshot
	pane, viewport image.Rectangle
	background     uint32
	epoch          uint64
	presentation   uint64
}

// Called with Terminal.mu, after releasing the preview state mutex.
func (t *Terminal) configureRegionPresenter() {
	s := &t.displayRegions
	p := &s.presenter
	p.mu.Lock()
	defer p.mu.Unlock()
	s.mu.Lock()
	defer s.mu.Unlock()
	p.epoch = s.layoutEpoch
	if t.host == nil {
		return // headless tests explicitly composite the preview
	}
	if s.secondary == nil || s.pane.Empty() {
		p.destroy()
		t.requestRegionPaint()
		return
	}
	if p.native == nil {
		surface, err := t.host.NewSurface(s.pane, s.background)
		if err != nil {
			limits.Report("preview window", "%v", err)
			return
		}
		p.native = surface
	} else {
		p.native.Configure(s.pane, s.background)
	}
	p.last = nil
	if s.wake == nil {
		s.wake = make(chan struct{}, 1)
		if t.done == nil {
			t.done = make(chan struct{})
		}
		go t.regionPainter(s.wake, t.done, p.present)
	}
	s.secondary.dirty = true
	t.requestRegionPaint()
}

func (p *regionPresenter) destroy() {
	p.last = nil
	if p.native != nil {
		p.native.Close()
		p.native = nil
	}
}

func (t *Terminal) closeRegionPresenter() {
	p := &t.displayRegions.presenter
	p.mu.Lock()
	p.epoch++
	p.destroy()
	p.mu.Unlock()
}

func (t *Terminal) exposeRegion(surface api.Surface) {
	p := &t.displayRegions.presenter
	p.mu.Lock()
	if p.native == surface && p.native != nil && p.last != nil && p.native.Expose() {
		// Repaint the last committed surface even during synchronized output.
		p.mu.Unlock()
		return
	}
	p.mu.Unlock()
	t.displayRegions.mu.Lock()
	if s := t.displayRegions.secondary; s != nil {
		s.dirty = true
		t.requestRegionPaint()
	}
	t.displayRegions.mu.Unlock()
}

func (p *regionPresenter) present(frame regionFrame, pixels []uint32) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.native == nil || frame.epoch != p.epoch {
		return // resized, reset or closed while preparing this frame
	}
	w, h := frame.pane.Dx(), frame.pane.Dy()
	dy, reusable := regionScrollCopy(p.last, frame)
	if native, ok := p.native.(api.GuardedSurface); ok {
		if !native.PresentIfCurrent(frame.presentation, pixels, w, h, dy, reusable) {
			return
		}
	} else {
		p.native.Present(pixels, w, h, dy, reusable)
	}
	p.last = &frame
}

// A backing pixmap is the scroll source, so obscured/unmapped window pixels
// cannot corrupt the retained view. Only newly exposed rows cross the X socket.
func regionScrollCopy(previous *regionFrame, next regionFrame) (int, bool) {
	if previous == nil || previous.epoch != next.epoch || previous.presentation != next.presentation || previous.pane != next.pane || previous.viewport != next.viewport || previous.background != next.background {
		return 0, false
	}
	a, b := previous.surface, next.surface
	if a.request == nil || b.request == nil || !a.scrollable() || !b.scrollable() ||
		a.search != b.search ||
		a.pan != b.pan || a.zoomPercent() != b.zoomPercent() ||
		len(a.bitmap.pixels) == 0 || len(b.bitmap.pixels) == 0 || &a.bitmap.pixels[0] != &b.bitmap.pixels[0] ||
		a.bitmap.width != b.renderWidth(next.viewport.Dx(), next.viewport.Dy()) {
		return 0, false
	}
	return int(math.Round(b.scrollY)) - int(math.Round(a.scrollY)), true
}

// No terminal lock, main framebuffer, atlas or native PDF calls in this loop.
// Rendering is paced even during bursts of input and decode completions.
func (t *Terminal) regionPainter(wake <-chan struct{}, done <-chan struct{}, present func(regionFrame, []uint32)) {
	timer := time.NewTimer(0)
	defer timer.Stop()
	var last time.Time
	var pixels []uint32
	for {
		select {
		case <-done:
			return
		case <-wake:
		case <-timer.C:
		}
		now := time.Now()
		if wait := limits.PreviewFrameInterval - now.Sub(last); wait > 0 {
			timer.Reset(wait)
			continue
		}
		s := &t.displayRegions
		s.mu.Lock()
		if !s.paused {
			t.tickRegionScroll(now)
		}
		frame, ok := t.regionFrame()
		inactive := s.secondary == nil || s.pane.Empty()
		s.mu.Unlock()
		if !ok {
			if inactive {
				pixels = nil
			}
			continue
		}
		last = now
		size, valid := limits.RGBA("preview frame", frame.pane.Dx(), frame.pane.Dy())
		if !valid {
			continue
		}
		if cap(pixels) < size/4 {
			pixels = make([]uint32, size/4)
		} else {
			pixels = pixels[:size/4]
		}
		r := frame.viewport.Sub(frame.pane.Min)
		pane := image.Rect(0, 0, frame.pane.Dx(), frame.pane.Dy())
		renderRegionPixels(frame.surface, r, pane, frame.background, pixels, pane.Dx())
		present(frame, pixels)
		t.noteRegionListFrame(frame)
		if frame.surface.scrollY != frame.surface.targetY {
			timer.Reset(max(0, limits.PreviewFrameInterval-time.Since(last)))
		}
	}
}

// Caller holds the preview mutex.
func (t *Terminal) regionFrame() (regionFrame, bool) {
	s := &t.displayRegions
	var generation uint64
	if host, ok := t.host.(api.WidgetPresentation); ok {
		var visible bool
		generation, visible = host.PresentationState()
		if !visible {
			return regionFrame{}, false
		}
	}
	if s.secondary == nil || !s.secondary.dirty || s.paused || s.pane.Empty() || t.closed() {
		return regionFrame{}, false
	}
	s.secondary.dirty = false
	surface := *s.secondary
	if surface.viewerPrevious != nil {
		surface = *surface.viewerPrevious
	}
	return regionFrame{surface: surface, pane: s.pane, viewport: s.viewport, background: s.background, epoch: s.layoutEpoch, presentation: generation}, true
}

// Called with Terminal.mu at PTY batch boundaries. Region hooks also sample
// this state before mutation, so a synchronization marker and preview command
// in the same PTY read cannot leak partial updates. Ordinary terminal output
// never pauses the preview worker.
func (t *Terminal) pauseRegionPaint(paused bool) {
	s := &t.displayRegions
	s.mu.Lock()
	s.paused = paused
	if !paused {
		t.requestRegionPaint()
	}
	s.mu.Unlock()
}

func (t *Terminal) queueRegionReply(reply regionReply) {
	s := &t.displayRegions
	if s.replies == nil {
		s.replies = make(chan regionReply, 1)
	}
	select {
	case <-s.replies:
	default:
	}
	s.replies <- reply
}

// Native workers publish bounded replies; only the terminal event loop writes
// application input. This keeps the lock order one-way.
func (t *Terminal) flushRegionReplies() {
	t.flushPDFSearchReply()
	s := &t.displayRegions
	s.mu.Lock()
	var reply regionReply
	select {
	case reply = <-s.replies:
	default:
	}
	current := reply.text != "" && reply.generation == s.generation && s.secondary != nil
	var metadata regionMetadataReply
	select {
	case metadata = <-s.metadataReplies:
	default:
	}
	metadataCurrent := metadata.text != "" && metadata.revision == s.documentRevision && s.secondary != nil
	s.mu.Unlock()
	if current && t.termCore != nil {
		t.termCore.WriteToTTY([]byte(reply.text), false)
	}
	if metadataCurrent && t.termCore != nil {
		t.termCore.WriteToTTY([]byte(metadata.text), false)
	}
}
