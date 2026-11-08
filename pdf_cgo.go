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
type scrollPDF struct {
	handle       unsafe.Pointer
	cache        regionBitmap // worker-owned; published pixels are immutable
	renderedRows uint64       // native work, useful for cache regression measurements
}

func openScrollPDF(data []byte) (*scrollPDF, error) {
	if len(data) == 0 || !limits.Check("continuous PDF", int64(len(data)), limits.FileBytes) {
		return nil, fmt.Errorf("continuous PDF exceeds input limit or is empty")
	}
	preparePDFFonts()
	input := C.CBytes(data)
	defer C.free(input)
	handle := C.pdf_scroll_open((*C.uchar)(input), C.int(len(data)), C.int(limits.PreviewPages))
	if handle == nil {
		return nil, fmt.Errorf("cannot open continuous PDF (page limit %d)", limits.PreviewPages)
	}
	return &scrollPDF{handle: handle}, nil
}

func (p *scrollPDF) close() {
	if p != nil && p.handle != nil {
		C.pdf_scroll_close(p.handle)
		p.handle = nil
		p.cache = regionBitmap{}
	}
}

func (p *scrollPDF) render(width, height, position int, background uint32) (regionBitmap, error) {
	return p.renderScreens(width, height, position, background, limits.PreviewScreens)
}

func (p *scrollPDF) renderScreens(width, height, position int, background uint32, screens int) (regionBitmap, error) {
	if _, ok := limits.Product("continuous PDF viewport", limits.PreviewCacheBytes, width, height, 4); !ok {
		return regionBitmap{}, fmt.Errorf("continuous PDF viewport exceeds bitmap budget")
	}
	total := int(C.pdf_scroll_height(p.handle, C.int(width), C.int(limits.PreviewHeight)))
	if total < 1 {
		return regionBitmap{}, fmt.Errorf("continuous PDF dimensions exceed limits")
	}
	// Retain several screenfuls so ordinary wheel motion only copies cached
	// pixels. The cache size is independent of document length.
	rows := min(total, screens*height, limits.PreviewCacheBytes/4/width)
	if rows < min(total, height) {
		return regionBitmap{}, fmt.Errorf("continuous PDF viewport exceeds cache budget")
	}
	top := clampInt(position-max(0, (rows-height)/2), 0, total-rows)
	// C writes native BGRA, which is the framebuffer's uint32 representation.
	old := p.cache
	if old.width == width && old.background == background && old.top == top && old.height == rows {
		return old, nil
	}
	pixels := make([]uint32, width*rows)
	start, end := top, top
	if old.width == width && old.background == background {
		start, end = max(top, old.top), min(top+rows, old.top+old.height)
		if start < end {
			copy(pixels[(start-top)*width:(end-top)*width], old.pixels[(start-old.top)*width:(end-old.top)*width])
		} else {
			start, end = top, top
		}
	}
	for _, span := range [][2]int{{top, start}, {end, top + rows}} {
		if span[0] == span[1] {
			continue
		}
		out := pixels[(span[0]-top)*width:]
		if C.pdf_scroll_render(p.handle, C.int(width), C.int(span[0]), C.int(span[1]-span[0]), (*C.uchar)(unsafe.Pointer(&out[0])), C.uint(background)) != 1 {
			return regionBitmap{}, fmt.Errorf("cannot render continuous PDF strip")
		}
		p.renderedRows += uint64(span[1] - span[0])
	}
	tops := make([]C.int, limits.PreviewPages+1)
	count := int(C.pdf_scroll_page_tops(p.handle, &tops[0], C.int(len(tops))))
	pageTops := make([]int, count)
	for i := range pageTops {
		pageTops[i] = int(tops[i])
	}
	p.cache = regionBitmap{pixels: pixels, width: width, height: rows, top: top, contentHeight: total, background: background, pageTops: pageTops}
	return p.cache, nil
}

func (p *scrollPDF) find(width int, search *regionSearch, current func() bool) (int, int, error) {
	if C.pdf_scroll_height(p.handle, C.int(width), C.int(limits.PreviewHeight)) < 1 {
		return 0, 0, fmt.Errorf("invalid PDF search geometry")
	}
	tops := make([]C.int, limits.PreviewPages+1)
	pages := int(C.pdf_scroll_page_tops(p.handle, &tops[0], C.int(len(tops)))) - 1
	text := C.CString(search.text)
	defer C.free(unsafe.Pointer(text))
	for i := 0; i < pages; i++ {
		if !current() {
			return 0, 0, nil
		}
		page := ((search.page+i*search.direction)%pages + pages) % pages
		at := int(C.pdf_scroll_search_page(p.handle, C.int(page), text))
		if at > 0 {
			top, height := int(tops[page]), int(tops[page+1]-tops[page])
			return page + 1, top + int(int64(height)*int64(at-1)/1000000), nil
		}
	}
	return 0, 0, nil
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
	preparePDFFonts()
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
	preparePDFFonts()
	cdata := C.CBytes(data)
	defer C.free(cdata)
	count := int(C.pdf_page_count((*C.uchar)(cdata), C.int(len(data))))
	if count < 0 {
		limits.Report("PDF", "native allocation/decoder exception while reading pages")
		return 0
	}
	return count
}
