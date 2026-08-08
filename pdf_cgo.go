// Package main bridges st-go to poppler for PDF rendering (C++ API).
//
// The pdf_render_page / pdf_page_count symbols are defined by the C++ bridge
// third_party_wrapper/pdf_bridge.cpp (linked when poppler is enabled) or by
// the no-op dummy-pdf.o (see Makefile targets); a dummy makes the PDF path
// fail gracefully (no image) instead of crashing.
package main

/*
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
