package frontend

import (
	"encoding/json"
	"fmt"
	"st-go/limits"
	"st-go/pdfmeta"
	"st-go/platform_neutral/api"
)

func (t *Terminal) findSceneDocument(surface *regionSurface, text string, direction int, next bool) error {
	if len(text) > limits.PreviewSearchBytes || (direction != 1 && direction != -1) {
		return fmt.Errorf("invalid PDF search")
	}
	s := &t.displayRegions
	if s.documentCancel != nil {
		s.documentCancel()
	}
	s.documentQuery++
	surface.media.Search = text
	if text == "" {
		surface.search = nil
		surface.dirty = true
		t.requestRegionPaint()
		return nil
	}
	if next && surface.search != nil && surface.search.query == text {
		if n := len(surface.search.hits); n > 0 {
			surface.search.active = (surface.search.active + direction + n) % n
			t.revealSceneMatch(surface)
		}
		t.publishSearchReply(surface.search)
		return nil
	}
	seq, revision := s.documentQuery, s.documentRevision
	s.documentCancel = t.host.(api.DocumentHost).QueryMedia(surface.media.ID, api.DocumentQuery{Kind: "search", Text: text}, func(data []byte, err error) {
		s.mu.Lock()
		defer s.mu.Unlock()
		if t.closed() || seq != s.documentQuery || revision != s.documentRevision || s.secondary != surface {
			return
		}
		var result pdfmeta.Search
		if err != nil || json.Unmarshal(data, &result) != nil {
			return
		}
		matches := &pdfMatches{query: text, active: -1, truncated: result.Truncated}
		for _, page := range result.Pages {
			if page > 0 && page <= limits.PreviewPages && len(matches.hits) < pdfmeta.MaxEntries {
				matches.hits = append(matches.hits, pdfMatch{page: page - 1})
			}
		}
		matches.choose(surface.media.Page-1, 0, direction)
		surface.search = matches
		t.revealSceneMatch(surface)
		t.publishSearchReply(matches)
	})
	return nil
}
func (t *Terminal) revealSceneMatch(surface *regionSurface) {
	if m := surface.search; m != nil && m.active >= 0 && m.active < len(m.hits) {
		surface.media.Page = m.hits[m.active].page + 1
		surface.media.Position = 0
		surface.media.PositionID++
	}
	surface.dirty = true
	t.requestRegionPaint()
}
