package main

import (
	"image"
	"math"
	"sync"
	"time"

	"github.com/BurntSushi/xgb"
	"github.com/BurntSushi/xgb/xproto"
	"st-go/limits"
)

// The child drawable isolates preview uploads from terminal painting. It uses
// the same X connection/event loop, but has its own GC, pixels and presentation
// lock. Only layout changes and shutdown wait for an upload; key/PTY handling
// and terminal paints never take this lock.
type regionPresenter struct {
	mu     sync.Mutex
	conn   *xgb.Conn
	window xproto.Window
	pixmap xproto.Pixmap
	gc     xproto.Gcontext
	epoch  uint64
	last   *regionFrame
}

type regionFrame struct {
	surface        regionSurface // immutable bitmap reference and navigation snapshot
	pane, viewport image.Rectangle
	background     uint32
	epoch          uint64
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
	if t.conn == nil {
		return // headless tests explicitly composite the preview
	}
	if s.secondary == nil || s.pane.Empty() {
		p.destroy()
		t.requestRegionPaint()
		return
	}
	if p.window == 0 {
		win, err := xproto.NewWindowId(t.conn)
		if err != nil {
			limits.Report("preview window", "%v", err)
			return
		}
		gc, err := xproto.NewGcontextId(t.conn)
		if err != nil {
			limits.Report("preview GC", "%v", err)
			return
		}
		p.conn, p.window, p.gc = t.conn, win, gc
		// Pointer events propagate to the parent with parent-relative coordinates.
		// Do not select keyboard, pointer or structure events on this child.
		xproto.CreateWindow(t.conn, 0, win, t.win, int16(s.pane.Min.X), int16(s.pane.Min.Y),
			uint16(s.pane.Dx()), uint16(s.pane.Dy()), 0, xproto.WindowClassInputOutput, 0,
			xproto.CwBackPixel|xproto.CwEventMask, []uint32{s.background & 0xffffff, xproto.EventMaskExposure})
		xproto.CreateGC(t.conn, gc, xproto.Drawable(win), xproto.GcGraphicsExposures, []uint32{0})
		xproto.MapWindow(t.conn, win)
	} else {
		xproto.ConfigureWindow(t.conn, p.window,
			xproto.ConfigWindowX|xproto.ConfigWindowY|xproto.ConfigWindowWidth|xproto.ConfigWindowHeight,
			[]uint32{uint32(s.pane.Min.X), uint32(s.pane.Min.Y), uint32(s.pane.Dx()), uint32(s.pane.Dy())})
	}
	if p.pixmap != 0 {
		xproto.FreePixmap(t.conn, p.pixmap)
		p.pixmap = 0
	}
	p.last = nil
	pid, err := xproto.NewPixmapId(t.conn)
	if err == nil {
		p.pixmap = pid
		xproto.CreatePixmap(t.conn, 24, pid, xproto.Drawable(p.window), uint16(s.pane.Dx()), uint16(s.pane.Dy()))
	} else {
		limits.Report("preview pixmap", "%v", err)
	}
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
	if p.pixmap != 0 {
		xproto.FreePixmap(p.conn, p.pixmap)
		p.pixmap = 0
	}
	p.last = nil
	if p.window != 0 {
		xproto.FreeGC(p.conn, p.gc)
		xproto.DestroyWindow(p.conn, p.window)
		p.window, p.gc = 0, 0
	}
}

func (t *Terminal) closeRegionPresenter() {
	p := &t.displayRegions.presenter
	p.mu.Lock()
	p.epoch++
	p.destroy()
	p.mu.Unlock()
}

func (t *Terminal) exposeRegion(window xproto.Window) {
	p := &t.displayRegions.presenter
	p.mu.Lock()
	if p.window == window && p.pixmap != 0 && p.last != nil {
		// Repaint the last committed surface even during synchronized output.
		r := p.last.pane
		xproto.CopyArea(p.conn, xproto.Drawable(p.pixmap), xproto.Drawable(p.window), p.gc,
			0, 0, 0, 0, uint16(r.Dx()), uint16(r.Dy()))
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
	if p.window == 0 || frame.epoch != p.epoch {
		return // resized, reset or closed while preparing this frame
	}
	w, h := frame.pane.Dx(), frame.pane.Dy()
	drawable := xproto.Drawable(p.window)
	start, end := 0, h
	if p.pixmap != 0 {
		drawable = xproto.Drawable(p.pixmap)
		if dy, reusable := regionScrollCopy(p.last, frame); reusable && dy > -h && dy < h {
			switch {
			case dy > 0:
				xproto.CopyArea(p.conn, drawable, drawable, p.gc, 0, int16(dy), 0, 0, uint16(w), uint16(h-dy))
				start = h - dy
			case dy < 0:
				xproto.CopyArea(p.conn, drawable, drawable, p.gc, 0, 0, 0, int16(-dy), uint16(w), uint16(h+dy))
				end = -dy
			default:
				end = 0
			}
		}
	}
	rows := max(1, (262144-28)/(w*4))
	for y := start; y < end; y += rows {
		n := min(rows, end-y)
		xproto.PutImage(p.conn, xproto.ImageFormatZPixmap, drawable, p.gc,
			uint16(w), uint16(n), 0, int16(y), 0, 24, uint32ToBytes(pixels[y*w:(y+n)*w]))
	}
	if p.pixmap != 0 {
		xproto.CopyArea(p.conn, drawable, xproto.Drawable(p.window), p.gc, 0, 0, 0, 0, uint16(w), uint16(h))
	}
	p.last = &frame
}

// A backing pixmap is the scroll source, so obscured/unmapped window pixels
// cannot corrupt the retained view. Only newly exposed rows cross the X socket.
func regionScrollCopy(previous *regionFrame, next regionFrame) (int, bool) {
	if previous == nil || previous.epoch != next.epoch || previous.pane != next.pane || previous.viewport != next.viewport || previous.background != next.background {
		return 0, false
	}
	a, b := previous.surface, next.surface
	if a.request == nil || b.request == nil || !a.request.Continuous || !b.request.Continuous ||
		a.search != b.search ||
		a.pan != b.pan || a.zoomPercent() != b.zoomPercent() ||
		len(a.bitmap.pixels) == 0 || len(b.bitmap.pixels) == 0 || &a.bitmap.pixels[0] != &b.bitmap.pixels[0] ||
		a.bitmap.width != b.renderWidth(next.viewport.Dx()) {
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
		if frame.surface.scrollY != frame.surface.targetY {
			timer.Reset(max(0, limits.PreviewFrameInterval-time.Since(last)))
		}
	}
}

// Caller holds the preview mutex.
func (t *Terminal) regionFrame() (regionFrame, bool) {
	s := &t.displayRegions
	if s.secondary == nil || !s.secondary.dirty || s.paused || s.pane.Empty() || t.closed() {
		return regionFrame{}, false
	}
	s.secondary.dirty = false
	return regionFrame{*s.secondary, s.pane, s.viewport, s.background, s.layoutEpoch}, true
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
