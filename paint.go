package main

import (
	"github.com/BurntSushi/xgb/xproto"

	"st-go/term"
)

// paintReq is a request to the single actual-painting worker. flushNow
// requests a synchronous XSync after the blit; otherwise the blit is sent
// without a round-trip (paint-but-flush-later).
type paintReq struct {
	flushNow bool
}

// initPaintWorker starts the single goroutine that performs all actual X11
// painting (framebuffer -> PutImage). Virtual painting only enqueues regions
// into the term core and signals this worker. Any paint request made before
// the worker existed (e.g. by the pty reader at startup) is dropped; its
// regions stay queued in the core and are drained by the next request.
func (t *Terminal) initPaintWorker() {
	t.paintMu.Lock()
	t.paintPending = nil
	t.paintMu.Unlock()
	t.paintCh = make(chan *paintReq, 1)
	t.paintStop = make(chan struct{})
	go t.paintWorker()
}

// paintRequest asks the worker to repaint. Requests coalesce: while one is
// pending, later requests only upgrade it to flush-now.
func (t *Terminal) paintRequest(flushNow bool) {
	t.paintMu.Lock()
	if t.paintPending != nil {
		if flushNow {
			t.paintPending.flushNow = true
		}
		t.paintMu.Unlock()
		return
	}
	req := &paintReq{flushNow: flushNow}
	t.paintPending = req
	t.paintMu.Unlock()
	select {
	case t.paintCh <- req:
	default:
	}
}

func (t *Terminal) paintWorker() {
	for {
		select {
		case req := <-t.paintCh:
			t.paintMu.Lock()
			t.paintPending = nil
			t.paintMu.Unlock()
			if t.closed() {
				return
			}

			// rasterize the queued regions into the framebuffer under the lock
			t.mu.Lock()
			var region term.Region
			if t.termCore != nil {
				region = t.termCore.Draw()
			}
			var data []byte
			var px, py, w, h int
			if !region.Empty() {
				data, px, py, w, h = t.snapshotRegion(region)
			}
			t.mu.Unlock()

			if len(data) > 0 {
				t.putImageRegion(px, py, w, h, data)
				if req.flushNow {
					t.conn.Sync()
				}
			}
		case <-t.paintStop:
			return
		}
	}
}

// stopPaintWorker stops the paint worker (terminal shutdown). It is safe to
// call when the worker was never started (e.g. test terminals).
func (t *Terminal) stopPaintWorker() {
	if t.paintStop == nil {
		return
	}
	select {
	case <-t.paintStop:
	default:
		close(t.paintStop)
	}
}

// snapshotRegion copies a cell region's pixels from the framebuffer into a
// contiguous byte buffer (called under t.mu). Returns the pixel rect.
func (t *Terminal) snapshotRegion(r term.Region) (data []byte, px, py, w, h int) {
	w = (r.X2 - r.X1 + 1) * t.cw
	h = (r.Y2 - r.Y1 + 1) * t.ch
	px = t.borderpx + r.X1*t.cw
	py = t.borderpx + r.Y1*t.ch
	if px < 0 {
		px = 0
	}
	if py < 0 {
		py = 0
	}
	if px+w > fbW {
		w = fbW - px
	}
	if py+h > fbH {
		h = fbH - py
	}
	if w < 1 || h < 1 || fbW < 1 || fbH < 1 {
		return nil, 0, 0, 0, 0
	}
	rows := make([]uint32, 0, w*h)
	for yy := 0; yy < h; yy++ {
		start := (py+yy)*fbW + px
		rows = append(rows, framebuf[start:start+w]...)
	}
	return uint32ToBytes(rows), px, py, w, h
}

// putImageRegion sends the region's pixels to the X server in chunks sized to
// the max request length. Runs outside the terminal lock.
func (t *Terminal) putImageRegion(px, py, w, h int, data []byte) {
	if w < 1 || h < 1 || len(data) == 0 {
		return
	}
	rowsPer := (262144 - 28) / (w * 4)
	if rowsPer < 1 {
		rowsPer = 1
	}
	if rowsPer > h {
		rowsPer = h
	}
	ypos := 0
	for start := 0; start < len(data); start += rowsPer * w * 4 {
		end := start + rowsPer*w*4
		if end > len(data) {
			end = len(data)
		}
		xproto.PutImage(t.conn, xproto.ImageFormatZPixmap,
			xproto.Drawable(t.win), t.gc,
			uint16(w), uint16((end-start)/(w*4)), int16(px), int16(py+ypos), 0, 24,
			data[start:end])
		ypos += (end - start) / (w * 4)
	}
}
