//go:build !linux || !cgo || !ffmpeg_static || purego

package main

import (
	"fmt"
	"io"
	"os"
)

func run(options, *os.File, io.Writer) error {
	return fmt.Errorf("native DRM playback requires Linux, cgo and the bundled decoder; build with make gov2")
}
