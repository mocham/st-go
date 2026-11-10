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
	// Blinking cells are an external drawing trigger and paint immediately.
	if t.blinkMs > 0 {
		go func() {
			ticker := time.NewTicker(time.Duration(t.blinkMs) * time.Millisecond)
			defer ticker.Stop()
			for {
				select {
				case <-ticker.C:
					t.mu.Lock()
					if !t.closed() && t.termCore != nil && t.termCore.Tattrset(term.ATTRBlink) {
						t.toggleBlink()
						t.termCore.RedrawAttr(term.ATTRBlink)
					}
					t.mu.Unlock()
				case <-t.done:
					return
				}
			}
		}()
	}

	// animated image playback timer: advances the running animation (open DSL
	// `anim`). Each frame is wrapped in PaintStop/PaintResume, so one frame =
	// one atomic paint batch = one flush.
	go func() {
		ticker := time.NewTicker(16 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case now := <-ticker.C:
				t.mu.Lock()
				if !t.closed() && t.termCore != nil {
					t.termCore.TickAnim(now)
					t.flushRegionReplies()
				}
				t.mu.Unlock()
			case <-t.done:
				return
			}
		}
	}()

	t.host.Run()
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
	return api.Callbacks{Lock: t.mu.Lock, Unlock: t.mu.Unlock, Closed: t.closed,
		Expose: t.expose, ExposeSurface: t.exposeRegion, KeyPress: t.keyPress, Text: t.textInput,
		PointerPress: t.pointerPress, PointerRelease: t.pointerRelease, PointerMotion: t.pointerMotion,
		Resize: t.resize, Focus: t.focus, Close: t.Close,
		Paste:      func(text string) { t.pasteToTTY(replaceNewlines(text)) },
		PaintDirty: func() { t.termCore.PaintDirty() },
	}
}

func (t *Terminal) expose() {
	if t.termCore.IsPaintPaused() {
		t.putFramebufferRegion(0, 0, fbW, fbH)
	} else {
		t.displayRegions.fullDirty = true
		t.termCore.Redraw()
	}
}

func (t *Terminal) focus(focused bool) {
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
	newCols := (w - 2*t.borderpx) / t.cw
	newRows := (h - 2*t.borderpx) / t.ch
	if newCols < 1 {
		newCols = 1
	}
	if newRows < 1 {
		newRows = 1
	}
	if newCols == t.physicalCols() && newRows == t.physicalRows() && w == fbW && h == fbH {
		return
	}
	if !limits.Grid(newCols, newRows) || !ensureFramebuffer(w, h) {
		return
	}
	t.applyDisplayLayout(newCols, newRows)
}
