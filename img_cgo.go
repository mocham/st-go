package main

/*
#cgo CFLAGS: -Ithird_party/stb
#cgo LDFLAGS: third_party/stb/stb_image.o -lm
#include <stdlib.h>
unsigned char *stb_decode_rgba(const unsigned char *data, int len, int *width, int *height);
*/
import "C"

import (
	"unsafe"
)

// decodeImage decodes an encoded image (PNG/JPEG/GIF/BMP...) held in memory
// into unpremultiplied RGBA. Returns width, height and the RGBA pixels.
func decodeImage(data []byte) (w, h int, rgba []byte, err bool) {
	if len(data) == 0 {
		return 0, 0, nil, true
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
