package latexpdf

import (
	"math"
	"strings"

	"codeberg.org/go-pdf/fpdf"
	"golang.org/x/net/html"
)

type pdfFormulaClip [4]fpdf.PointType

func (layout *pdfDocumentLayout) clipFormula(clips []pdfFormulaClip, x, y, scale float64) {
	for _, clip := range clips {
		points := make([]fpdf.PointType, len(clip))
		for i, point := range clip {
			points[i] = fpdf.PointType{X: x + point.X*scale, Y: y + point.Y*scale}
		}
		layout.pdf.ClipPolygon(points, false)
	}
}

// MathJax joins stretchy braces (and other delimiters) with oversized glyphs
// cropped by nested SVG viewports. Both their viewBox transform and their clip
// rectangle are essential: applying only the glyph's scale leaves gaps and
// draws overlapping extenders at the origin.
func svgNestedViewport(node *html.Node, matrix svgAffine) (svgAffine, *pdfFormulaClip, bool) {
	x, y := parseSVGNumber(htmlAttribute(node, "x")), parseSVGNumber(htmlAttribute(node, "y"))
	width, height := parseSVGNumber(htmlAttribute(node, "width")), parseSVGNumber(htmlAttribute(node, "height"))
	if width <= 0 || height <= 0 {
		return matrix, nil, false
	}
	var clip *pdfFormulaClip
	overflow := htmlAttribute(node, "overflow")
	if value, ok := htmlStyleProperty(node, "overflow"); ok {
		overflow = value
	}
	if overflow != "visible" {
		clip = &pdfFormulaClip{}
		for i, point := range [][2]float64{{x, y}, {x + width, y}, {x + width, y + height}, {x, y + height}} {
			px, py := matrix.point(point[0], point[1])
			clip[i] = fpdf.PointType{X: px, Y: py}
		}
	}
	local := svgAffine{a: 1, d: 1, e: x, f: y}
	viewBox := strings.Fields(strings.ReplaceAll(htmlAttribute(node, "viewbox"), ",", " "))
	if len(viewBox) == 4 {
		vx, vy := parseSVGNumber(viewBox[0]), parseSVGNumber(viewBox[1])
		vw, vh := parseSVGNumber(viewBox[2]), parseSVGNumber(viewBox[3])
		if vw <= 0 || vh <= 0 {
			return matrix, nil, false
		}
		sx, sy := width/vw, height/vh
		aspect := strings.Fields(htmlAttribute(node, "preserveaspectratio"))
		if len(aspect) > 0 && aspect[0] == "defer" {
			aspect = aspect[1:]
		}
		align := "xMidYMid"
		if len(aspect) > 0 {
			align = aspect[0]
		}
		if align != "none" {
			scale := math.Min(sx, sy)
			if len(aspect) > 1 && aspect[1] == "slice" {
				scale = math.Max(sx, sy)
			}
			sx, sy = scale, scale
			if strings.Contains(align, "xMid") {
				local.e += (width - vw*sx) / 2
			} else if strings.Contains(align, "xMax") {
				local.e += width - vw*sx
			}
			if strings.Contains(align, "YMid") {
				local.f += (height - vh*sy) / 2
			} else if strings.Contains(align, "YMax") {
				local.f += height - vh*sy
			}
		}
		local.a, local.d = sx, sy
		local.e -= vx * sx
		local.f -= vy * sy
	}
	return matrix.multiply(local), clip, true
}
