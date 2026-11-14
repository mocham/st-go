package frontend

import (
	"fmt"
	"image"
	"slices"

	"st-go/platform_neutral/api"
	term "st-go/platform_neutral/stterminal"
)

const regionStatusBackground uint32 = 0xff25282e

// Status snapshots are immutable and painted alongside the retained preview.
type regionStatusSurface struct {
	items            []term.RegionStatusItem
	cw, ch, baseline int
}

func (s *regionStatusSurface) columns(width int) int {
	return max(1, (width-2*s.cw)/max(1, 22*s.cw))
}

func (s *regionStatusSurface) height(pane image.Rectangle) int {
	if s == nil || len(s.items) == 0 {
		return 0
	}
	rows := (len(s.items) + s.columns(pane.Dx()) - 1) / s.columns(pane.Dx())
	return min(rows*s.ch+4, max(0, pane.Dy()/2))
}

func (t *Terminal) SetRegionStatus(id term.RegionID, content term.RegionStatus) error {
	if err := content.Validate(); err != nil {
		return err
	}
	s := &t.displayRegions
	s.mu.Lock()
	if _, err := t.secondaryRegion(id); err != nil {
		s.mu.Unlock()
		return err
	}
	if s.status != nil && slices.Equal(s.status.items, content.Items) || s.status == nil && len(content.Items) == 0 {
		s.mu.Unlock()
		return nil
	}
	full := t.secondaryFullPanePixels()
	previous := s.status.height(full)
	s.status = nil
	if len(content.Items) > 0 {
		s.status = &regionStatusSurface{items: slices.Clone(content.Items), cw: t.cw, ch: t.ch, baseline: t.baseline}
	}
	changed := previous != s.status.height(full)
	s.paused = t.termCore.IsPaintPaused()
	s.secondary.dirty = true
	t.requestRegionPaint()
	s.mu.Unlock()
	if changed {
		t.applyDisplayLayout(t.physicalCols(), t.physicalRows())
	}
	return nil
}

func regionStatusScene(status *regionStatusSurface, width, height int) api.SceneUpdate {
	u := api.SceneUpdate{Reset: true, Background: regionStatusBackground}
	if status == nil || height < 1 {
		return u
	}
	columns := status.columns(width)
	rows := max(1, (height-4)/max(1, status.ch))
	capacity := rows * columns
	for i, item := range status.items {
		if i >= capacity {
			break
		}
		if i == capacity-1 && len(status.items) > capacity {
			item = term.RegionStatusItem{Text: fmt.Sprintf("+%d more slots", len(status.items)-i), Color: 0xffaaaaaa}
		}
		x, y := status.cw+(i%columns)*22*status.cw, 2+(i/columns)*status.ch
		if y >= height || x >= width {
			continue
		}
		// Semantic backends retain rows by Y, so every column at this height
		// belongs to the same row rather than replacing its preceding column.
		if i%columns == 0 {
			u.Rows = append(u.Rows, api.SceneRow{Y: y, Height: min(status.ch, height-y), Baseline: status.baseline, FontSize: status.ch, Background: regionStatusBackground})
		}
		row := &u.Rows[len(u.Rows)-1]
		end := min(x+21*status.cw, width)
		for _, r := range item.Text {
			advance := term.CellWidth(r) * status.cw
			if advance < 1 || x+advance > end {
				break
			}
			appendSceneText(row, api.TextRun{X: x, Text: string(r), Advance: advance, Foreground: item.Color, Background: regionStatusBackground})
			x += advance
		}
	}
	return u
}

func statusPixels(status *regionStatusSurface, width, height int) []uint32 {
	pixels := make([]uint32, width*height)
	(rasterScene{pixels: pixels, width: width, height: height, clip: image.Rect(0, 0, width, height), glyph: cachedGlyphPixels}).Apply(regionStatusScene(status, width, height))
	return pixels
}

func (p *regionPresenter) presentStatus(frame regionFrame) {
	if p.status == nil || frame.status == nil || frame.statusBounds.Empty() {
		return
	}
	w, h := frame.statusBounds.Dx(), frame.statusBounds.Dy()
	if semantic, ok := p.status.(api.SemanticSurface); ok && semantic.PresentScene(frame.presentation, regionStatusScene(frame.status, w, h)) {
		return
	}
	pixels := statusPixels(frame.status, w, h)
	if guarded, ok := p.status.(api.GuardedSurface); ok {
		guarded.PresentIfCurrent(frame.presentation, pixels, w, h, 0, false)
	} else {
		p.status.Present(pixels, w, h, 0, false)
	}
}
