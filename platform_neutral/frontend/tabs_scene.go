package frontend

import (
	"strconv"

	"st-go/platform_neutral/api"
	term "st-go/platform_neutral/stterminal"
)

func (m *terminalTabs) paintSceneBar(scene api.SceneTarget) {
	if m.bar == nil || !m.bar.Visible() {
		return
	}
	r := m.bar.Bounds()
	cell := max(1, m.renderer.cw)
	m.hits = nil
	row := api.SceneRow{Y: 0, Height: r.Dy(), FontSize: m.renderer.ch, Baseline: max(0, (r.Dy()-m.renderer.ch)/2) + m.renderer.baseline, Background: 0xff2d2b46}
	available := r.Dx()
	m.imeLeft, m.imeRight = -1, -1
	if m.active < len(m.entries) {
		if v := m.entries[m.active].terminal.inputMethod.view.Load(); v != nil {
			width := min(available, 9*cell)
			if v.text != "" {
				width = max(width, available-min(3*cell, available/4))
			}
			available -= width
			m.imeLeft, m.imeRight = available, min(r.Dx(), available+9*cell)
			label := " IME OFF "
			if v.enabled {
				label = " IME ON  "
			}
			label += v.text
			at := available
			for _, ch := range label {
				advance := max(0, term.CellWidth(ch)) * cell
				if at+advance > r.Dx() {
					break
				}
				appendSceneText(&row, api.TextRun{X: at, Text: string(ch), Advance: advance, Foreground: 0xffc4dfb5, Background: row.Background})
				at += advance
			}
		}
	}
	widths := make([]int, len(m.entries))
	needed := 0
	for i, entry := range m.entries {
		widths[i] = min(available, terminalTabWidthCells*cell)
		if entry.compact {
			widths[i] = min(available, 3*cell)
		}
		if i <= m.active {
			needed += widths[i]
		}
	}
	start := 0
	for start < m.active && needed > available {
		needed -= widths[start]
		start++
	}
	var fills []api.SceneFill
	x := 0
	for i := start; i < len(m.entries) && x < available; i++ {
		if x > 0 && x+widths[i] > available {
			break
		}
		bg, fg := uint32(0xff2d2b46), uint32(0xffc4dfb5)
		if i == m.active {
			bg, fg = 0xff414967, 0xffffffff
		}
		end := min(available, x+widths[i])
		labelEnd, shrinkLeft := end, -1
		text := strconv.Itoa(i+1) + " " + m.entries[i].title
		if m.entries[i].compact {
			text = " " + strconv.Itoa(i+1)
		} else if end-x >= 3*cell && m.allowed && available >= 12*cell {
			shrinkLeft = end - 2*cell
			labelEnd = shrinkLeft
		}
		label := tabLabel(text, max(0, (labelEnd-x)/cell-1))
		// Pad the complete clickable tab so its background is independent of title length.
		at := x
		for _, char := range label {
			advance := max(0, term.CellWidth(char)) * cell
			if advance == 0 {
				continue
			}
			if at+advance > labelEnd {
				break
			}
			appendSceneText(&row, api.TextRun{X: at, Text: string(char), Advance: advance, Foreground: fg, Background: bg})
			at += advance
		}
		for at+cell <= labelEnd {
			appendSceneText(&row, api.TextRun{X: at, Text: " ", Advance: cell, Foreground: fg, Background: bg})
			at += cell
		}
		if shrinkLeft >= 0 {
			appendSceneText(&row, api.TextRun{X: shrinkLeft, Text: " -", Advance: cell, Foreground: fg, Background: 0xff414967})
		}
		if m.entries[i].compact && end-x > 4 && r.Dy() > 4 {
			// A numbered window icon, drawn as primitives so no symbol font is
			// required. Text and outline share the same local tab coordinates.
			fills = append(fills,
				api.SceneFill{X: x + 1, Y: 1, Width: end - x - 2, Height: 1, Color: fg},
				api.SceneFill{X: x + 1, Y: r.Dy() - 2, Width: end - x - 2, Height: 1, Color: fg},
				api.SceneFill{X: x + 1, Y: 1, Width: 1, Height: r.Dy() - 2, Color: fg},
				api.SceneFill{X: end - 2, Y: 1, Width: 1, Height: r.Dy() - 2, Color: fg})
		}
		m.hits = append(m.hits, tabHit{left: x, right: end, index: i, shrinkLeft: shrinkLeft})
		x = end
	}
	scene.Apply(api.SceneUpdate{Reset: true, Background: row.Background, Rows: []api.SceneRow{row}, Fills: fills})
}
