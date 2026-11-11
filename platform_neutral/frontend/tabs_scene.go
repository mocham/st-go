//go:build terminal_tabs

package frontend

import (
	"st-go/platform_neutral/api"
	term "st-go/platform_neutral/stterminal"
	"strconv"
)

func (m *terminalTabs) paintSceneBar(scene api.SceneTarget) {
	if m.bar == nil || !m.bar.Visible() {
		return
	}
	r := m.bar.Bounds()
	cell := max(1, m.renderer.cw)
	available := r.Dx() / cell
	m.hits = nil
	row := api.SceneRow{Y: 0, Height: r.Dy(), FontSize: m.renderer.ch, Baseline: max(0, (r.Dy()-m.renderer.ch)/2) + m.renderer.baseline, Background: 0xff2d2b46}
	shown := min(len(m.entries), max(1, available/terminalTabWidthCells))
	start := max(0, m.active-shown+1)
	x := 0
	for i := start; i < min(len(m.entries), start+shown); i++ {
		bg, fg := uint32(0xff2d2b46), uint32(0xffc4dfb5)
		if i == m.active {
			bg, fg = 0xff414967, 0xffffffff
		}
		end := min(r.Dx(), x+terminalTabWidthCells*cell)
		label := tabLabel(strconv.Itoa(i+1)+" "+m.entries[i].title, max(0, (end-x)/cell-1))
		// Pad the complete clickable tab so its background is independent of title length.
		at := x
		for _, char := range label {
			advance := max(0, term.CellWidth(char)) * cell
			if advance == 0 {
				continue
			}
			if at+advance > end {
				break
			}
			appendSceneText(&row, api.TextRun{X: at, Text: string(char), Advance: advance, Foreground: fg, Background: bg})
			at += advance
		}
		for at+cell <= end {
			appendSceneText(&row, api.TextRun{X: at, Text: " ", Advance: cell, Foreground: fg, Background: bg})
			at += cell
		}
		m.hits = append(m.hits, tabHit{x, end, i})
		x = end
	}
	scene.Apply(api.SceneUpdate{Reset: true, Background: row.Background, Rows: []api.SceneRow{row}})
}
