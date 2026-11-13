package frontend

import (
	"time"

	"st-go/limits"
	"st-go/platform_neutral/api"
	term "st-go/platform_neutral/stterminal"
)

// run starts neutral timers and delegates native event dispatch to the host.
// The core was attached before PTY startup; don't reassign it while output runs.
func (t *Terminal) run() {
	go t.eventLoop()
	t.host.Run()
}

// A mailbox, not a periodic poll. Workers may signal while holding their own
// locks; only this consumer takes the terminal lock and writes protocol replies.
func (t *Terminal) wakeEvents() {
	select {
	case t.eventWake <- struct{}{}:
	default:
	}
}

func (t *Terminal) eventLoop() {
	timer := time.NewTimer(time.Hour)
	timer.Stop()
	defer timer.Stop()
	for {
		t.mu.Lock()
		if t.closed() {
			t.mu.Unlock()
			return
		}
		next := t.serviceEvents(time.Now())
		t.mu.Unlock()
		var deadline <-chan time.Time
		if !next.IsZero() {
			timer.Reset(max(time.Millisecond, time.Until(next)))
			deadline = timer.C
		}
		select {
		case <-t.done:
			return
		case <-t.eventWake:
		case <-deadline:
		}
		timer.Stop()
	}
}

func (t *Terminal) serviceEvents(now time.Time) time.Time {
	if t.termCore == nil {
		return time.Time{}
	}
	t.flushRegionReplies() // replies must progress even for invisible PTYs
	t.pollIME()
	if t.visualHidden {
		t.nextBlink = time.Time{}
		interval := hiddenRefresh(now.Sub(t.hiddenSince))
		if interval == 0 {
			t.paintDeadline = time.Time{}
			return time.Time{}
		}
		// Recent hidden tabs still prepare pending text/animation changes at a
		// progressively slower cadence. Static tabs need no speculative wakeups.
		next := t.termCore.NextAnimDeadline()
		if !next.IsZero() && !now.Before(next) {
			t.termCore.TickAnim(now)
			next = t.termCore.NextAnimDeadline()
		}
		if !t.paintDeadline.IsZero() && !now.Before(t.paintDeadline) {
			t.paintNow()
		}
		if !next.IsZero() && next.Before(now.Add(interval)) {
			next = now.Add(interval)
		}
		if !t.paintDeadline.IsZero() && (next.IsZero() || t.paintDeadline.Before(next)) {
			next = t.paintDeadline
		}
		if !next.IsZero() {
			next = minTime(next, t.hiddenSince.Add(2*time.Second))
		}
		return next
	}
	if next := t.termCore.NextAnimDeadline(); !next.IsZero() && !now.Before(next) {
		t.termCore.TickAnim(now)
	}
	if t.blinkMs > 0 && t.termCore.Tattrset(term.ATTRBlink) {
		if t.nextBlink.IsZero() {
			t.nextBlink = now.Add(time.Duration(t.blinkMs) * time.Millisecond)
		} else if !now.Before(t.nextBlink) {
			t.toggleBlink()
			t.termCore.RedrawAttr(term.ATTRBlink)
			t.nextBlink = now.Add(time.Duration(t.blinkMs) * time.Millisecond)
		}
	} else {
		t.nextBlink = time.Time{}
	}
	if !t.paintDeadline.IsZero() && !now.Before(t.paintDeadline) {
		t.paintDeadline = time.Time{}
		t.paintNow()
	}
	next := t.termCore.NextAnimDeadline()
	for _, at := range []time.Time{t.nextBlink, t.paintDeadline} {
		if !at.IsZero() && (next.IsZero() || at.Before(next)) {
			next = at
		}
	}
	return next
}

func hiddenRefresh(elapsed time.Duration) time.Duration {
	switch {
	case elapsed < 250*time.Millisecond:
		return 33 * time.Millisecond
	case elapsed < time.Second:
		return 100 * time.Millisecond
	case elapsed < 2*time.Second:
		return 250 * time.Millisecond
	default:
		return 0
	}
}

func (t *Terminal) activity() {
	t.lastActivity = time.Now()
	if !t.paintDeadline.IsZero() {
		t.paintDeadline = t.lastActivity.Add(16 * time.Millisecond)
	}
}

// toggleBlink flips the blink window mode so glyphs render on/off.
func (t *Terminal) toggleBlink() {
	if t.winMode&term.ModeBlink != 0 {
		t.winMode &^= term.ModeBlink
	} else {
		t.winMode |= term.ModeBlink
	}
}

func (t *Terminal) eventCallbacks() api.Callbacks {
	return api.Callbacks{Lock: t.mu.Lock, Unlock: func() { t.wakeEvents(); t.mu.Unlock() }, Closed: t.closed,
		Expose: t.expose, ExposeSurface: t.exposeRegion,
		KeyPress:       func(key uint, mods uint16) { t.activity(); t.keyPress(key, mods) },
		Text:           func(text string) { t.activity(); t.textInput(text) },
		PointerPress:   func(b byte, m uint16, x, y int) { t.activity(); t.pointerPress(b, m, x, y) },
		PointerRelease: func(b byte, m uint16, x, y int) { t.activity(); t.pointerRelease(b, m, x, y) },
		PointerMotion:  func(m uint16, x, y int) { t.activity(); t.pointerMotion(m, x, y) },
		Resize:         t.resize, Focus: t.focus, Close: t.Close,
		Visibility: func(visible bool) {
			changed := t.visualHidden == visible
			t.visualHidden = !visible
			if changed && !visible {
				t.hiddenSince = time.Now()
			}
			if changed && visible && t.termCore != nil {
				t.activity()
				t.termCore.TickAnim(time.Now())
				t.expose()
			}
		},
		Paste:      func(text string) { t.activity(); t.pasteToTTY(replaceNewlines(text)) },
		PaintDirty: func() { t.termCore.PaintDirty() },
		CancelInput: func() {
			t.buttons, t.regionButtons = 0, 0
			t.msel, t.suppressRestoreButton = mouseSel{}, false
			t.displayRegions.mu.Lock()
			if s := t.displayRegions.secondary; s != nil {
				s.dragging = false
			}
			t.displayRegions.mu.Unlock()
		},
	}
}

func (t *Terminal) expose() {
	if t.termCore.IsPaintPaused() {
		t.putFramebufferRegion(0, 0, t.fbW, t.fbH)
	} else {
		t.displayRegions.fullDirty = true
		t.termCore.Redraw()
	}
}

func (t *Terminal) focus(focused bool) {
	if !focused && t.inputMethod.stream != nil {
		t.flushIME()
		t.paintIME()
	}
	t.setWinMode(focused, term.ModeFocused)
	if t.termCore.WinMode()&term.ModeFocus != 0 {
		sequence := "\x1b[O"
		if focused {
			sequence = "\x1b[I"
		}
		t.termCore.WriteToTTY([]byte(sequence), false)
	}
	t.paint()
}

// resize recomputes cols/rows and resizes the terminal.
func (t *Terminal) resize(w, h int) {
	defer t.paintIME()
	newCols := (w - 2*t.borderpx) / t.cw
	newRows := (h - 2*t.borderpx) / t.ch
	if newCols < 1 {
		newCols = 1
	}
	if newRows < 1 {
		newRows = 1
	}
	if newCols == t.physicalCols() && newRows == t.physicalRows() && w == t.fbW && h == t.fbH {
		return
	}
	if !limits.Grid(newCols, newRows) || !t.ensureFramebuffer(w, h) {
		return
	}
	t.applyDisplayLayout(newCols, newRows)
}
