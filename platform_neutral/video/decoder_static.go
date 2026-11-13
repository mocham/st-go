//go:build linux && ffmpeg_static && cgo && !purego

package video

/*
#cgo CFLAGS: -I${SRCDIR}/../../third_party/ffmpeg/include -I${SRCDIR}/../../third_party_wrapper
#cgo LDFLAGS: ${SRCDIR}/../../third_party/video_ffmpeg.o ${SRCDIR}/../../third_party/ffmpeg/lib/libavformat.a ${SRCDIR}/../../third_party/ffmpeg/lib/libavfilter.a ${SRCDIR}/../../third_party/ffmpeg/lib/libavcodec.a ${SRCDIR}/../../third_party/ffmpeg/lib/libswscale.a ${SRCDIR}/../../third_party/ffmpeg/lib/libswresample.a ${SRCDIR}/../../third_party/ffmpeg/lib/libavutil.a ${SRCDIR}/../../third_party/x264/lib/libx264.a -lm -lpthread
#include <stdlib.h>
#include <libavutil/error.h>
#include <st_go_ffmpeg_static.h>
#include "video_ffmpeg.h"
*/
import "C"

import (
	"context"
	"fmt"
	"time"
	"unsafe"
)

type nativeDecoder struct {
	handle   *C.st_video
	metadata metadata
	cancel   context.CancelFunc
	joined   chan struct{}
	buffer   []uint32
}

func init() { openDecoder = openNative }
func nativeError(code C.int) error {
	if code >= 0 {
		return nil
	}
	var text [256]C.char
	C.av_strerror(code, &text[0], C.size_t(len(text)))
	return fmt.Errorf("video: %s", C.GoString(&text[0]))
}
func openNative(ctx context.Context, path string) (decoder, error) {
	h := C.st_video_alloc()
	if h == nil {
		return nil, fmt.Errorf("cannot allocate video decoder")
	}
	ctx, cancel := context.WithCancel(ctx)
	d := &nativeDecoder{handle: h, cancel: cancel, joined: make(chan struct{})}
	go func() { <-ctx.Done(); C.st_video_cancel(h); close(d.joined) }()
	name := C.CString(path)
	err := nativeError(C.st_video_open(h, name))
	C.free(unsafe.Pointer(name))
	if err != nil {
		d.close()
		return nil, err
	}
	var w, hgt, a C.int
	var duration C.int64_t
	C.st_video_info(h, &w, &hgt, &a, &duration)
	d.metadata = metadata{int(w), int(hgt), a != 0, time.Duration(duration) * time.Microsecond}
	return d, nil
}
func (d *nativeDecoder) info() metadata { return d.metadata }
func (d *nativeDecoder) close()         { d.cancel(); <-d.joined; C.st_video_free(d.handle) }
func (d *nativeDecoder) next() (int, time.Duration, error) {
	var pts C.int64_t
	k := C.st_video_next(d.handle, &pts)
	return int(k), time.Duration(pts) * time.Microsecond, nativeError(k)
}
func (d *nativeDecoder) seek(pos time.Duration) error {
	return nativeError(C.st_video_seek(d.handle, C.int64_t(pos/time.Microsecond)))
}
func (d *nativeDecoder) pixels(w, h int) ([]uint32, error) {
	if w < 1 || h < 1 || int64(w)*int64(h) > 4096*2160 {
		return nil, fmt.Errorf("video surface exceeds 4096x2160 pixel budget")
	}
	size := w * h
	if cap(d.buffer) < size+16 {
		d.buffer = make([]uint32, size+16)
	}
	p := d.buffer[:size]
	for i := range p {
		p[i] = 0xff000000
	}
	err := nativeError(C.st_video_pixels(d.handle, (*C.uint8_t)(unsafe.Pointer(&p[0])), C.int(w), C.int(h), C.int(w*4)))
	return p, err
}
func (d *nativeDecoder) resetAudio() { C.st_video_audio_reset(d.handle) }
func (d *nativeDecoder) audio(rate int, speed float64, flush bool) ([]byte, error) {
	var data *C.uint8_t
	var size C.int
	var drain C.int
	if flush {
		drain = 1
	}
	if err := nativeError(C.st_video_audio(d.handle, C.int(rate), C.double(speed), drain, &data, &size)); err != nil {
		return nil, err
	}
	// Worker consumes this borrowed native buffer before the next decoder call.
	return unsafe.Slice((*byte)(unsafe.Pointer(data)), int(size)), nil
}
