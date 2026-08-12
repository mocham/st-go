// webp_anim_cgo.go bridges st-go to libwebp's WebPAnimDecoder (from
// libwebpdemux) for animated WebP playback. The C symbols are resolved at
// link time from the real libwebp.a + libwebpdemux.a (or the no-op dummy in
// reduced builds, which makes decodeWebPAnim return false).
package main

/*
#include <stdlib.h>
#include <stdint.h>
#include <stddef.h>
void *webp_anim_open(const uint8_t *data, size_t len, int *width, int *height, int *frame_count);
int webp_anim_next(void *handle, uint32_t *timestamp_ms, uint8_t **buf);
void webp_anim_delete(void *handle);
*/
import "C"

import (
	"time"
	"unsafe"
)

// animFrame is one composited animated-WebP frame (full canvas RGBA) plus how
// long to show it.
type animFrame struct {
	rgba     []byte
	duration time.Duration
}

// decodeWebPAnim decodes an animated WebP into its frames (composited by
// libwebp, which applies partial-frame disposal) and per-frame durations
// derived from the frame timestamps. Returns ok=false for static or non-WebP
// data.
func decodeWebPAnim(data []byte) (w, h int, frames []animFrame, ok bool) {
	if len(data) == 0 {
		return 0, 0, nil, false
	}
	cdata := C.CBytes(data)
	defer C.free(cdata)

	var cw, ch, ccount C.int
	hnd := C.webp_anim_open((*C.uint8_t)(cdata), C.size_t(len(data)), &cw, &ch, &ccount)
	if hnd == nil {
		return 0, 0, nil, false
	}
	defer C.webp_anim_delete(hnd)

	w, h = int(cw), int(ch)
	if w <= 0 || h <= 0 || ccount <= 0 {
		return 0, 0, nil, false
	}

	var timestamps []int64
	var bufs [][]byte
	for {
		var ts C.uint32_t
		var buf *C.uint8_t
		if C.webp_anim_next(hnd, &ts, &buf) == 0 {
			break
		}
		timestamps = append(timestamps, int64(ts))
		bufs = append(bufs, C.GoBytes(unsafe.Pointer(buf), C.int(w*h*4)))
	}
	if len(bufs) == 0 {
		return 0, 0, nil, false
	}
	for i, b := range bufs {
		dur := 100 * time.Millisecond
		switch {
		case i+1 < len(timestamps) && timestamps[i+1] > timestamps[i]:
			dur = time.Duration(timestamps[i+1]-timestamps[i]) * time.Millisecond
		case i == len(timestamps)-1 && len(timestamps) > 1 && timestamps[1] > timestamps[0]:
			// last frame: assume the previous frame's duration
			dur = time.Duration(timestamps[1]-timestamps[0]) * time.Millisecond
		}
		if dur < 5*time.Millisecond {
			dur = 5 * time.Millisecond
		}
		frames = append(frames, animFrame{rgba: b, duration: dur})
	}
	return w, h, frames, true
}

// decodeWebPAnimInfo decodes an animated WebP for its metadata only (frame
// durations, canvas size, frame count), discarding the pixel data. It is used
// to set up playback timing without holding the frame bitmaps.
func decodeWebPAnimInfo(data []byte) (durations []int, w, h int, ok bool) {
	w, h, frames, ok := decodeWebPAnim(data)
	if !ok {
		return nil, 0, 0, false
	}
	for _, f := range frames {
		ms := int(f.duration / time.Millisecond)
		if ms < 5 {
			ms = 5
		}
		durations = append(durations, ms)
	}
	return durations, w, h, true
}

// decodeWebPAnimFrame decodes a single frame (frameIdx, 0-based) of an
// animated WebP into a full-canvas RGBA buffer. WebPAnimDecoder is forward
// only, so frames 0..frameIdx are decoded and earlier ones discarded; this
// keeps a playing animation's memory bounded to one frame at a time.
func decodeWebPAnimFrame(data []byte, frameIdx int) (w, h int, rgba []byte, ok bool) {
	if len(data) == 0 || frameIdx < 0 {
		return 0, 0, nil, false
	}
	cdata := C.CBytes(data)
	defer C.free(cdata)

	var cw, ch, ccount C.int
	hnd := C.webp_anim_open((*C.uint8_t)(cdata), C.size_t(len(data)), &cw, &ch, &ccount)
	if hnd == nil {
		return 0, 0, nil, false
	}
	defer C.webp_anim_delete(hnd)

	w, h = int(cw), int(ch)
	if w <= 0 || h <= 0 {
		return 0, 0, nil, false
	}
	for i := 0; i <= frameIdx; i++ {
		var buf *C.uint8_t
		if C.webp_anim_next(hnd, nil, &buf) == 0 {
			return 0, 0, nil, false
		}
		if i == frameIdx {
			rgba = C.GoBytes(unsafe.Pointer(buf), C.int(w*h*4))
		}
	}
	return w, h, rgba, rgba != nil
}
