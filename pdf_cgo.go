// Package main bridges st-go to a minimal static poppler build (C++ API).
//
// The C++ page_renderer::render_page() returns a raw BGRA image with no
// cairo/glib dependency, so the static link only needs poppler + freetype
// (already in third_party/) + zlib. That drops the glib/gobject/ffi/cairo/
// pixman/lcms/openjpeg/turbojpeg chain that poppler's glib API requires.
package main

/*
#cgo CXXFLAGS: -std=c++11 -Ithird_party/poppler/include -Ithird_party/poppler/include/poppler -I.
#cgo LDFLAGS: -Lthird_party/poppler/lib -lpoppler-cpp -lpoppler -Lthird_party/freetype -lfreetype -Lthird_party/poppler/lib -lpng16 -lz -lstdc++ -lm
#include <stdlib.h>
#include "pdf_bridge.h"
*/
import "C"

import (
	"unsafe"
)

// renderPDFPage renders page `page` (0-based) of the PDF in `data` to a BGRA
// buffer scaled to fit outW x outH. Returns the buffer and its row stride.
func renderPDFPage(data []byte, page, outW, outH int) ([]byte, int, bool) {
	if len(data) == 0 {
		return nil, 0, false
	}
	cdata := C.CBytes(data)
	defer C.free(cdata)

	out := make([]byte, outW*outH*4)
	ok := C.pdf_render_page(
		(*C.uchar)(cdata),
		C.int(len(data)),
		C.int(page),
		C.int(outW),
		C.int(outH),
		(*C.uchar)(unsafe.Pointer(&out[0])),
	) != 0
	if !ok {
		return nil, 0, false
	}
	return out, outW * 4, true
}

// pdfPageCount returns the number of pages in a PDF, or 0 on failure.
func pdfPageCount(data []byte) int {
	if len(data) == 0 {
		return 0
	}
	cdata := C.CBytes(data)
	defer C.free(cdata)
	return int(C.pdf_page_count((*C.uchar)(cdata), C.int(len(data))))
}
