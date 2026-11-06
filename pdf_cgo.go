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

	"st-go/limits"
)

// renderPDFPage renders page `page` (0-based) of the PDF in `data` to a BGRA
// buffer scaled to fit outW x outH. Returns the buffer and its row stride.
func renderPDFPage(data []byte, page, outW, outH int) ([]byte, int, bool) {
	return renderPDFPageViewport(data, page, outW, outH, false)
}

func renderPDFPageViewport(data []byte, page, outW, outH int, fitWidth bool) ([]byte, int, bool) {
	if len(data) == 0 || !limits.Check("encoded PDF", int64(len(data)), limits.FileBytes) {
		return nil, 0, false
	}
	size, valid := limits.RGBA("PDF bitmap", outW, outH)
	if !valid {
		return nil, 0, false
	}
	cdata := C.CBytes(data)
	defer C.free(cdata)

	out := make([]byte, size)
	var fit C.int
	if fitWidth {
		fit = 1
	}
	result := C.pdf_render_page(
		(*C.uchar)(cdata),
		C.int(len(data)),
		C.int(page),
		C.int(outW),
		C.int(outH),
		(*C.uchar)(unsafe.Pointer(&out[0])),
		fit,
	)
	if result < 0 {
		limits.Report("PDF", "native allocation/decoder exception while rendering")
	}
	if result <= 0 {
		return nil, 0, false
	}
	return out, outW * 4, true
}

// pdfPageCount returns the number of pages in a PDF, or 0 on failure.
func pdfPageCount(data []byte) int {
	if len(data) == 0 || !limits.Check("encoded PDF", int64(len(data)), limits.FileBytes) {
		return 0
	}
	cdata := C.CBytes(data)
	defer C.free(cdata)
	count := int(C.pdf_page_count((*C.uchar)(cdata), C.int(len(data))))
	if count < 0 {
		limits.Report("PDF", "native allocation/decoder exception while reading pages")
		return 0
	}
	return count
}
