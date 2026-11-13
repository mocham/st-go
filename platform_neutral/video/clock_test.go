package video

import (
	"testing"
	"time"
)

func TestPlaybackClockChangesRateWithoutMovingMediaPosition(t *testing.T) {
	now := time.Unix(1234, 0)
	c := playbackClock{wall: now, speed: 1}
	change := now.Add(200 * time.Millisecond)
	c.reset(change, c.position(change))
	c.speed = 2
	if got := c.position(change); got != 200*time.Millisecond {
		t.Fatal("rate change jumped", got)
	}
	if got := c.deadline(time.Second); !got.Equal(now.Add(600 * time.Millisecond)) {
		t.Fatal("incorrect fast deadline", got)
	}
	change = now.Add(400 * time.Millisecond)
	c.reset(change, c.position(change))
	c.speed = 0.5
	if got := c.position(change); got != 600*time.Millisecond {
		t.Fatal("second rate change jumped", got)
	}
	if got := c.deadline(time.Second); !got.Equal(now.Add(1200 * time.Millisecond)) {
		t.Fatal("incorrect slow deadline", got)
	}
	// Resuming after pause and seeking use a fresh anchor at the same rate.
	c.reset(now.Add(10*time.Second), 2*time.Second)
	if got := c.deadline(2500 * time.Millisecond); !got.Equal(now.Add(11 * time.Second)) {
		t.Fatal("rate lost after pause/seek", got)
	}
}
