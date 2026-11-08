package main

import (
	"fmt"
	"image"
	"math"
)

func (m *pdfMatches) choose(page, y, direction int) {
	if len(m.hits) == 0 {
		m.active = -1
		return
	}
	if direction > 0 {
		m.active = 0
		for i, hit := range m.hits {
			if hit.page > page || (hit.page == page && hit.box.Min.Y >= y) {
				m.active = i
				break
			}
		}
	} else {
		m.active = len(m.hits) - 1
		for i := len(m.hits) - 1; i >= 0; i-- {
			hit := m.hits[i]
			if hit.page < page || (hit.page == page && hit.box.Min.Y <= y) {
				m.active = i
				break
			}
		}
	}
}

func (m *pdfMatches) destination(tops []int, height int) (int, int) {
	if m == nil || m.active < 0 || m.active >= len(m.hits) {
		return 0, 0
	}
	hit := m.hits[m.active]
	if hit.page+1 >= len(tops) {
		return 0, 0
	}
	y := tops[hit.page] + int(int64(tops[hit.page+1]-tops[hit.page])*int64(hit.box.Min.Y)/1e6)
	// Context above the occurrence, without changing the topmost page merely
	// because its first match is near the page's top edge.
	y = max(tops[hit.page], y-height/4)
	return hit.page + 1, min(y, max(0, tops[len(tops)-1]-height))
}

func (t *Terminal) publishSearchReply(matches *pdfMatches) {
	page, index, total, local, truncated := 0, 0, 0, 0, 0
	if matches != nil {
		total = len(matches.hits)
		if matches.truncated {
			truncated = 1
		}
		if matches.active >= 0 && matches.active < total {
			page, index = matches.hits[matches.active].page+1, matches.active+1
			for i := matches.active; i >= 0 && matches.hits[i].page == page-1; i-- {
				local++
			}
		}
	}
	// Retain the original reply for clients that only understand page numbers.
	t.queueRegionReply(regionReply{t.displayRegions.generation, fmt.Sprintf("\x1bPregion secondary found %d\x1b\\\x1bPregion secondary matches %d %d %d %d %d\x1b\\", page, page, index, total, local, truncated)})
}

func (t *Terminal) revealSearchMatch() {
	s := t.displayRegions.secondary
	s.searchAnchored = true
	r := t.displayRegions.viewport
	width := s.renderWidth(r.Dx())
	// The previous bitmap may still be at the old zoom while resize decodes.
	tops := make([]int, len(s.bitmap.pageTops))
	for i, top := range s.bitmap.pageTops {
		tops[i] = int(int64(top) * int64(width) / int64(max(1, s.bitmap.width)))
	}
	_, y := s.search.destination(tops, r.Dy())
	s.scrollY, s.targetY = float64(y), float64(y)
	if s.search.active >= 0 {
		x := int(int64(s.search.hits[s.search.active].box.Min.X) * int64(width) / 1e6)
		s.pan = clampInt(x-r.Dx()/4, 0, max(0, width-r.Dx()))
	}
	s.dirty = true
	t.queueRegionScroll()
	t.requestRegionPaint()
}

func paintSearchMatches(surface regionSurface, viewport image.Rectangle, pixels []uint32, stride int) {
	m, b := surface.search, surface.bitmap
	if m == nil || len(b.pageTops) < 2 || b.contentHeight == 0 {
		return
	}
	width := surface.renderWidth(viewport.Dx())
	scale := float64(width) / float64(max(1, b.width))
	x0 := viewport.Min.X - surface.pan + max(0, (viewport.Dx()-width)/2)
	for i, hit := range m.hits {
		if hit.page+1 >= len(b.pageTops) {
			continue
		}
		pageTop := float64(b.pageTops[hit.page]) * scale
		h := float64(b.pageTops[hit.page+1]-b.pageTops[hit.page]) * scale
		box := image.Rect(x0+int(math.Floor(float64(hit.box.Min.X)*float64(width)/1e6)),
			viewport.Min.Y+int(math.Floor(pageTop+float64(hit.box.Min.Y)*h/1e6-surface.scrollY)),
			x0+int(math.Ceil(float64(hit.box.Max.X)*float64(width)/1e6)),
			viewport.Min.Y+int(math.Ceil(pageTop+float64(hit.box.Max.Y)*h/1e6-surface.scrollY))).Intersect(viewport)
		for y := box.Min.Y; y < box.Max.Y; y++ {
			for x := box.Min.X; x < box.Max.X; x++ {
				pixel := pixels[y*stride+x]
				// Multiplicative yellow/orange tint preserves black text and never
				// modifies cached document pixels. The active occurrence is darker.
				green, blue := uint32(235), uint32(100)
				if i == m.active {
					green, blue = 170, 45
				}
				pixels[y*stride+x] = 0xff000000 | pixel&0xff0000 | (((pixel>>8)&255)*green/255)<<8 | (pixel&255)*blue/255
			}
		}
	}
}
