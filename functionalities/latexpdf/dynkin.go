package latexpdf

import (
	"fmt"
	"html/template"
	"math"
	"strconv"
	"strings"
)

type dynkinNode struct {
	x, y float64
}

// renderDynkinCommand implements the labelled A/B/C/D diagrams used by the
// dynkin-diagrams package in manuscripts. The result is ordinary SVG so the
// native PDF path converter can retain it as vectors.
func (context *latexRenderContext) renderDynkinCommand(source string, commandEnd int) (string, int, bool) {
	position := skipTeXSpace(source, commandEnd)
	options := ""
	if position < len(source) && source[position] == '[' {
		var ok bool
		options, position, ok = parseDelimited(source, position, '[', ']')
		if !ok {
			return "", commandEnd, false
		}
		position = skipTeXSpace(source, position)
	}
	diagramType, next, ok := parseBraced(source, position)
	if !ok {
		return "", commandEnd, false
	}
	position = skipTeXSpace(source, next)
	rankSource, end, ok := parseBraced(source, position)
	if !ok {
		return "", commandEnd, false
	}

	diagramType = strings.ToUpper(strings.TrimSpace(diagramType))
	if diagramType != "A" && diagramType != "B" && diagramType != "C" && diagramType != "D" {
		return "", commandEnd, false
	}
	labels, scale := parseDynkinOptions(options)
	rank, _ := strconv.Atoi(strings.TrimSpace(rankSource))
	if rank <= 0 {
		rank = len(labels)
	}
	if rank <= 0 {
		rank = 5
	}
	if diagramType == "D" && rank < 4 {
		rank = 4
	}
	if rank > 32 {
		return "", commandEnd, false
	}
	for len(labels) < rank {
		labels = append(labels, "")
	}
	if len(labels) > rank {
		labels = labels[:rank]
	}

	glyphs := make([]diagramGlyph, rank)
	maxLabelHeight := 0.0
	for index, label := range labels {
		if strings.TrimSpace(label) == "" {
			continue
		}
		glyph, err := context.mathDiagramGlyph(label)
		if err != nil {
			glyph = plainDiagramGlyph(label)
		}
		glyphs[index] = glyph
		if glyph.viewHeight > maxLabelHeight {
			maxLabelHeight = glyph.viewHeight
		}
	}

	const (
		margin   = 180.0
		gap      = 780.0
		radius   = 92.0
		labelGap = 120.0
	)
	nodes := make([]dynkinNode, rank)
	centerY := margin + maxLabelHeight + labelGap + radius
	labelHalfWidth := func(index int) float64 {
		return math.Max(radius, glyphs[index].viewWidth/2)
	}
	nodes[0] = dynkinNode{x: margin + labelHalfWidth(0), y: centerY}
	width := 0.0
	if diagramType == "D" {
		for index := 1; index < rank-2; index++ {
			spacing := math.Max(gap, labelHalfWidth(index-1)+labelHalfWidth(index)+220)
			nodes[index] = dynkinNode{x: nodes[index-1].x + spacing, y: centerY}
		}
		branchHalfWidth := math.Max(labelHalfWidth(rank-2), labelHalfWidth(rank-1))
		spacing := math.Max(gap, labelHalfWidth(rank-3)+branchHalfWidth+220)
		branchX := nodes[rank-3].x + spacing
		nodes[rank-2] = dynkinNode{x: branchX, y: centerY - 230}
		nodes[rank-1] = dynkinNode{x: branchX, y: centerY + 230}
		width = branchX + branchHalfWidth + margin
	} else {
		for index := 1; index < rank; index++ {
			spacing := math.Max(gap, labelHalfWidth(index-1)+labelHalfWidth(index)+220)
			nodes[index] = dynkinNode{x: nodes[index-1].x + spacing, y: centerY}
		}
		width = nodes[rank-1].x + labelHalfWidth(rank-1) + margin
	}

	height := centerY + radius + margin
	if diagramType == "D" {
		height += 230
		if glyphs[rank-1].markup != "" {
			height += glyphs[rank-1].viewHeight + labelGap
		}
	}
	var svg strings.Builder
	fmt.Fprintf(&svg, `<svg style="vertical-align: -0.45ex;" xmlns="http://www.w3.org/2000/svg" width="%.3fex" height="%.3fex" viewBox="0 0 %.3f %.3f" role="img" focusable="false" data-diagram="dynkin">`, width/diagramUnitsPerEx*scale, height/diagramUnitsPerEx*scale, width, height)

	drawBond := func(left, right dynkinNode, double bool) {
		if !double {
			fmt.Fprintf(&svg, `<line x1="%.3f" y1="%.3f" x2="%.3f" y2="%.3f" fill="none" stroke="currentColor" stroke-width="34"/>`, left.x, left.y, right.x, right.y)
			return
		}
		dx, dy := right.x-left.x, right.y-left.y
		length := dx*dx + dy*dy
		if length == 0 {
			return
		}
		// These bonds are horizontal in the supported diagrams. Keeping the
		// offset explicit also makes the SVG converter's path geometry stable.
		for _, offset := range []float64{-31, 31} {
			fmt.Fprintf(&svg, `<line x1="%.3f" y1="%.3f" x2="%.3f" y2="%.3f" fill="none" stroke="currentColor" stroke-width="28"/>`, left.x, left.y+offset, right.x, right.y+offset)
		}
	}

	if diagramType == "D" {
		for index := 0; index < rank-3; index++ {
			drawBond(nodes[index], nodes[index+1], false)
		}
		drawBond(nodes[rank-3], nodes[rank-2], false)
		drawBond(nodes[rank-3], nodes[rank-1], false)
	} else {
		for index := 0; index < rank-1; index++ {
			double := (diagramType == "B" || diagramType == "C") && index == rank-2
			drawBond(nodes[index], nodes[index+1], double)
		}
		if rank >= 2 && (diagramType == "B" || diagramType == "C") {
			left, right := nodes[rank-2], nodes[rank-1]
			tipX, direction := right.x-radius-30, 1.0
			if diagramType == "C" {
				tipX, direction = left.x+radius+30, -1
			}
			fmt.Fprintf(&svg, `<path d="M %.3f %.3f L %.3f %.3f L %.3f %.3f Z" fill="currentColor" stroke="none"/>`, tipX, centerY, tipX-direction*125, centerY-75, tipX-direction*125, centerY+75)
		}
	}
	for _, node := range nodes {
		writeDynkinCircle(&svg, node.x, node.y, radius)
	}
	for index, glyph := range glyphs {
		if glyph.markup == "" {
			continue
		}
		x := nodes[index].x - glyph.viewWidth/2
		y := margin
		if diagramType == "D" && index == rank-1 {
			y = nodes[index].y + radius + labelGap
		}
		writeDiagramGlyph(&svg, glyph, x, y)
	}
	svg.WriteString(`</svg>`)

	literal := source[:end]
	result := `<span class="math-inline dynkin-diagram" data-tex="` + template.HTMLEscapeString(literal) + `">` + svg.String() + `</span>`
	return result, end, true
}

