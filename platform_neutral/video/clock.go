package video

import "time"

// The media and wall-clock anchors keep rate changes continuous even when the
// next decoded frame is waiting for its presentation deadline.
type playbackClock struct {
	wall  time.Time
	media time.Duration
	speed float64
}

func (c *playbackClock) reset(now time.Time, position time.Duration) {
	c.wall, c.media = now, position
}

func (c *playbackClock) position(now time.Time) time.Duration {
	return c.media + time.Duration(float64(now.Sub(c.wall))*c.speed)
}

func (c *playbackClock) deadline(position time.Duration) time.Time {
	return c.wall.Add(time.Duration(float64(position-c.media) / c.speed))
}
