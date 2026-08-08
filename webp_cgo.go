// webp_cgo.go bridges st-go to libwebp for WebP decoding.
// stb_image does not support WebP, so we use libwebp's WebPDecodeRGBA.
//
// The WebP symbols are resolved at link time from either the real libwebp.a
// or the no-op dummy-webp.o (see Makefile targets); a dummy makes decodeWebP
// return false so the caller just shows nothing.
package main

/*
#include <stdlib.h>
#include <stdint.h>
#include <stddef.h>
int WebPGetInfo(const uint8_t *data, size_t data_size, int *width, int *height);
uint8_t *WebPDecodeRGBA(const uint8_t *data, size_t data_size, int *width, int *height);
void WebPFree(void *ptr);
*/
import "C"

import (
	"unsafe"
)

// decodeWebP decodes a WebP image held in memory into unpremultiplied RGBA.
// Returns width, height and the RGBA pixels, or ok=false on failure.
func decodeWebP(data []byte) (w, h int, rgba []byte, ok bool) {
	if len(data) == 0 {
		return 0, 0, nil, false
	}
	cdata := C.CBytes(data)
	defer C.free(cdata)

	var cw, ch C.int
	if C.WebPGetInfo((*C.uint8_t)(cdata), C.size_t(len(data)), &cw, &ch) == 0 {
		return 0, 0, nil, false
	}
	if cw <= 0 || ch <= 0 {
		return 0, 0, nil, false
	}
	w, h = int(cw), int(ch)

	p := C.WebPDecodeRGBA((*C.uint8_t)(cdata), C.size_t(len(data)), &cw, &ch)
	if p == nil {
		return 0, 0, nil, false
	}
	defer C.WebPFree(unsafe.Pointer(p))
	rgba = C.GoBytes(unsafe.Pointer(p), C.int(w*h*4))
	return w, h, rgba, true
}
