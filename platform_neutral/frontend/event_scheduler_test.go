//go:build terminal_tabs

package frontend

import (
	"testing"
	"time"

	"st-go/platform_neutral/api"
	term "st-go/platform_neutral/stterminal"
)

type framebufferSchedulingHost struct{ api.Host }

func (framebufferSchedulingHost) BackendName() string { return "fb" }

type animationSchedulingHooks struct{ *Terminal }

func (animationSchedulingHooks) ImageDecodeAnimFrame([]byte, int, term.ImageDecodeOptions) (int, int, []term.Glyph, bool) {
	return 1, 1, []term.Glyph{{U: 'A'}}, true
}

func TestEventSchedulerHiddenAnimationGraceAndResume(t *testing.T) {
	trm, _, _ := tabTestTerminal(t)
	trm.mu.Lock()
	defer trm.mu.Unlock()
	trm.attachTerm(term.NewTerm(trm.cfg, animationSchedulingHooks{trm}))
	trm.termCore.SetAnim([]byte("test"), term.ImageDecodeOptions{}, []int{10000, 10000}, 1, 1, 0, 0, 4, 3)
	trm.eventCallbacks().Visibility(false)
	start := trm.hiddenSince
	for _, elapsed := range []time.Duration{0, 500 * time.Millisecond, 1500 * time.Millisecond} {
		now := start.Add(elapsed)
		if next := trm.serviceEvents(now); next.IsZero() || !next.After(now) {
			t.Fatal("animation grace blocked prematurely", elapsed, next)
		}
	}
	if next := trm.serviceEvents(start.Add(2 * time.Second)); !next.IsZero() {
		t.Fatal("hidden animation did not block", next)
	}
	trm.eventCallbacks().Visibility(true)
	if next := trm.serviceEvents(time.Now()); next.IsZero() {
		t.Fatal("reactivation failed to resume animation deadlines")
	}
}

func TestEventSchedulerIdleHiddenRepliesAndReactivation(t *testing.T) {
	trm, _, replies := tabTestTerminal(t)
	trm.mu.Lock()
	defer trm.mu.Unlock()
	now := time.Now()
	if next := trm.serviceEvents(now); !next.IsZero() {
		t.Fatal("idle terminal has timer", next)
	}
	trm.termCore.Twrite([]byte("\x1b[5mblink"), false)
	if next := trm.serviceEvents(now); next.IsZero() {
		t.Fatal("blink timer not armed")
	}
	cb := trm.eventCallbacks()
	cb.Visibility(false)
	trm.pdfSearch.reply = "hidden reply"
	if next := trm.serviceEvents(now.Add(3 * time.Second)); !next.IsZero() {
		t.Fatal("hidden terminal still wakes", next)
	}
	if replies.String() != "hidden reply" {
		t.Fatal("hidden terminal lost protocol reply", replies.String())
	}
	trm.termCore.Twrite([]byte("\x1b[0m\x1b[2J\x1b[Hlatest"), false)
	cb.Visibility(true)
	if trm.visualHidden || trm.termCore.HasPendingPaint() {
		t.Fatal("reactivation failed to paint latest state")
	}
	if next := trm.serviceEvents(now.Add(4 * time.Second)); !next.IsZero() {
		t.Fatal("removed blink retained timer")
	}
}

func TestEventSchedulerFramebufferPacingAndLease(t *testing.T) {
	trm, _, _ := tabTestTerminal(t)
	trm.mu.Lock()
	defer trm.mu.Unlock()
	original := trm.host
	trm.host = framebufferSchedulingHost{original}
	defer func() { trm.host = original }()
	trm.lastActivity = time.Now().Add(-time.Minute)
	trm.lastPaint = time.Now()
	trm.paint()
	if delta := trm.paintDeadline.Sub(trm.lastPaint); delta != time.Second {
		t.Fatal("inactive pacing", delta)
	}
	trm.RequestRefresh(33*time.Millisecond, 2*time.Minute)
	if time.Until(trm.paintDeadline) > 40*time.Millisecond {
		t.Fatal("lease did not accelerate pending work")
	}
	trm.refreshUntil = time.Now().Add(-time.Second)
	trm.paint()
	if delta := trm.paintDeadline.Sub(trm.lastPaint); delta != time.Second {
		t.Fatal("expired lease still overrides cadence", delta)
	}
	trm.activity()
	if time.Until(trm.paintDeadline) > 20*time.Millisecond {
		t.Fatal("input failed to accelerate pending work")
	}
	trm.visualHidden = true
	if next := trm.serviceEvents(time.Now()); !next.IsZero() {
		t.Fatal("static hidden tab has timer")
	}
}

func TestEventSchedulerHiddenTextGraceThenBlocks(t *testing.T) {
	trm, _, _ := tabTestTerminal(t)
	trm.mu.Lock()
	defer trm.mu.Unlock()
	trm.eventCallbacks().Visibility(false)
	trm.lastPaint = time.Now()
	trm.termCore.Twrite([]byte("recent update"), false)
	trm.paint()
	if trm.paintDeadline.IsZero() {
		t.Fatal("recent hidden tab blocked immediately")
	}
	trm.serviceEvents(time.Now().Add(100 * time.Millisecond))
	if trm.termCore.HasPendingPaint() {
		t.Fatal("hidden grace did not prepare recent text")
	}
	trm.hiddenSince = time.Now().Add(-3 * time.Second)
	trm.termCore.Twrite([]byte("later update"), false)
	trm.paint()
	if !trm.termCore.HasPendingPaint() || !trm.serviceEvents(time.Now()).IsZero() {
		t.Fatal("old hidden tab kept rendering")
	}
	trm.eventCallbacks().Visibility(true)
	if trm.termCore.HasPendingPaint() {
		t.Fatal("reactivation failed to restore newest text")
	}
}
