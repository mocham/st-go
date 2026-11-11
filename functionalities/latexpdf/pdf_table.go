package latexpdf

import (
	"math"
	"strings"

	"golang.org/x/net/html"
)

type pdfTableCell struct {
	items []pdfLineItem
	lines [][]pdfLineItem
	align string
}

type pdfTableRow struct {
	cells  []pdfTableCell
	header bool
}

func pdfLineHeight(line []pdfLineItem) float64 {
	ascent, descent, font := 0.0, 0.0, 0.0
	for _, item := range line {
		ascent = math.Max(ascent, item.ascent)
		descent = math.Max(descent, item.descent)
		font = math.Max(font, item.inline.style.size*25.4/72)
	}
	return ascent + descent + font*0.5
}

func (row pdfTableRow) height(padding, emptyHeight float64, firstLineOnly bool) float64 {
	height := emptyHeight
	for _, cell := range row.cells {
		h := 0.0
		for _, line := range cell.lines {
			h += pdfLineHeight(line)
			if firstLineOnly {
				break
			}
		}
		height = math.Max(height, h)
	}
	return height + 2*padding
}

func (layout *pdfDocumentLayout) renderTable(table *html.Node, indent float64) {
	var rows []pdfTableRow
	columns := 0
	var collect func(*html.Node)
	collect = func(node *html.Node) {
		if node.Type == html.ElementNode && node.Data == "tr" {
			row := pdfTableRow{}
			for cell := node.FirstChild; cell != nil; cell = cell.NextSibling {
				if cell.Type != html.ElementNode || cell.Data != "td" && cell.Data != "th" {
					continue
				}
				style := bodyPDFStyle()
				if cell.Data == "th" {
					style.bold = true
					row.header = true
				}
				align := "left"
				css := strings.ReplaceAll(htmlAttribute(cell, "style"), " ", "")
				if strings.Contains(css, "text-align:right") {
					align = "right"
				} else if strings.Contains(css, "text-align:center") {
					align = "center"
				}
				row.cells = append(row.cells, pdfTableCell{items: layout.breakInlineRuns(layout.collectInlineChildren(cell, style)), align: align})
			}
			if len(row.cells) > 0 {
				columns = max(columns, len(row.cells))
				rows = append(rows, row)
			}
			return
		}
		for child := node.FirstChild; child != nil; child = child.NextSibling {
			collect(child)
		}
	}
	collect(table)
	if columns == 0 {
		return
	}
	available := layout.availableWidth(indent)
	pageHeight := layout.pageHeight - layout.marginTop - layout.marginBottom
	padding := math.Min(1.6*layout.fontScale, math.Min(available/float64(columns)/5, pageHeight/20))
	emptyHeight := math.Min(bodyPDFStyle().size*layout.fontScale*25.4/72*1.54, pageHeight/4)
	preferred := make([]float64, columns)
	for _, row := range rows {
		for column, cell := range row.cells {
			width := 0.0
			for _, item := range cell.items {
				if item.inline.lineBreak {
					width = 0
				} else {
					width += item.width
				}
				preferred[column] = math.Max(preferred[column], width+2*padding)
			}
		}
	}
	// Give every column a readable share, then allocate remaining space by
	// content width. Long prose cannot take all the space from short labels.
	base := math.Min(12*layout.fontScale, available/float64(columns)*0.5)
	weight := 0.0
	for column := range preferred {
		preferred[column] = math.Max(1, math.Min(preferred[column], available*0.7)-base)
		weight += preferred[column]
	}
	widths := make([]float64, columns)
	for column := range widths {
		widths[column] = base + (available-base*float64(columns))*preferred[column]/weight
	}
	for index := range rows {
		row := &rows[index]
		for len(row.cells) < columns {
			row.cells = append(row.cells, pdfTableCell{})
		}
		for column := range row.cells {
			cell := &row.cells[column]
			width := widths[column] - 2*padding
			cell.lines = layout.wrapInlineItems(cell.items, width)
			for _, line := range cell.lines {
				lineWidth := 0.0
				for _, item := range line {
					lineWidth += item.width
				}
				// A huge formula or glyph in an exceptionally narrow column must
				// still fit both the cell width and one page fragment.
				scale := math.Min(1, math.Min(width/math.Max(lineWidth, 0.001), (pageHeight/4)/math.Max(pdfLineHeight(line), 0.001)))
				if scale < 1 {
					for i := range line {
						line[i].width *= scale
						line[i].formulaWidth *= scale
						line[i].ascent *= scale
						line[i].descent *= scale
						line[i].inline.style.size *= scale
					}
				}
			}
		}
	}
	layout.verticalSpace(2)
	headerHeight := 0.0
	if rows[0].header {
		headerHeight = rows[0].height(padding, emptyHeight, false)
	}
	repeatHeader := headerHeight > 0 && headerHeight <= pageHeight/3
	if len(rows) > 1 && headerHeight > 0 {
		needed := headerHeight + rows[1].height(padding, emptyHeight, true)
		if needed <= pageHeight {
			layout.ensureSpace(needed)
		}
	}
	for index, row := range rows {
		// Slice positions are local to this row's pagination; the repeatable
		// header retains its complete lines.
		row.cells = append([]pdfTableCell(nil), row.cells...)
		nextPage := func() {
			layout.addPage()
			if repeatHeader && index > 0 {
				header := rows[0]
				header.cells = append([]pdfTableCell(nil), header.cells...)
				layout.drawTableChunk(&header, widths, indent, padding, emptyHeight, headerHeight)
			}
		}
		fullHeight := row.height(padding, emptyHeight, false)
		pageCapacity := pageHeight
		if repeatHeader && index > 0 {
			pageCapacity -= headerHeight
		}
		if fullHeight <= pageCapacity && layout.y+fullHeight > layout.pageHeight-layout.marginBottom {
			nextPage()
		}
		for {
			space := layout.pageHeight - layout.marginBottom - layout.y
			if row.height(padding, emptyHeight, true) > space+0.00001 {
				nextPage()
				space = layout.pageHeight - layout.marginBottom - layout.y
			}
			if !layout.drawTableChunk(&row, widths, indent, padding, emptyHeight, space) {
				break
			}
			nextPage()
		}
	}
	layout.y = math.Min(layout.pageHeight-layout.marginBottom, layout.y+2)
}

