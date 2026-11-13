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
	audio(rate int) ([]byte, error)
	close()
}

var openDecoder func(context.Context, string) (decoder, error)

func Supported() bool { return openDecoder != nil }
