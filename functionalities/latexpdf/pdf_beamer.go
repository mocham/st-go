package latexpdf

import (
	"fmt"
	"math"
	"strconv"
	"strings"

	"golang.org/x/net/html"
)

func (layout *pdfDocumentLayout) ruleFormula(node *html.Node, style pdfTextStyle) *pdfFormula {
	fontSize := style.size * layout.fontScale * layout.resolvedTextSize()
	widthCSS, _ := htmlStyleProperty(node, "width")
	heightCSS, _ := htmlStyleProperty(node, "height")
	width := pdfCSSLength(widthCSS, layout.availableWidth(0), fontSize)
	height := pdfCSSLength(heightCSS, layout.pageHeight-layout.marginTop-layout.marginBottom, fontSize)
	if width <= 0 || height <= 0 || fontSize <= 0 {
		return nil
	}
	segments, err := parseSVGPath(fmt.Sprintf("M0 0H%gV%gH0Z", width, height), identityAffine())
	if err != nil {
		return nil
	}
	path := pdfFormulaPath{fillPaint: pdfFormulaPaint{enabled: true, current: true}}
	path.Segments = segments
	return &pdfFormula{viewWidth: width, viewHeight: height, widthEx: width / (fontSize * 25.4 / 144), paths: []pdfFormulaPath{path}}
}

func (layout *pdfDocumentLayout) resolvedTextSize() float64 {
	if layout.textSize > 0 {
		return layout.textSize
	}
	return 1
}

func (layout *pdfDocumentLayout) bodyStyle() pdfTextStyle {
	style := bodyPDFStyle()
	if color := layout.textColor; color != nil {
		style.red, style.green, style.blue = color.red, color.green, color.blue
	}
	return style
}

func (layout *pdfDocumentLayout) flowBoxY(y float64) float64 {
	if layout.inFlowBox && layout.boxScale > 0 {
		return layout.boxTop + (y-layout.boxTop)*layout.boxScale
	}
	return y
}

func (layout *pdfDocumentLayout) addLinkRegion(x, y, width, height float64, link int) {
	if layout.inFlowBox && layout.boxScale > 0 {
		x = layout.boxCenterX + (x-layout.boxCenterX)*layout.boxScale
		y = layout.flowBoxY(y)
		width, height = width*layout.boxScale, height*layout.boxScale
	}
	layout.pdf.Link(x, y, width, height, link)
}

func (layout *pdfDocumentLayout) renderFlowBox(node *html.Node, indent float64) {
	id := htmlAttribute(node, "id")
	x, width := layout.marginLeft+indent, layout.availableWidth(indent)
	availableHeight := layout.pageHeight - layout.marginTop - layout.marginBottom
	fontSize := 11.2 * layout.fontScale * layout.resolvedTextSize()
	length := func(property string, fallback float64) float64 {
		if css, ok := htmlStyleProperty(node, property); ok {
			return math.Max(0, pdfCSSLength(css, width, fontSize))
		}
		return fallback
	}
	if specified := length("width", width); specified > 0 {
		width = math.Min(width, specified)
	}
	padLeft, padRight := math.Min(width/4, length("padding-left", 4)), math.Min(width/4, length("padding-right", 4))
	padTop, padBottom := math.Min(availableHeight/4, length("padding-top", 4)), math.Min(availableHeight/4, length("padding-bottom", 4))
	gap := length("margin-top", 6)
	nested := layout.inFlowBox
	scale := 1.0
	height := layout.measuredBoxes[id]
	if nested {
		layout.y += gap
	} else if layout.measuredBoxes == nil {
		// Measure at a stable origin. Placement happens in the second pass.
		if layout.y > layout.marginTop+.01 {
			layout.addPage()
		}
	} else {
		if height > availableHeight-padTop-padBottom {
			scale = (availableHeight - padTop - padBottom) / height
		}
		if layout.y <= layout.marginTop+.01 {
			gap = 0
		}
		if layout.y+gap+height*scale+padTop+padBottom > layout.pageHeight-layout.marginBottom+.001 {
			layout.addPage()
		} else {
			layout.y += gap
		}
	}
	boxTop := layout.y
	boxHeight := padTop + height*scale + padBottom
	radius := math.Min(length("border-radius", 0), math.Min(width, boxHeight)/2)
	paint := func(mode string) {
		if radius > 0 {
			layout.pdf.RoundedRect(x, boxTop, width, boxHeight, radius, "1234", mode)
		} else {
			layout.pdf.Rect(x, boxTop, width, boxHeight, mode)
		}
	}
	if css, ok := htmlStyleProperty(node, "background-color"); ok && layout.measuredBoxes != nil {
		if color, valid := parseCSSColor(css); valid {
			layout.pdf.SetFillColor(color.red, color.green, color.blue)
			paint("F")
		}
	}
	for _, section := range layout.pendingSections {
		layout.registerAnchor(section.ID, layout.y)
		layout.pdf.Bookmark(section.Title, 0, layout.flowBoxY(layout.y))
	}
	layout.pendingSections = nil
	layout.registerAnchor(id, layout.y)
	left, right, inset := layout.marginLeft, layout.marginRight, layout.rightInset
	layout.marginLeft, layout.marginRight, layout.rightInset = x+padLeft, layout.pageWidth-x-width+padRight, 0
	layout.y += padTop
	contentTop := layout.y
	if !nested {
		layout.boxTop, layout.boxCenterX = contentTop, x+width/2
		layout.inFlowBox, layout.boxScale = true, scale
		layout.pdf.TransformBegin()
		layout.pdf.TransformScaleXY(scale*100, layout.boxCenterX, layout.boxTop)
	}
	layout.renderChildren(node, 0)
	layout.boxHeights[id] = layout.y - contentTop
	layout.y = contentTop + (layout.y-contentTop)*scale + padBottom
	if !nested {
		layout.pdf.TransformEnd()
		layout.inFlowBox, layout.boxScale = false, 0
	}
	layout.marginLeft, layout.marginRight, layout.rightInset = left, right, inset
	boxHeight = layout.y - boxTop
	border := renderColor{128, 146, 161}
	if css, ok := htmlStyleProperty(node, "border-color"); ok {
		if color, valid := parseCSSColor(css); valid {
			border = color
		}
	}
	if thickness := length("border-width", .3); thickness > 0 {
		layout.pdf.SetDrawColor(border.red, border.green, border.blue)
		layout.pdf.SetLineWidth(thickness)
		paint("D")
	}
	if layout.regions == nil {
		layout.regions = make(map[string][]PDFRegion)
	}
	region := PDFRegion{Page: layout.pdf.PageNo(), X: x, Y: boxTop, Width: width, Height: layout.y - boxTop}
	if nested && layout.boxScale > 0 {
		region.X = layout.boxCenterX + (region.X-layout.boxCenterX)*layout.boxScale
		region.Y = layout.flowBoxY(region.Y)
		region.Width, region.Height = region.Width*layout.boxScale, region.Height*layout.boxScale
	}
	layout.regions[id] = []PDFRegion{region}
}