func parseDynkinOptions(source string) ([]string, float64) {
	var labels []string
	scale := 1.0
	for _, option := range splitDynkinFields(source) {
		name, value, ok := strings.Cut(option, "=")
		if !ok {
			continue
		}
		switch strings.ToLower(strings.TrimSpace(name)) {
		case "labels":
			value = strings.TrimSpace(value)
			if inner, end, parsed := parseBraced(value, 0); parsed && skipTeXSpace(value, end) == len(value) {
				value = inner
			}
			for _, label := range splitDynkinFields(value) {
				labels = append(labels, strings.TrimSpace(label))
			}
		case "scale":
			if parsed, err := strconv.ParseFloat(strings.TrimSpace(value), 64); err == nil && parsed >= 0.25 && parsed <= 4 {
				scale = parsed
			}
		}
	}
	return labels, scale
}

func splitDynkinFields(source string) []string {
	var result []string
	start, braceDepth, bracketDepth := 0, 0, 0
	for position := 0; position < len(source); position++ {
		if isEscaped(source, position) {
			continue
		}
		switch source[position] {
		case '{':
			braceDepth++
		case '}':
			if braceDepth > 0 {
				braceDepth--
			}
		case '[':
			bracketDepth++
		case ']':
			if bracketDepth > 0 {
				bracketDepth--
			}
		case ',':
			if braceDepth == 0 && bracketDepth == 0 {
				result = append(result, source[start:position])
				start = position + 1
			}
		}
	}
	result = append(result, source[start:])
	return result
}

func writeDynkinCircle(output *strings.Builder, x, y, radius float64) {
	const kappa = 0.5522847498
	control := radius * kappa
	fmt.Fprintf(output, `<path d="M %.3f %.3f C %.3f %.3f %.3f %.3f %.3f %.3f C %.3f %.3f %.3f %.3f %.3f %.3f C %.3f %.3f %.3f %.3f %.3f %.3f C %.3f %.3f %.3f %.3f %.3f %.3f Z" fill="#ffffff" stroke="currentColor" stroke-width="34"/>`,
		x+radius, y,
		x+radius, y+control, x+control, y+radius, x, y+radius,
		x-control, y+radius, x-radius, y+control, x-radius, y,
		x-radius, y-control, x-control, y-radius, x, y-radius,
		x+control, y-radius, x+radius, y-control, x+radius, y)
}
