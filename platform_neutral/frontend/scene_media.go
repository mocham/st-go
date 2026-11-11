package frontend

import (
	"path/filepath"
	"strings"

	"st-go/platform_neutral/api"
	term "st-go/platform_neutral/stterminal"
)

type sceneImage struct {
	resource         uint64
	cols, rows, page int
	fit              string
}

func (t *Terminal) OpenImageSource(path string, opts term.ImageDecodeOptions) (cols, rows int, glyphs []term.Glyph, handled bool) {
	if t.scene() == nil {
		return
	}
	switch strings.ToLower(filepath.Ext(path)) {
	case ".png", ".jpg", ".jpeg", ".gif", ".webp", ".bmp", ".tif", ".tiff", ".pdf":
	default:
		return // preserve the core's plain-text file insertion protocol
	}
	handled = true
	registry, ok := t.host.(api.MediaRegistry)
	if !ok {
		return
	}
	cols, rows = opts.ViewCols, opts.ViewRows
	if cols < 1 {
		cols = t.cols
	}
	if rows < 1 {
		rows = t.rows
	}
	resource := registry.RegisterMedia(api.MediaSource{Path: path})
	if resource == 0 {
		return 0, 0, nil, true
	}
	if t.sceneImages == nil {
		t.sceneImages = make(map[uint32]sceneImage)
	}
	// Old scrollback cells may outlive a resource. Like the native image atlas,
	// bound residency and leave evicted cells blank rather than reuse their IDs.
	if len(t.sceneImages) >= 16 {
		var oldest uint32 = ^uint32(0)
		for key := range t.sceneImages {
			oldest = min(oldest, key)
		}
		registry.ReleaseMedia(t.sceneImages[oldest].resource)
		delete(t.sceneImages, oldest)
	}
	t.nextSceneImage++
	id := t.nextSceneImage
	fit := "contain"
	if opts.FitWidth {
		fit = "width"
	}
	if opts.FitHeight {
		fit = "height"
	}
	t.sceneImages[id] = sceneImage{resource, cols, rows, opts.Page + 1, fit}
	for y := 0; y < rows; y++ {
		for x := 0; x < cols; x++ {
			glyphs = append(glyphs, term.Glyph{U: term.ImageRune, Fg: id, Bg: uint32(y<<16 | x)})
		}
	}
	return cols, rows, glyphs, true
}
func (t *Terminal) sceneImageCell(row *api.SceneRow, g term.Glyph, x, y int) {
	s, ok := t.sceneImages[g.Fg]
	if !ok {
		return
	}
	gx, gy := int(g.Bg&65535), int(g.Bg>>16)
	placement := api.MediaPlacement{ID: s.resource, X: t.borderpx + (x-gx)*t.cw, Y: t.borderpx + (y-gy)*t.ch, Width: s.cols * t.cw, Height: s.rows * t.ch, ClipX: t.borderpx + x*t.cw, ClipY: t.borderpx + y*t.ch, ClipWidth: t.cw, ClipHeight: t.ch, Page: s.page, Fit: s.fit}
	if n := len(row.Media); n > 0 {
		last := &row.Media[n-1]
		if last.ID == placement.ID && last.X == placement.X && last.Y == placement.Y && last.ClipY == placement.ClipY && last.ClipX+last.ClipWidth == placement.ClipX {
			last.ClipWidth += placement.ClipWidth
			return
		}
	}
	row.Media = append(row.Media, placement)
}
func (t *Terminal) releaseSceneImages() {
	if registry, ok := t.host.(api.MediaRegistry); ok {
		for _, s := range t.sceneImages {
			registry.ReleaseMedia(s.resource)
		}
	}
	clear(t.sceneImages)
}
