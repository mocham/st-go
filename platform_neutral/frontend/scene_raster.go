package frontend

import (
	"golang.org/x/text/unicode/norm"
	"image"
	"slices"
	"st-go/platform_neutral/api"
)

// rasterScene is the native adapter for the same semantic rows consumed by
// Canvas. It operates on renderer-owned working pixels; upload/scroll batching
// remains at the native presentation boundary. Glyph caches never enter rows.
type rasterScene struct {
	pixels        []uint32
	width, height int
	clip          image.Rectangle
	glyph         func(rune, uint32, uint32, int, int, int) []uint32
	media         func(api.MediaPlacement)
}

func (s rasterScene) Apply(update api.SceneUpdate) {
	if update.Reset {
		for i := range s.pixels {
			s.pixels[i] = update.Background
		}
	}
	fill := func(r image.Rectangle, color uint32) {
		r = r.Intersect(s.clip)
		for y := r.Min.Y; y < r.Max.Y; y++ {
			for x := r.Min.X; x < r.Max.X; x++ {
				s.pixels[y*s.width+x] = color
			}
		}
	}
	for _, row := range update.Rows {
		w := row.Width
		if w == 0 {
			w = s.width
		}
		band := image.Rect(row.X, row.Y, row.X+w, row.Y+row.Height).Intersect(s.clip)
		fill(band, row.Background)
		h := row.FontSize
		if h == 0 {
			h = row.Height
		}
		top := row.Y
		baseline := row.Baseline
		if h < row.Height {
			top += max(0, (row.Height-h)/2)
			baseline -= top - row.Y
		}
		for _, run := range row.Text {
			x := run.X
			for _, char := range run.Text {
				fill(image.Rect(x, row.Y, x+run.Advance, row.Y+row.Height).Intersect(band), run.Background)
				if char != ' ' {
					pixels := s.glyph(char, run.Foreground, run.Background, run.Advance, h, baseline)
					if run.Cluster {
						pixels = clusterPixels(run.Text, run.Foreground, run.Background, run.Advance, h, baseline, s.glyph)
					}
					box := image.Rect(x, top, x+run.Advance, top+h).Intersect(band)
					if len(pixels) == run.Advance*h {
						for y := box.Min.Y; y < box.Max.Y; y++ {
							at := (y-top)*run.Advance + box.Min.X - x
							copy(s.pixels[y*s.width+box.Min.X:y*s.width+box.Max.X], pixels[at:at+box.Dx()])
						}
					}
				}
				if run.Style&8 != 0 {
					fill(image.Rect(x, row.Y+row.Baseline+1, x+run.Advance, row.Y+row.Baseline+2).Intersect(band), run.Foreground)
				}
				if run.Style&128 != 0 {
					fill(image.Rect(x, top+h/2, x+run.Advance, top+h/2+1).Intersect(band), run.Foreground)
				}
				x += run.Advance
				if run.Cluster {
					break
				}
			}
		}
		if s.media != nil {
			for _, m := range row.Media {
				s.media(m)
			}
		}
	}
	for _, f := range update.Fills {
		fill(image.Rect(f.X, f.Y, f.X+f.Width, f.Y+f.Height), f.Color)
	}
}

func clusterPixels(text string, fg, bg uint32, w, h, baseline int, glyph func(rune, uint32, uint32, int, int, int) []uint32) []uint32 {
	var out []uint32
	for _, char := range norm.NFC.String(text) {
		pixels := glyph(char, fg, bg, w, h, baseline)
		if out == nil {
			out = slices.Clone(pixels)
			continue
		}
		for i, pixel := range pixels {
			if i < len(out) && pixel != bg {
				out[i] = pixel
			}
		}
	}
	return out
}

func (t *Terminal) sceneGlyph(char rune, fg, bg uint32, w, h, baseline int) []uint32 {
	return cachedGlyphPixels(char, fg, bg, w, h, baseline)
}
