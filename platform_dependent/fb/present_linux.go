//go:build linux

package fb

import (
	"image"
	"time"
)

const (
	presentFrameInterval = time.Second / 60
	maxDamageRectangles  = 128
)

// invalidate only changes pending damage. The one-slot wakeup is a mailbox,
// not a queue of frames: repeated paints are folded into the newest state.
// Caller holds d.mu. Inactive VTs retain damage without waking the presenter.
func (d *Driver) invalidate(r image.Rectangle) {
	if d.closed {
		return
	}
	l := d.screen.layout
	r = r.Intersect(image.Rect(0, 0, l.width, l.height))
	if r.Empty() {
		return
	}
	// Merge only when the union is itself a rectangle. In particular, separate
	// dirty rows or distant cursor positions must not repaint the clean gap.
	for i := 0; i < len(d.damage); {
		old := d.damage[i]
		union, overlap := old.Union(r), old.Intersect(r)
		if union.Dx()*union.Dy() == old.Dx()*old.Dy()+r.Dx()*r.Dy()-overlap.Dx()*overlap.Dy() {
			r = union
			d.damage = append(d.damage[:i], d.damage[i+1:]...)
			i = 0
		} else {
			i++
		}
	}
	d.damage = append(d.damage, r)
	pixels := 0
	for _, rect := range d.damage {
		pixels += rect.Dx() * rect.Dy()
	}
	if len(d.damage) > maxDamageRectangles || pixels > l.width*l.height {
		d.damage = []image.Rectangle{image.Rect(0, 0, l.width, l.height)}
	}
	d.wakePresenter()
}

// Caller holds d.mu. Pending state is publishable only after its paint commits.
func (d *Driver) wakePresenter() {
	if d.active && !d.closed && d.paintDepth == 0 && len(d.damage) != 0 {
		select {
		case d.presentWake <- struct{}{}:
		default:
		}
	}
}

func (d *Driver) BeginPaint() {
	d.mu.Lock()
	d.paintDepth++
	d.mu.Unlock()
}

func (d *Driver) EndPaint() {
	d.mu.Lock()
	if d.paintDepth > 0 {
		d.paintDepth--
	}
	d.wakePresenter()
	d.mu.Unlock()
}

func presentDelay(last, now time.Time) time.Duration {
	if last.IsZero() {
		return presentFrameInterval
	}
	// Always target the next tick, including after idle/late frames. Producers
	// get a coalescing window rather than triggering an immediate flush.
	return presentFrameInterval - max(0, now.Sub(last))%presentFrameInterval
}

// startPresenter is called once by Run after initial host callbacks. It is
// intentionally independent of input polling, PTY painting, and PDF rendering.
func (d *Driver) startPresenter() {
	d.mu.Lock()
	d.presentWake = make(chan struct{}, 1)
	d.presentDone = make(chan struct{})
	wake, stopped := d.presentWake, d.presentDone
	d.wakePresenter()
	d.mu.Unlock()
	go d.presentLoop(wake, stopped)
}

func (d *Driver) presentLoop(wake <-chan struct{}, stopped chan<- struct{}) {
	defer close(stopped)
	var last time.Time
	timer := time.NewTimer(time.Hour)
	timer.Stop()
	defer timer.Stop()
	for {
		select {
		case <-d.done:
			return
		case <-wake:
		}
		timer.Reset(presentDelay(last, time.Now()))
		select {
		case <-d.done:
			return
		case <-timer.C:
		}
		// All updates already queued will be covered by the next snapshot.
		select {
		case <-wake:
		default:
		}
		last = time.Now()
		d.flushPending()
	}
}

// flushPending detaches one damage batch, composes final pixels in a private
// native-format back buffer, and then releases the producer mutex BEFORE any
// hardware copy. Damage arriving during that copy belongs to the next batch;
// it is never cleared by completion of this one. Only this worker presents.
// presentMu also serializes VT release, player handoff, restoration and unmap.
func (d *Driver) flushPending() {
	d.presentMu.Lock()
	defer d.presentMu.Unlock()
	d.mu.Lock()
	if d.closed || !d.active || d.paintDepth != 0 || len(d.damage) == 0 {
		d.mu.Unlock()
		return
	}
	if len(d.backBuffer) != d.screen.layout.length {
		// screenLayout already bounds this allocation to limits.PixelBytes.
		d.backBuffer = make([]byte, d.screen.layout.length)
	}
	damage := d.damage
	d.damage = nil
	for _, r := range damage {
		d.composeRect(d.backBuffer, r)
	}
	frame := d.backBuffer
	screen := d.screen
	d.mu.Unlock()
	if screen.present != nil {
		screen.present(frame, damage)
	} else {
		screen.copyFrame(frame, damage)
	}
}

// Wait for any in-flight presentation before relinquishing scanout ownership.
// Never acquire presentMu while holding d.mu; the presenter uses the same order.
func (d *Driver) suspendPresentation() {
	d.presentMu.Lock()
	d.mu.Lock()
	d.active, d.restorePixels = false, false
	d.mu.Unlock()
	d.presentMu.Unlock()
}
