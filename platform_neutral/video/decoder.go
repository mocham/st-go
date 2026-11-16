// Package video owns bounded, terminal-local MP4 decoding and playback.
package video

import (
	"context"
	"time"
)

type metadata struct {
	width, height int
	audio         bool
	duration      time.Duration
}
type decoder interface {
	info() metadata
	next() (kind int, pts time.Duration, err error)
	seek(time.Duration) error
	pixels(width, height int) ([]uint32, error)
	// Audio outputs stereo S16 at rate, time-stretched by speed. Flush drains
	// delayed samples at EOF instead of feeding the current decoded frame.
	audio(rate int, speed float64, flush bool) ([]byte, error)
	resetAudio()
	close()
}

var openDecoder func(context.Context, string) (decoder, error)

func Supported() bool { return openDecoder != nil }
