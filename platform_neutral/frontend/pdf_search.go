//go:build !backend_web

package frontend

/*
#include <stdlib.h>
#include "pdf_bridge.h"
*/
import "C"

import (
	"fmt"
	"image"
	"math"
	"unsafe"

	"st-go/limits"
)

// Rectangles are immutable, page-relative millionths, independent of zoom.
type pdfMatch struct {
	page int // zero-based
	box  image.Rectangle
}

type pdfMatches struct {
	query     string
	hits      []pdfMatch
	active    int
	truncated bool
}

type pdfPageSize struct{ width, height float64 }

func (p *scrollPDF) pageSize(page int) (pdfPageSize, error) {
	var width, height C.double
	if C.pdf_scroll_page_size(p.handle, C.int(page), &width, &height) == 0 {
		return pdfPageSize{}, fmt.Errorf("invalid PDF page size")
	}
	w, h := float64(width), float64(height)
	if math.IsNaN(w) || math.IsNaN(h) || math.IsInf(w, 0) || math.IsInf(h, 0) || w < 1 || h < 1 {
		return pdfPageSize{}, fmt.Errorf("invalid PDF page size")
	}
	return pdfPageSize{w, h}, nil
}

func (p *scrollPDF) matches(width int, query string, current func() bool) (*pdfMatches, []int, error) {
	if C.pdf_scroll_height(p.handle, C.int(width), C.int(limits.PreviewHeight)) < 1 {
		return nil, nil, fmt.Errorf("invalid PDF search geometry")
	}
	tops := make([]C.int, limits.PreviewPages+1)
	count := int(C.pdf_scroll_page_tops(p.handle, &tops[0], C.int(len(tops))))
	pageTops := make([]int, count)
	for i := range pageTops {
		pageTops[i] = int(tops[i])
	}
	result := &pdfMatches{query: query, active: -1}
	text := C.CString(query)
	defer C.free(unsafe.Pointer(text))
	rects := make([]C.double, 4*(limits.PreviewSearchMatches+1))
	for page := 0; page < count-1; page++ {
		if !current() {
			return nil, nil, nil
		}
		var truncated C.int
		capacity := limits.PreviewSearchMatches - len(result.hits) + 1
		n := int(C.pdf_scroll_search_rects(p.handle, C.int(page), text, &rects[0], C.int(capacity), &truncated))
		if n < 0 || n > capacity {
			return nil, nil, fmt.Errorf("cannot search PDF page %d", page+1)
		}
		for i := 0; i < n; i++ {
			if len(result.hits) == limits.PreviewSearchMatches {
				result.truncated = true
				break
			}
			var box [4]int
			for j := range box {
				v := float64(rects[4*i+j])
				if math.IsNaN(v) || math.IsInf(v, 0) {
					return nil, nil, fmt.Errorf("invalid PDF search rectangle")
				}
				box[j] = int(math.Round(max(0, min(1, v)) * 1e6))
			}
			if box[2] > box[0] && box[3] > box[1] {
				result.hits = append(result.hits, pdfMatch{page, image.Rect(box[0], box[1], box[2], box[3])})
			}
		}
		result.truncated = result.truncated || truncated != 0
		if result.truncated {
			break
		}
	}
	return result, pageTops, nil
}
