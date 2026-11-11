package frontend

import (
	"image"
	"st-go/platform_neutral/api"
	term "st-go/platform_neutral/stterminal"
	"unicode/utf8"
)

func (t *Terminal) scene() api.SceneSurface {
	if host, ok := t.host.(api.ScenePainter); ok {
		return host.Scene()
	}
	return nil
}
func (t *Terminal) drawSceneLine(line []term.Glyph, first, y, last int) {
	scene := t.scene()
	if scene != nil {
		first, last = 0, min(t.cols, len(line))
	}
	row := api.SceneRow{X: t.borderpx + first*t.cw, Width: (last - first) * t.cw, Y: t.borderpx + y*t.ch, Height: t.ch, Baseline: t.baseline, Background: t.defaultBgARGB()}
	row.Text = t.sceneScratch.Text[:0]
	row.Media = t.sceneScratch.Media[:0]
	buffer := t.sceneText[:0]
	var pending api.TextRun
	end := 0
	flush := func() {
		if len(buffer) > 0 {
			pending.Text = string(buffer)
			row.Text = append(row.Text, pending)
			buffer = buffer[:0]
		}
	}
	for x := first; x < min(last, len(line)); x++ {
		g := line[x]
		if g.U == term.ImageRune {
			if scene != nil {
				t.sceneImageCell(&row, g, x, y)
			} else {
				row.Media = append(row.Media, api.MediaPlacement{ID: uint64(g.Fg) + 1, X: t.borderpx + x*t.cw, Y: t.borderpx + y*t.ch, Width: t.cw, Height: t.ch})
			}
			continue
		}
		if g.U == 0 || g.Mode&term.ATTRWdummy != 0 {
			continue
		}
		if t.termCore.Selected(x, y) {
			g.Mode ^= term.ATTRReverse
		}
		fg, bg := t.xglyphcolors(g)
		width := t.cw
		if g.Mode&term.ATTRWide != 0 {
			width *= 2
		}
		style := uint16(g.Mode & (term.ATTRBold | term.ATTRItalic | term.ATTRUnderline | term.ATTRStruck))
		run := api.TextRun{X: t.borderpx + x*t.cw, Cluster: g.Suffix != "", Advance: width, Foreground: fg, Background: bg, Style: style}
		if run.Cluster {
			flush()
			run.Text = g.Text()
			row.Text = append(row.Text, run)
			continue
		}
		if len(buffer) > 0 && (end != run.X || pending.Advance != run.Advance || pending.Foreground != fg || pending.Background != bg || pending.Style != style) {
			flush()
		}
		if len(buffer) == 0 {
			pending = run
		}
		buffer = utf8.AppendRune(buffer, g.U)
		end = run.X + run.Advance
	}
	flush()
	t.sceneScratch = row
	t.sceneText = buffer[:0]
	update := api.SceneUpdate{Rows: []api.SceneRow{row}}
	if scene != nil {
		scene.Apply(update)
		return
	}
	raster := rasterScene{pixels: t.framebuf, width: t.fbW, height: t.fbH, clip: t.terminalClip(), glyph: t.sceneGlyph, media: func(m api.MediaPlacement) {
		t.drawImageCell(term.Glyph{U: term.ImageRune, Fg: uint32(m.ID - 1)}, (m.X-t.borderpx)/t.cw, (m.Y-t.borderpx)/t.ch)
	}}
	raster.Apply(update)
}

// Runs share colors/metrics but retain explicit cell advances. No font shaping
// or browser measurement is allowed to change the terminal grid.
func appendSceneText(row *api.SceneRow, run api.TextRun) {
	if n := len(row.Text); n > 0 {
		last := &row.Text[n-1]
		if !last.Cluster && !run.Cluster && last.X+len([]rune(last.Text))*last.Advance == run.X && last.Advance == run.Advance && last.Foreground == run.Foreground && last.Background == run.Background && last.Style == run.Style {
			last.Text += run.Text
			return
		}
	}
	row.Text = append(row.Text, run)
}

func (t *Terminal) drawSceneCursor(cx, cy int, g term.Glyph) {
	c := api.SceneCursor{}
	if !t.winModeIs(term.ModeHide) {
		w := t.cw
		if g.Mode&term.ATTRWide != 0 {
			w *= 2
		}
		r := image.Rect(t.borderpx+cx*t.cw, t.borderpx+cy*t.ch, t.borderpx+cx*t.cw+w, t.borderpx+(cy+1)*t.ch).Intersect(t.terminalClip())
		c = api.SceneCursor{X: r.Min.X, Y: r.Min.Y, Width: r.Dx(), Height: r.Dy(), Shape: t.cursorShape, Thickness: t.cursorThick, Color: t.colorAt(ColDefaultCs), Outline: !t.winModeIs(term.ModeFocused)}
		glyph, color := t.cursorGlyph(cx, cy, g)
		c.Color = color
		c.Foreground = t.resolveFG(glyph)
		c.Baseline = t.baseline
		if g.U != term.ImageRune {
			c.Text = glyph.Text()
		}
		if t.cursorShape == 7 {
			c.Text = "☃"
		}
	}
	t.scene().Apply(api.SceneUpdate{Cursor: &c})
}
