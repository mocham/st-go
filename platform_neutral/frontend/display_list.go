package frontend

import (
	"fmt"
	"image"
	"math"
	"strings"

	"golang.org/x/text/unicode/norm"
	"st-go/limits"
	term "st-go/platform_neutral/stterminal"
)

const listBackground uint32 = 0xfff7f5ef
const listForeground uint32 = 0xff30343b

// Glyph pixels and content are immutable after publication. The preview painter
// can composite them without touching fonts, terminal cells, or Terminal.mu.
type regionListSurface struct {
	content          term.RegionList
	glyphs           map[rune]listGlyph
	cw, ch, baseline int
}
type listGlyph struct {
	pixels []uint32
	width  int
}

func newRegionList(content term.RegionList, cw, ch, baseline int, previous *regionListSurface) (*regionListSurface, error) {
	if err := content.Validate(); err != nil {
		return nil, err
	}
	content.Items = append([]term.RegionListItem(nil), content.Items...)
	content.Title = norm.NFC.String(content.Title)
	list := &regionListSurface{content: content, glyphs: make(map[rune]listGlyph), cw: cw, ch: ch, baseline: baseline}
	texts := []string{content.Title, "No headings or labels", "Shift-Tab: preview", "…"}
	for i := range content.Items {
		list.content.Items[i].Text = norm.NFC.String(content.Items[i].Text)
		texts = append(texts, list.content.Items[i].Text)
	}
	bytes := 0
	for _, text := range texts {
		for _, r := range text {
			if _, ok := list.glyphs[r]; ok {
				continue
			}
			width := term.CellWidth(r) * cw
			if width <= 0 {
				continue
			}
			if len(list.glyphs) >= limits.GlyphCacheEntries || int64(width)*int64(ch)*4 > int64(limits.GlyphCacheBytes-bytes) {
				return nil, fmt.Errorf("region list glyphs exceed resource limit")
			}
			bytes += width * ch * 4
			if previous != nil && previous.cw == cw && previous.ch == ch && previous.baseline == baseline {
				if glyph, ok := previous.glyphs[r]; ok {
					list.glyphs[r] = glyph
					continue
				}
			}
			_, pixels := makeGlyph(r, listForeground, listBackground, width, ch, baseline)
			list.glyphs[r] = listGlyph{pixels, width}
		}
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
	list := surface.list
	fill := func(rect image.Rectangle, color uint32) {
		rect = rect.Intersect(pane)
		for y := rect.Min.Y; y < rect.Max.Y; y++ {
			for x := rect.Min.X; x < rect.Max.X; x++ {
				pixels[y*stride+x] = color
			}
		}
	}
	fill(pane, listBackground)
	fill(image.Rect(pane.Min.X, pane.Min.Y, r.Min.X, pane.Max.Y), 0xff777777)
	drawText := func(text string, x, y int, clip image.Rectangle) {
		for _, char := range text {
			glyph, ok := list.glyphs[char]
			if !ok {
				continue
			}
			if x+glyph.width > clip.Max.X {
				break
			}
			box := image.Rect(x, y, x+glyph.width, y+list.ch).Intersect(clip)
			for py := box.Min.Y; py < box.Max.Y; py++ {
				first := (py-y)*glyph.width + box.Min.X - x
				if first+box.Dx() <= len(glyph.pixels) {
					copy(pixels[py*stride+box.Min.X:py*stride+box.Max.X], glyph.pixels[first:first+box.Dx()])
				}
			}
			x += glyph.width
		}
	}
	drawText(list.content.Title, r.Min.X+list.cw, r.Min.Y+list.ch/3, r)
	drawText("Shift-Tab: preview", r.Min.X+list.cw, r.Min.Y+list.ch+list.ch/2, r)
	body := r
	body.Min.Y = min(r.Max.Y, r.Min.Y+list.headerHeight())
	fill(image.Rect(r.Min.X, body.Min.Y-1, r.Max.X, body.Min.Y), 0xffd0cdc5)
	rowHeight := list.rowHeight()
	first := int(surface.scrollY) / rowHeight
	if len(list.content.Items) == 0 {
		drawText("No headings or labels", body.Min.X+list.cw, body.Min.Y+list.ch/3, body)
	}
	for i := first; i < len(list.content.Items); i++ {
		y := body.Min.Y + i*rowHeight - int(math.Round(surface.scrollY))
		if y >= body.Max.Y {
			break
		}
		item := list.content.Items[i]
		if item.ID == surface.listSelected {
			fill(image.Rect(body.Min.X, y, body.Min.X+max(2, list.cw/3), y+rowHeight).Intersect(body), 0xff668db0)
		}
		indent := min(2*item.Level, max(0, body.Dx()/max(1, list.cw)-14))
		x := body.Min.X + (1+indent)*list.cw
		clip := body
		clip.Max.X = max(body.Min.X, body.Max.X-4)
		text := item.Text
		columns := max(0, (clip.Max.X-x)/max(1, list.cw))
		var visible strings.Builder
		used := 0
		for _, char := range text {
			w := term.CellWidth(char)
			if used+w > columns {
				break
			}
			visible.WriteRune(char)
			used += w
		}
		drawText(visible.String(), x, y+(rowHeight-list.ch)/2, clip)
	}
	if maximum := list.maximum(r.Dy()); maximum > 0 && body.Dy() > 0 {
		height := max(6, int(float64(body.Dy()*body.Dy())/(maximum+float64(body.Dy()))))
		height = min(height, body.Dy())
		top := body.Min.Y + int(surface.scrollY/maximum*float64(body.Dy()-height))
		fill(image.Rect(body.Max.X-3, top, body.Max.X, top+height), 0xff8e9399)
	}
}
