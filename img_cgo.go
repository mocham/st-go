package main

/*
#include <stdlib.h>
unsigned char *stb_decode_rgba(const unsigned char *data, int len, int *width, int *height);
*/
import "C"

import (
	"unsafe"
)

// decodeImage decodes an encoded image (PNG/JPEG/GIF/BMP/WebP...) held in
// memory into unpremultiplied RGBA. Returns width, height and the RGBA pixels.
//
// The stb_decode_rgba symbol is resolved at link time from either the real
// stb_image.o or the no-op dummy-stb.o (see Makefile targets); a dummy makes
// this return an error so the caller just shows nothing.
func decodeImage(data []byte) (w, h int, rgba []byte, err bool) {
	if len(data) == 0 {
		return 0, 0, nil, true
	}
	// stb_image has no WebP support; route WebP to libwebp.
	if isWebP(data) {
		w, h, rgba, ok := decodeWebP(data)
		if !ok {
			return 0, 0, nil, true
		}
		return w, h, rgba, false
	}
	var cw, ch C.int
	p := C.stb_decode_rgba((*C.uchar)(unsafe.Pointer(&data[0])),
		C.int(len(data)), &cw, &ch)
	if p == nil {
		return 0, 0, nil, true
	}
	defer C.free(unsafe.Pointer(p))
	w, h = int(cw), int(ch)
	if w <= 0 || h <= 0 {
		return 0, 0, nil, true
	}
	rgba = C.GoBytes(unsafe.Pointer(p), C.int(w*h*4))
	return w, h, rgba, false
}

// isWebP reports whether the bytes are a RIFF/WEBP container
// ("RIFF" at offset 0, "WEBP" at offset 8).
func isWebP(b []byte) bool {
	return len(b) >= 12 &&
		b[0] == 'R' && b[1] == 'I' && b[2] == 'F' && b[3] == 'F' &&
		b[8] == 'W' && b[9] == 'E' && b[10] == 'B' && b[11] == 'P'
}
