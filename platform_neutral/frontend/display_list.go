package frontend

import (
	"fmt"
	"image"
	"math"

	"golang.org/x/text/unicode/norm"
	"st-go/platform_neutral/api"
	term "st-go/platform_neutral/stterminal"
)

const listBackground uint32 = 0xfff7f5ef
const listForeground uint32 = 0xff30343b

// Lists retain only semantic content and metrics. Renderer-owned glyph caches
// are populated during presentation, independently of the terminal input loop.
type regionListSurface struct {
	content          term.RegionList
	cw, ch, baseline int
}

func newRegionList(content term.RegionList, cw, ch, baseline int, previous *regionListSurface) (*regionListSurface, error) {
	if err := content.Validate(); err != nil {
		return nil, err
	}
	content.Items = append([]term.RegionListItem(nil), content.Items...)
	content.Title = norm.NFC.String(content.Title)
	list := &regionListSurface{content: content, cw: cw, ch: ch, baseline: baseline}
	for i := range content.Items {
		list.content.Items[i].Text = norm.NFC.String(content.Items[i].Text)
	}
	return list, nil
}

// ShowRegionList overlays the existing secondary surface, preserving the PDF
// bitmap and navigation for restore. No layout changes or PTY resizes occur.
func (t *Terminal) ShowRegionList(id term.RegionID, content *term.RegionList) error {
	s := &t.displayRegions
	s.mu.Lock()
	defer s.mu.Unlock()
	if t.termCore != nil {
		s.paused = t.termCore.IsPaintPaused()
	}
	surface, err := t.secondaryRegion(id)
	if err != nil {
		return err
	}
	if content == nil {
		if surface.list == nil {
			return nil
		}
		s.secondary = s.listPreview
		s.listPreview = nil
		s.listVisible = nil
		if s.secondary == nil {
			s.secondary = &regionSurface{}
		}
		s.secondary.dirty = true
		t.queueRegionImage()
		t.requestRegionPaint()
		return nil
	}
	list, err := newRegionList(*content, t.cw, t.ch, t.baseline, surface.list)
	if err != nil {
		return err
	}
	if surface.list == nil {
		s.listPreview = surface
		surface = &regionSurface{reportScroll: surface.reportScroll}
		s.secondary = surface
	} else if surface.list.content.Token != content.Token {
		surface.listSelected = 0
	}
	surface.list = list
	surface.dirty = true
	t.queueRegionImage()
	return nil
}

func (list *regionListSurface) headerHeight() int { return 3 * list.ch }
func (list *regionListSurface) rowHeight() int    { return list.ch + max(2, list.ch/3) }
func (list *regionListSurface) maximum(height int) float64 {
	return float64(max(0, len(list.content.Items)*list.rowHeight()-max(0, height-list.headerHeight())))
}

func (t *Terminal) listMousePress(button byte, px, py int) {
	s := &t.displayRegions
	surface, r := s.secondary, s.viewport
	list := surface.list
	if button == 4 || button == 5 {
		delta := 1
		if button == 4 {
			delta = -1
		}
		t.scrollRegionBy(delta)
		return
	}
	if button != 1 || !image.Pt(px, py).In(r) || py < r.Min.Y+list.headerHeight() {
		return
	}
	if s.listVisible != list || s.listVisibleEpoch != s.layoutEpoch {
		return
	}
	// Match the committed frame's rounded pixel origin exactly, including at
	// a row boundary during easing or while a newer X upload is still blocked.
	index := (py - r.Min.Y - list.headerHeight() + int(math.Round(s.listVisibleY))) / list.rowHeight()
	if index < 0 || index >= len(list.content.Items) {
		return
	}
	item := list.content.Items[index]
	surface.listSelected = item.ID
	surface.dirty = true
	t.requestRegionPaint()
	t.termCore.WriteToTTY([]byte(fmt.Sprintf("\x1bPregion secondary event activate %d %d\x1b\\", list.content.Token, item.ID)), false)
}

func (t *Terminal) noteRegionListFrame(frame regionFrame) {
	if host, ok := t.host.(api.WidgetPresentation); ok {
		generation, visible := host.PresentationState()
		if !visible || generation != frame.presentation {
			return
		}
	}
	if frame.surface.list == nil {
		return
	}
	s := &t.displayRegions
	s.mu.Lock()
	if s.secondary != nil && s.secondary.list == frame.surface.list && s.layoutEpoch == frame.epoch {
		s.listVisible, s.listVisibleY, s.listVisibleEpoch = frame.surface.list, frame.surface.scrollY, frame.epoch
	}
	s.mu.Unlock()
}

func renderRegionList(surface regionSurface, r, pane image.Rectangle, pixels []uint32, stride int) {
	update := regionListScene(regionFrame{surface: surface, pane: pane, viewport: r})
	// Headless composition can target a pane inside a larger framebuffer.
	for i := range update.Rows {
		row := &update.Rows[i]
		row.X += pane.Min.X
		row.Y += pane.Min.Y
		for j := range row.Text {
			row.Text[j].X += pane.Min.X
		}
	}
	for i := range update.Fills {
		update.Fills[i].X += pane.Min.X
		update.Fills[i].Y += pane.Min.Y
	}
	update.Reset = false
	for y := pane.Min.Y; y < pane.Max.Y; y++ {
		for x := pane.Min.X; x < pane.Max.X; x++ {
			pixels[y*stride+x] = listBackground
		}
	}
	(rasterScene{pixels: pixels, width: stride, height: len(pixels) / stride, clip: pane, glyph: cachedGlyphPixels}).Apply(update)
}
