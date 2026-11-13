//go:build linux && ffmpeg_static && cgo && !purego

package fb

/*
#cgo CFLAGS: -I${SRCDIR}/../../third_party/ffmpeg/include -I${SRCDIR}/../../third_party_wrapper
#cgo LDFLAGS: ${SRCDIR}/../../third_party/recording_ffmpeg.o ${SRCDIR}/../../third_party/ffmpeg/lib/libavformat.a ${SRCDIR}/../../third_party/ffmpeg/lib/libavcodec.a ${SRCDIR}/../../third_party/ffmpeg/lib/libswscale.a ${SRCDIR}/../../third_party/ffmpeg/lib/libavutil.a ${SRCDIR}/../../third_party/x264/lib/libx264.a -lm -lpthread
#include <stdlib.h>
#include <libavcodec/defs.h>
#include <libavutil/error.h>
#include <st_go_ffmpeg_static.h>
#include "recording_ffmpeg.h"
*/
import "C"

import (
	"fmt"
	"image"
	"runtime"
	"time"
	"unsafe"
)

func recordingNativeError(operation string, result C.int) error {
	if result >= 0 {
		return nil
	}
	var text [C.AV_ERROR_MAX_STRING_SIZE]C.char
	C.av_strerror(result, &text[0], C.size_t(len(text)))
	return fmt.Errorf("%s: %s", operation, C.GoString(&text[0]))
}

// The full framebuffer binary links these components directly. The recording
// worker is the sole owner of all native state; C never retains Go pixel memory.
func encodeRecordingFile(path string, width, height int, stop <-chan struct{}, capture func(*image.RGBA)) error {
	name := C.CString(path)
	defer C.free(unsafe.Pointer(name))
	var encoder *C.st_recording
	threads := min(4, runtime.GOMAXPROCS(0))
	if err := recordingNativeError("open recording", C.st_recording_open(name, C.int(width), C.int(height), recordingFPS, C.int(threads), &encoder)); err != nil {
		return err
	}
	defer C.st_recording_free(encoder)
	// Native SIMD routines may read beyond the last pixel. Keep zeroed tail
	// padding outside Pix's visible length, just like FFmpeg-owned buffers.
	size := width * height * 4
	pixels := make([]byte, size+int(C.AV_INPUT_BUFFER_PADDING_SIZE))
	frame := &image.RGBA{Pix: pixels[:size], Stride: width * 4, Rect: image.Rect(0, 0, width, height)}
	ticker := time.NewTicker(time.Second / recordingFPS)
	defer ticker.Stop()
	started := time.Now()
	var nextPTS int64
	for {
		capture(frame)
		// Preserve wall-clock duration if capture/encoding misses a tick. MP4
		// holds the preceding frame across timestamp gaps without a frame queue.
		pts := max(nextPTS, int64(time.Since(started))*recordingFPS/int64(time.Second))
		result := C.st_recording_write(encoder, (*C.uint8_t)(unsafe.Pointer(&frame.Pix[0])), C.int(frame.Stride), C.int64_t(pts))
		if err := recordingNativeError("encode recording frame", result); err != nil {
			return err
		}
		nextPTS = pts + 1
		select {
		case <-stop:
			return recordingNativeError("finalize recording", C.st_recording_finish(encoder))
		case <-ticker.C:
		}
	}
}