func pdfCSSLength(value string, available, fontSize float64) float64 {
	if strings.HasPrefix(value, "calc(") && strings.HasSuffix(value, ")") {
		if first, second, ok := strings.Cut(value[5:len(value)-1], " + "); ok {
			return pdfCSSLength(first, available, fontSize) + pdfCSSLength(second, available, fontSize)
		}
	}
	for _, unit := range []struct {
		suffix string
		factor float64
	}{
		{"%", available / 100}, {"mm", 1}, {"cm", 10}, {"in", 25.4},
		{"pt", 25.4 / 72}, {"em", fontSize * 25.4 / 72}, {"ex", fontSize * 25.4 / 144},
	} {
		if strings.HasSuffix(value, unit.suffix) {
			number, err := strconv.ParseFloat(strings.TrimSpace(strings.TrimSuffix(value, unit.suffix)), 64)
			if err == nil && !math.IsInf(number, 0) && !math.IsNaN(number) {
				return number * unit.factor
			}
		}
	}
	return 0
}

func (layout *pdfDocumentLayout) renderBeamerColumns(node *html.Node, indent float64) {
	var columns []*html.Node
	for child := node.FirstChild; child != nil; child = child.NextSibling {
		if htmlClasses(child)["beamer-column"] {
			columns = append(columns, child)
		}
	}
	if len(columns) == 0 {
		layout.renderChildren(node, indent)
		return
	}
	available := layout.availableWidth(indent)
	gap := available * .01
	widths := make([]float64, len(columns))
	total := 0.0
	for index, column := range columns {
		css, _ := htmlStyleProperty(column, "width")
		width := pdfCSSLength(css, available, 11.2*layout.fontScale)
		if width <= 0 {
			width = available / float64(len(columns))
		}
		widths[index], total = width, total+width
	}
	usable := available - gap*float64(len(columns)-1)
	if total > usable {
		for index := range widths {
			widths[index] *= usable / total
		}
	} else if len(columns) > 1 {
		gap = (available - total) / float64(len(columns)-1)
	}
	left, right, inset := layout.marginLeft, layout.marginRight, layout.rightInset
	startPage, startY := layout.pdf.PageNo(), layout.y
	endPage, endY := startPage, startY
	x := left + indent
	for index, column := range columns {
		layout.pdf.SetPage(startPage)
		layout.y = startY
		layout.marginLeft, layout.marginRight, layout.rightInset = x, layout.pageWidth-x-widths[index], 0
		layout.renderChildren(column, 0)
		page := layout.pdf.PageNo()
		if page > endPage || (page == endPage && layout.y > endY) {
			endPage, endY = page, layout.y
		}
		x += widths[index] + gap
	}
	layout.marginLeft, layout.marginRight, layout.rightInset = left, right, inset
	layout.pdf.SetPage(endPage)
	layout.y = endY
}

func (layout *pdfDocumentLayout) renderBeamerBlock(node *html.Node, indent float64) {
	layout.verticalSpace(1)
	layout.ensureSpace(10)
	startPage, startY := layout.pdf.PageNo(), layout.y
	previousInset := layout.rightInset
	layout.rightInset += 2
	for child := node.FirstChild; child != nil; child = child.NextSibling {
		if child.Type == html.ElementNode && child.Data == "h3" {
			style := pdfTextStyle{size: 11.2, bold: true, red: 23, green: 52, blue: 77}
			layout.drawParagraph(layout.collectInline(child, style), indent+2, 1, 1)
		} else {
			layout.renderNode(child, indent+2)
		}
	}
	layout.y += 1
	layout.rightInset = previousInset
	theme := pdfBoxTheme{fillR: 234, fillG: 242, fillB: 248, lineR: 25, lineG: 76, lineB: 120}
	if htmlClasses(node)["alertblock"] {
		theme.lineR, theme.lineG, theme.lineB = 168, 50, 50
	}
	if htmlClasses(node)["exampleblock"] {
		theme.lineR, theme.lineG, theme.lineB = 32, 117, 74
	}
	layout.drawBoxAcrossPages(startPage, startY, layout.pdf.PageNo(), layout.y, indent, theme)
	layout.verticalSpace(1)
}