// Draw as many complete lines of every cell as fit, leaving the rest for the
// next page. This also handles a single cell taller than a whole page.
func (layout *pdfDocumentLayout) drawTableChunk(row *pdfTableRow, widths []float64, indent, padding, emptyHeight, space float64) bool {
	counts := make([]int, len(row.cells))
	height := emptyHeight
	remaining := false
	for column, cell := range row.cells {
		h := 0.0
		for _, line := range cell.lines {
			next := pdfLineHeight(line)
			if h+next > space-2*padding+0.00001 {
				break
			}
			h += next
			counts[column]++
		}
		height = math.Max(height, h)
		remaining = remaining || counts[column] < len(cell.lines)
	}
	height += 2 * padding
	top, x := layout.y, layout.marginLeft+indent
	for column := range row.cells {
		cell := &row.cells[column]
		layout.pdf.SetDrawColor(170, 180, 190)
		layout.pdf.SetLineWidth(0.18)
		mode := "D"
		if row.header {
			layout.pdf.SetFillColor(233, 237, 241)
			mode = "DF"
		}
		layout.pdf.Rect(x, top, widths[column], height, mode)
		layout.y = top + padding
		for _, line := range cell.lines[:counts[column]] {
			lineWidth := 0.0
			for _, item := range line {
				lineWidth += item.width
			}
			offset := 0.0
			if cell.align == "right" {
				offset = widths[column] - 2*padding - lineWidth
			} else if cell.align == "center" {
				offset = (widths[column] - 2*padding - lineWidth) / 2
			}
			layout.drawLine(line, x+padding+math.Max(0, offset))
		}
		cell.lines = cell.lines[counts[column]:]
		x += widths[column]
	}
	layout.y = top + height
	return remaining
}
