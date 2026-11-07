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
	"fmt"
	"unsafe"

	"st-go/limits"
)

// scrollPDF's native document is confined to the region decoder goroutine.
type scrollPDF struct{ handle unsafe.Pointer }

func openScrollPDF(data []byte) (*scrollPDF, error) {
	if len(data) == 0 || !limits.Check("continuous PDF", int64(len(data)), limits.FileBytes) {
		return nil, fmt.Errorf("continuous PDF exceeds input limit or is empty")
	}
	input := C.CBytes(data)
	defer C.free(input)
	handle := C.pdf_scroll_open((*C.uchar)(input), C.int(len(data)), C.int(limits.PreviewPages))
	if handle == nil {
		return nil, fmt.Errorf("cannot open continuous PDF (page limit %d)", limits.PreviewPages)
	}
	return &scrollPDF{handle}, nil
}

func (p *scrollPDF) close() {
	if p != nil && p.handle != nil {
		C.pdf_scroll_close(p.handle)
		p.handle = nil
	}
}

func (p *scrollPDF) render(width, height, position int) (regionBitmap, error) {
	if _, ok := limits.RGBA("continuous PDF viewport", width, height); !ok {
		return regionBitmap{}, fmt.Errorf("continuous PDF viewport exceeds bitmap budget")
	}
	total := int(C.pdf_scroll_height(p.handle, C.int(width), C.int(limits.PreviewHeight)))
	if total < 1 {
		return regionBitmap{}, fmt.Errorf("continuous PDF dimensions exceed limits")
	}
	// Retain several screenfuls so ordinary wheel motion only copies cached
	// pixels. The cache size is independent of document length.
	rows := min(total, 3*height, limits.PreviewCacheBytes/4/max(1, width))
	if rows < min(total, height) {
		return regionBitmap{}, fmt.Errorf("continuous PDF viewport exceeds cache budget")
	}
	top := clampInt(position-max(0, (rows-height)/2), 0, total-rows)
	size, ok := limits.RGBA("continuous PDF strip", width, rows)
	if !ok {
		return regionBitmap{}, fmt.Errorf("continuous PDF strip exceeds bitmap budget")
	}
	// C writes native BGRA, which is the framebuffer's uint32 representation.
	pixels := make([]uint32, size/4)
	if C.pdf_scroll_render(p.handle, C.int(width), C.int(top), C.int(rows), (*C.uchar)(unsafe.Pointer(&pixels[0]))) != 1 {
		return regionBitmap{}, fmt.Errorf("cannot render continuous PDF strip")
	}
	return regionBitmap{pixels: pixels, width: width, height: rows, top: top, contentHeight: total}, nil
}

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
