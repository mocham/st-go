//go:build fastimage && !backend_web && cgo && !purego

package frontend

/*
#include <stddef.h>
int st_go_decode_png_rgba(const unsigned char *, size_t, unsigned char *, size_t, int, int);
int st_go_decode_jpeg_rgba(const unsigned char *, size_t, unsigned char *, size_t, int, int);
*/
import "C"

import "unsafe"

func decodePreferredImage(data []byte, format string, width, height, size int) ([]byte, bool) {
	if len(data) == 0 || size <= 0 {
		return nil, false
	}
	if format != "png" && format != "jpeg" {
		return nil, false
	}
	output := make([]byte, size)
	inputPtr := (*C.uchar)(unsafe.Pointer(&data[0]))
	outputPtr := (*C.uchar)(unsafe.Pointer(&output[0]))
	var decoded C.int
	switch format {
	case "png":
		decoded = C.st_go_decode_png_rgba(inputPtr, C.size_t(len(data)), outputPtr, C.size_t(size), C.int(width), C.int(height))
	case "jpeg":
		decoded = C.st_go_decode_jpeg_rgba(inputPtr, C.size_t(len(data)), outputPtr, C.size_t(size), C.int(width), C.int(height))
	}
	if decoded != 1 {
		return nil, false
	}
	return output, true
}
