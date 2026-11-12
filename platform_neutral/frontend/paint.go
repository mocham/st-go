package frontend

import (
	"time"

	"st-go/platform_neutral/api"
	term "st-go/platform_neutral/stterminal"
)

// attachTerm installs the synchronous frontend paint callback before PTY
// processing starts. Paint requests raised while parsing PTY output are left
// to that loop's latency/synchronized-output scheduler; every other trigger
// paints in its caller's t.mu critical section, or arms framebuffer pacing.
func (t *Terminal) attachTerm(core *term.Term) {
	if t.eventWake == nil {
		t.eventWake = make(chan struct{}, 1)
	}
	t.termCore = core
	core.SetPaintFn(func() {
		if t.inPTYWrite {
			t.ptyPaintRequested = true
			return
		}
		t.paint()
	})
	// Mapping/physical sizing can precede core attachment. A later unchanged
	// ConfigureNotify must not leave the new core at the stale configured grid.
	if t.cols > 0 && t.rows > 0 && (core.Cols() != t.cols || core.Rows() != t.rows) {
		core.Tresize(t.cols, t.rows)
	}
}

func (t *Terminal) writeTerminalOutput(core *term.Term, data []byte) int {
	t.mu.Lock()
	if t.closed() {
		t.mu.Unlock()
		return len(data)
	}
	t.inPTYWrite = true
	written := core.Twrite(data, false)
	t.inPTYWrite = false
	t.wakeEvents()
	t.pauseRegionPaint(core.IsPaintPaused())
	if t.ptyPaintRequested && !core.IsPaintPaused() {
		t.ptyPaintRequested = false
		t.paint()
	}
	if t.host == nil && !core.IsPaintPaused() {
		t.paintSecondary()
	}
	t.mu.Unlock()
	return written
}

func (t *Terminal) paintTerminalOutput(core *term.Term) {
	t.mu.Lock()
	if !core.IsPaintPaused() {
		core.PaintDirty()
	}
	t.mu.Unlock()
}

// paint rasterizes and submits all queued cell damage. Callers hold t.mu, so
// terminal state, framebuffer state, resize, and X event painting stay in one
// serialization domain without a separate paint goroutine or snapshot copy.
func (t *Terminal) paint() {
	now := time.Now()
	if t.visualHidden {
		interval := hiddenRefresh(now.Sub(t.hiddenSince))
		if interval == 0 {
			return
		}
		if due := t.lastPaint.Add(interval); now.Before(due) {
			t.paintDeadline = due
			t.wakeEvents()
			return
		}
	} else if host, ok := t.host.(api.BackendIdentity); ok && host.BackendName() == "fb" && !t.lastPaint.IsZero() {
		interval := adaptiveRefresh(now.Sub(t.lastActivity))
		if t.termCore != nil && t.termCore.HasAnim() {
			interval = 16 * time.Millisecond
		}
		if now.Before(t.refreshUntil) {
			interval = min(interval, t.refreshInterval)
		}
		if due := t.lastPaint.Add(interval); now.Before(due) {
			t.paintDeadline = due
			t.wakeEvents()
			return
		}
	}
	t.paintNow()
}

// Only pending damage is paced. Idle terminals have no refresh timer at all.
func adaptiveRefresh(idle time.Duration) time.Duration {
	switch {
	case idle < 500*time.Millisecond:
		return 16 * time.Millisecond
	case idle < 2*time.Second:
		return 33 * time.Millisecond
	case idle < 5*time.Second:
		return 100 * time.Millisecond
	case idle < 15*time.Second:
		return 250 * time.Millisecond
	default:
		return time.Second
	}
}

func (t *Terminal) RequestRefresh(interval, duration time.Duration) {
	t.refreshInterval, t.refreshUntil = interval, time.Now().Add(duration)
	if !t.paintDeadline.IsZero() && duration > 0 {
		t.paintDeadline = minTime(t.paintDeadline, time.Now().Add(interval))
	}
	t.wakeEvents()
}

func (t *Terminal) paintNow() {
	t.paintDeadline = time.Time{}
	if t.closed() || t.termCore == nil || t.termCore.IsPaintPaused() {
		return
	}
	t.lastPaint = time.Now()
	if batch, ok := t.host.(api.PaintBatcher); ok {
		batch.BeginPaint()
		defer batch.EndPaint()
	}
	defer t.presentTextCursor()
	full := t.displayRegions.fullDirty
	if full {
		t.clearFramebuffer()
		t.displayRegions.mu.Lock()
		if t.displayRegions.secondary != nil {
			t.displayRegions.secondary.dirty = true
			t.requestRegionPaint()
		}
		t.displayRegions.mu.Unlock()
	}
	regions := t.termCore.DrawRegions()
	secondary := t.paintSecondary()
	if full {
		t.displayRegions.fullDirty = false
		t.putFramebufferRegion(0, 0, t.fbW, t.fbH)
		return
	}
	for _, region := range regions {
		px, py, w, h := t.pixelRegion(region)
		t.putFramebufferRegion(px, py, w, h)
	}
	if !secondary.Empty() {
		t.putFramebufferRegion(secondary.Min.X, secondary.Min.Y, secondary.Dx(), secondary.Dy())
	}
}

func (t *Terminal) pixelRegion(r term.Region) (px, py, w, h int) {
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
	if px+w > t.fbW {
		w = t.fbW - px
	}
	if py+h > t.fbH {
		h = t.fbH - py
	}
	if w < 1 || h < 1 || t.fbW < 1 || t.fbH < 1 {
		return 0, 0, 0, 0
	}
	return px, py, w, h
}

// putFramebufferRegion sends framebuffer rows directly to X in request-sized
// chunks. XGB copies each request before returning, so no retained snapshot is
// needed while terminal mutation remains serialized by t.mu.
func (t *Terminal) putFramebufferRegion(px, py, w, h int) {
	if w < 1 || h < 1 || len(t.framebuf) == 0 || t.host == nil {
		return
	}
	t.host.Upload(t.framebuf, t.fbW, px, py, w, h)
}
