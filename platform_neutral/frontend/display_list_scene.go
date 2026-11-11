package frontend

import (
	"image"
	"math"
	"st-go/platform_neutral/api"
	term "st-go/platform_neutral/stterminal"
)

func regionListScene(frame regionFrame) api.SceneUpdate {
	l := frame.surface.list
	r := frame.viewport.Sub(frame.pane.Min)
	u := api.SceneUpdate{Reset: true, Background: listBackground}
	fill := func(rect image.Rectangle, color uint32) {
		rect = rect.Intersect(image.Rect(0, 0, frame.pane.Dx(), frame.pane.Dy()))
		if !rect.Empty() {
			u.Fills = append(u.Fills, api.SceneFill{X: rect.Min.X, Y: rect.Min.Y, Width: rect.Dx(), Height: rect.Dy(), Color: color})
		}
	}
	fill(image.Rect(0, 0, r.Min.X, frame.pane.Dy()), 0xff777777)
	text := func(value string, x, y int, clip image.Rectangle) {
		band := image.Rect(0, y, frame.pane.Dx(), y+l.ch).Intersect(clip)
		if band.Empty() {
			return
		}
		row := api.SceneRow{Y: band.Min.Y, Height: band.Dy(), Baseline: y + l.baseline - band.Min.Y, FontSize: l.ch, Background: listBackground}
		for _, char := range value {
			advance := term.CellWidth(char) * l.cw
			if advance <= 0 {
				continue
			}
			if x+advance > clip.Max.X {
				break
			}
			appendSceneText(&row, api.TextRun{X: x, Text: string(char), Advance: advance, Foreground: listForeground, Background: listBackground})
			x += advance
		}
		u.Rows = append(u.Rows, row)
	}
	text(l.content.Title, r.Min.X+l.cw, r.Min.Y+l.ch/3, r)
	text("Shift-Tab: preview", r.Min.X+l.cw, r.Min.Y+l.ch+l.ch/2, r)
	body := r
	body.Min.Y = min(r.Max.Y, r.Min.Y+l.headerHeight())
	fill(image.Rect(r.Min.X, body.Min.Y-1, r.Max.X, body.Min.Y), 0xffd0cdc5)
	if len(l.content.Items) == 0 {
		text("No headings or labels", body.Min.X+l.cw, body.Min.Y+l.ch/3, body)
	}
	for i := max(0, int(frame.surface.scrollY)/l.rowHeight()); i < len(l.content.Items); i++ {
		y := body.Min.Y + i*l.rowHeight() - int(math.Round(frame.surface.scrollY))
		if y >= body.Max.Y {
			break
		}
		item := l.content.Items[i]
		indent := min(2*item.Level, max(0, body.Dx()/max(1, l.cw)-14))
		if item.ID == frame.surface.listSelected {
			fill(image.Rect(body.Min.X, y, body.Min.X+max(2, l.cw/3), y+l.rowHeight()).Intersect(body), 0xff668db0)
		}
		text(item.Text, body.Min.X+(1+indent)*l.cw, y+(l.rowHeight()-l.ch)/2, body)
	}
	if maximum := l.maximum(r.Dy()); maximum > 0 && body.Dy() > 0 {
		height := min(body.Dy(), max(6, int(float64(body.Dy()*body.Dy())/(maximum+float64(body.Dy())))))
		top := body.Min.Y + int(frame.surface.scrollY/maximum*float64(body.Dy()-height))
		fill(image.Rect(body.Max.X-3, top, body.Max.X, top+height), 0xff8e9399)
	}
	return u
}
