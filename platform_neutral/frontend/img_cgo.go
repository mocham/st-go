package frontend

/*
#include <stdlib.h>
int stb_image_info(const unsigned char *data, int len, int *width, int *height);
unsigned char *stb_decode_rgba(const unsigned char *data, int len, int *width, int *height);
*/
import "C"

import (
	"unsafe"

	"st-go/limits"
)

// decodeImage decodes an encoded image (PNG/JPEG/GIF/BMP/WebP...) held in
// memory into unpremultiplied RGBA. Returns width, height and the RGBA pixels.
//
// The stb_decode_rgba symbol is resolved at link time from either the real
// stb_image.o or the no-op dummy-stb.o (see Makefile targets); a dummy makes
// this return an error so the caller just shows nothing.
func decodeImage(data []byte) (w, h int, rgba []byte, err bool) {
	if len(data) == 0 || !limits.Check("encoded image", int64(len(data)), limits.FileBytes) {
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
	if C.stb_image_info((*C.uchar)(unsafe.Pointer(&data[0])), C.int(len(data)), &cw, &ch) == 0 {
		return 0, 0, nil, true
	}
	if _, ok := limits.ImageRGBA("decoded image", int(cw), int(ch)); !ok {
		return 0, 0, nil, true
	}
	p := C.stb_decode_rgba((*C.uchar)(unsafe.Pointer(&data[0])),
		C.int(len(data)), &cw, &ch)
	if p == nil {
		return 0, 0, nil, true
	}
	defer C.free(unsafe.Pointer(p))
	w, h = int(cw), int(ch)
	size, ok := limits.ImageRGBA("decoded image", w, h)
	if !ok {
		return 0, 0, nil, true
	}
	rgba = C.GoBytes(unsafe.Pointer(p), C.int(size))
	return w, h, rgba, false
}

// isWebP reports whether the bytes are a RIFF/WEBP container
// ("RIFF" at offset 0, "WEBP" at offset 8).
func isWebP(b []byte) bool {
	return len(b) >= 12 &&
		b[0] == 'R' && b[1] == 'I' && b[2] == 'F' && b[3] == 'F' &&
		b[8] == 'W' && b[9] == 'E' && b[10] == 'B' && b[11] == 'P'
}
