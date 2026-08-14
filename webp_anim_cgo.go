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
int webp_anim_info(const uint8_t *data, size_t len, int *width, int *height, uint32_t **durations);
int webp_anim_next(void *handle, uint32_t *timestamp_ms, uint8_t **buf);
void webp_anim_delete(void *handle);
*/
import "C"

import (
	"crypto/sha256"
	"time"
	"unsafe"
)

// animFrame is one composited animated-WebP frame (full canvas RGBA) plus how
// long to show it.
type animFrame struct {
	rgba     []byte
	duration time.Duration
}

// webPAnimDecoder keeps libwebp's forward-only compositing state so sequential
// playback decodes each frame once instead of replaying frames 0..N on every
// tick. It owns both the C input copy and decoder handle.
type webPAnimDecoder struct {
	handle     unsafe.Pointer
	data       unsafe.Pointer
	sourceHash [sha256.Size]byte
	w, h       int
	frameCount int
	index      int
}

func openWebPAnimDecoder(data []byte) (*webPAnimDecoder, bool) {
	if len(data) == 0 {
		return nil, false
	}
	cdata := C.CBytes(data)
	var cw, ch, ccount C.int
	handle := C.webp_anim_open((*C.uint8_t)(cdata), C.size_t(len(data)), &cw, &ch, &ccount)
	if handle == nil || cw <= 0 || ch <= 0 || ccount <= 0 {
		if handle != nil {
			C.webp_anim_delete(handle)
		}
		C.free(cdata)
		return nil, false
	}
	return &webPAnimDecoder{
		handle:     handle,
		data:       cdata,
		sourceHash: sha256.Sum256(data),
		w:          int(cw),
		h:          int(ch),
		frameCount: int(ccount),
		index:      -1,
	}, true
}

func (d *webPAnimDecoder) close() {
	if d == nil {
		return
	}
	if d.handle != nil {
		C.webp_anim_delete(d.handle)
		d.handle = nil
	}
	if d.data != nil {
		C.free(d.data)
		d.data = nil
	}
}

func (d *webPAnimDecoder) frame(frameIdx int) (w, h int, rgba []byte, ok bool) {
	if d == nil || d.handle == nil || frameIdx <= d.index || frameIdx >= d.frameCount {
		return 0, 0, nil, false
	}
	for d.index < frameIdx {
		var buf *C.uint8_t
		if C.webp_anim_next(d.handle, nil, &buf) == 0 {
			return 0, 0, nil, false
		}
		d.index++
		if d.index == frameIdx {
			rgba = C.GoBytes(unsafe.Pointer(buf), C.int(d.w*d.h*4))
		}
	}
	return d.w, d.h, rgba, rgba != nil
}

func (t *Terminal) decodeWebPAnimFrameSequential(data []byte, frameIdx int) (w, h int, rgba []byte, ok bool) {
	hash := sha256.Sum256(data)
	if t.webpAnimDecoder == nil || t.webpAnimDecoder.sourceHash != hash || frameIdx <= t.webpAnimDecoder.index {
		if t.webpAnimDecoder != nil {
			t.webpAnimDecoder.close()
		}
		decoder, opened := openWebPAnimDecoder(data)
		if !opened {
			t.webpAnimDecoder = nil
			return 0, 0, nil, false
		}
		t.webpAnimDecoder = decoder
	}
	w, h, rgba, ok = t.webpAnimDecoder.frame(frameIdx)
	if !ok {
		t.webpAnimDecoder.close()
		t.webpAnimDecoder = nil
	}
	return w, h, rgba, ok
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

// decodeWebPAnimInfo reads an animated WebP's durations and canvas size through
// libwebpdemux without rasterizing any frames.
func decodeWebPAnimInfo(data []byte) (durations []int, w, h int, ok bool) {
	if len(data) == 0 {
		return nil, 0, 0, false
	}
	var cw, ch C.int
	var cdurations *C.uint32_t
	count := C.webp_anim_info(
		(*C.uint8_t)(unsafe.Pointer(&data[0])), C.size_t(len(data)), &cw, &ch, &cdurations,
	)
	if count <= 0 || cw <= 0 || ch <= 0 || cdurations == nil {
		return nil, 0, 0, false
	}
	defer C.free(unsafe.Pointer(cdurations))
	w, h = int(cw), int(ch)
	values := unsafe.Slice(cdurations, int(count))
	durations = make([]int, len(values))
	for i, duration := range values {
		ms := int(duration)
		if ms < 5 {
			ms = 5
		}
		durations[i] = ms
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
