package latexpdf

import (
	"bytes"
	"errors"
	"fmt"
	"html/template"
	"math"
	"strconv"
	"strings"
	"unicode"

	"golang.org/x/net/html"
)

const (
	diagramUnitsPerEx = 442.0
	diagramNodePad    = 150.0
)

type diagramGlyph struct {
	markup                string
	viewX, viewY          float64
	viewWidth, viewHeight float64
}

type diagramCell struct {
	tex   string
	glyph diagramGlyph
}

type diagramArrow struct {
	fromRow, fromCol int
	toRow, toCol     int
	style            string
	offset, bend     float64
	upper, lower     string
	upperGlyph       diagramGlyph
	lowerGlyph       diagramGlyph
}

type matrixDiagram struct {
	cells     [][]diagramCell
	arrows    []diagramArrow
	columnGap float64
	rowGap    float64
}

// renderXYMatrixExpression handles a complete xymatrix as a real diagram.
// Embedded xymatrix fragments still use normalizeXYMatrices as a conservative
// MathJax fallback.
func (context *latexRenderContext) renderXYMatrixExpression(expression string, display bool) (string, bool) {
	body, spacing, ok := standaloneXYMatrix(expression)
	if !ok {
		return "", false
	}
	diagram, err := parseXYMatrix(body)
	if err != nil {
		return "", false
	}
	diagram.applyXYSpacing(spacing)
	rendered, err := context.renderMatrixDiagram(diagram, expression, display, "xymatrix")
	if err != nil {
		return "", false
	}
	return rendered, true
}

func standaloneXYMatrix(source string) (body, spacing string, ok bool) {
	source = strings.TrimSpace(source)
	for {
		if len(source) < 2 || source[0] != '{' {
			break
		}
		inner, end, parsed := parseBraced(source, 0)
		if !parsed || skipTeXSpace(source, end) != len(source) {
			break
		}
		source = strings.TrimSpace(inner)
	}
	const command = `\xymatrix`
	if !strings.HasPrefix(source, command) || !commandBoundary(source, len(command)) {
		return "", "", false
	}
	position := len(command)
	if position < len(source) && source[position] == '@' {
		start := position
		position++
		for position < len(source) && source[position] != '{' && !unicode.IsSpace(rune(source[position])) {
			position++
		}
		spacing = source[start:position]
	}
	position = skipTeXSpace(source, position)
	body, end, ok := parseBraced(source, position)
	if !ok || skipTeXSpace(source, end) != len(source) {
		return "", "", false
	}
	return body, spacing, true
}

func parseXYMatrix(source string) (matrixDiagram, error) {
	rows := splitTopLevelTeXRows(source)
	if len(rows) == 0 {
		return matrixDiagram{}, errors.New("empty xymatrix")
	}
	diagram := matrixDiagram{cells: make([][]diagramCell, len(rows))}
	width := 0
	for rowIndex, row := range rows {
		cells := splitTopLevelTeXCells(row)
		if len(cells) > width {
			width = len(cells)
		}
		diagram.cells[rowIndex] = make([]diagramCell, len(cells))
		for colIndex, sourceCell := range cells {
			cell, arrows := parseXYCell(sourceCell, rowIndex, colIndex)
			diagram.cells[rowIndex][colIndex] = diagramCell{tex: strings.TrimSpace(cell)}
			diagram.arrows = append(diagram.arrows, arrows...)
		}
	}
	if width == 0 {
		return matrixDiagram{}, errors.New("empty xymatrix rows")
	}
	for rowIndex := range diagram.cells {
		if missing := width - len(diagram.cells[rowIndex]); missing > 0 {
			diagram.cells[rowIndex] = append(diagram.cells[rowIndex], make([]diagramCell, missing)...)
		}
	}
	return diagram, nil
}

func (diagram *matrixDiagram) applyXYSpacing(source string) {
	source = strings.TrimSpace(strings.TrimPrefix(source, "@"))
	if source == "" {
		return
	}
	for len(source) > 0 {
		axis := byte(0)
		if source[0] == 'C' || source[0] == 'R' {
			axis = source[0]
			source = source[1:]
		}
		if len(source) == 0 || source[0] != '=' {
			return
		}
		source = source[1:]
		end := 0
		for end < len(source) && source[end] != '@' {
			end++
		}
		value, ok := tikZDimension(strings.TrimSpace(source[:end]), 980)
		if ok {
			switch axis {
			case 'C':
				diagram.columnGap = value
			case 'R':
				diagram.rowGap = value
			default:
				diagram.columnGap, diagram.rowGap = value, value
			}
		}
		if end >= len(source) {
			return
		}
		source = source[end+1:]
	}
}

func parseXYCell(source string, row, col int) (string, []diagramArrow) {
	var output strings.Builder
	var arrows []diagramArrow
	for position := 0; position < len(source); {
		relative := strings.Index(source[position:], `\ar`)
		if relative < 0 {
			output.WriteString(source[position:])
			break
		}
		start := position + relative
		commandEnd := start + len(`\ar`)
		if !commandBoundary(source, commandEnd) {
			output.WriteString(source[position:commandEnd])
			position = commandEnd
			continue
		}
		arrow, end, ok := parseXYArrow(source, commandEnd, row, col)
		if !ok {
			output.WriteString(source[position:commandEnd])
			position = commandEnd
			continue
		}
		output.WriteString(source[position:start])
		arrows = append(arrows, arrow)
		position = end
	}
	return output.String(), arrows
}

func parseXYArrow(source string, position, row, col int) (diagramArrow, int, bool) {
	arrow := diagramArrow{fromRow: row, fromCol: col, style: "->"}
	position = skipTeXSpace(source, position)
	if position < len(source) && source[position] == '@' {
		var ok bool
		position, ok = parseXYArrowModifier(source, position+1, &arrow)
		if !ok {
			return diagramArrow{}, position, false
		}
	}
	for {
		position = skipTeXSpace(source, position)
		if position < len(source) && (source[position] == '^' || source[position] == '_') {
			marker := source[position]
			label, end, ok := parseXYArrowLabel(source, position+1)
			if !ok {
				return diagramArrow{}, position, false
			}
			if marker == '^' {
				arrow.upper = label
			} else {
				arrow.lower = label
			}
			position = end
			continue
		}
		break
	}
	position = skipTeXSpace(source, position)
	if position >= len(source) || source[position] != '[' {
		return diagramArrow{}, position, false
	}
	direction, end, ok := parseDelimited(source, position, '[', ']')
	if !ok {
		return diagramArrow{}, position, false
	}
	toRow, toCol, ok := xyDirectionTarget(row, col, direction)
	if !ok {
		return diagramArrow{}, position, false
	}
	arrow.toRow, arrow.toCol = toRow, toCol
	position = end
	for {
		beforeSpace := position
		position = skipTeXSpace(source, position)
		if position < len(source) && (source[position] == '^' || source[position] == '_') {
			marker := source[position]
			label, labelEnd, parsed := parseXYArrowLabel(source, position+1)
			if !parsed {
				return diagramArrow{}, position, false
			}
			if marker == '^' {
				arrow.upper = label
			} else {
				arrow.lower = label
			}
			position = labelEnd
			continue
		}
		position = beforeSpace
		break
	}
	return arrow, position, true
}

func parseXYArrowLabel(source string, position int) (string, int, bool) {
	position = skipTeXSpace(source, position)
	if position < len(source) && source[position] == '(' {
		if _, end, ok := parseDelimited(source, position, '(', ')'); ok {
			position = skipTeXSpace(source, end)
		}
	}
	for position < len(source) && strings.ContainsRune("-+<>!", rune(source[position])) {
		position++
	}
	position = skipTeXSpace(source, position)
	if position >= len(source) {
		return "", position, false
	}
	if source[position] == '{' {
		return parseBraced(source, position)
	}
	if source[position] == '\\' {
		_, end := readTeXCommand(source, position)
		if end > position {
			return source[position:end], end, true
		}
	}
	_, size := utf8DecodeRune(source[position:])
	if size <= 0 {
		return "", position, false
	}
	return source[position : position+size], position + size, true
}

func parseXYArrowModifier(source string, position int, arrow *diagramArrow) (int, bool) {
	position = skipTeXSpace(source, position)
	if position >= len(source) {
		return position, false
	}
	switch source[position] {
	case '{':
		style, end, ok := parseBraced(source, position)
		if ok {
			arrow.style = strings.TrimSpace(style)
		}
		return end, ok
	case '<':
		value, end, ok := parseDelimited(source, position, '<', '>')
		if !ok {
			return position, false
		}
		if offset, parsed := tikZDimension(strings.TrimSpace(value), diagramUnitsPerEx); parsed {
			arrow.offset = offset
		}
		return end, true
	case '/':
		end := strings.IndexByte(source[position+1:], '/')
		if end < 0 {
			return position, false
		}
		end += position + 1
		value := strings.TrimSpace(source[position+1 : end])
		sign := 1.0
		if strings.HasPrefix(value, "_") {
			sign = -1
			value = strings.TrimSpace(strings.TrimPrefix(value, "_"))
		} else if strings.HasPrefix(value, "^") {
			value = strings.TrimSpace(strings.TrimPrefix(value, "^"))
		}
		if bend, parsed := tikZDimension(value, diagramUnitsPerEx); parsed {
			arrow.bend = sign * bend
		}
		return end + 1, true
	default:
		return position, false
	}
}

func utf8DecodeRune(source string) (rune, int) {
	for _, value := range source {
		return value, len(string(value))
	}
	return 0, 0
}

func xyDirectionTarget(row, col int, direction string) (int, int, bool) {
	for _, value := range strings.TrimSpace(direction) {
		switch value {
		case 'u':
			row--
		case 'd':
			row++
		case 'l':
			col--
		case 'r':
			col++
		default:
			return 0, 0, false
		}
	}
	return row, col, true
}

func (context *latexRenderContext) renderTikZCD(content, source string) string {
	diagram, err := parseTikZCD(content)
	if err != nil {
		return diagramSourceFallback("tikz-cd", source, err)
	}
	rendered, err := context.renderMatrixDiagram(diagram, source, true, "tikzcd")
	if err != nil {
		return diagramSourceFallback("tikz-cd", source, err)
	}
	return rendered
}

func parseTikZCD(source string) (matrixDiagram, error) {
	rows := splitTopLevelTeXRows(source)
	if len(rows) == 0 {
		return matrixDiagram{}, errors.New("empty tikzcd")
	}
	diagram := matrixDiagram{cells: make([][]diagramCell, len(rows))}
	width := 0
	for rowIndex, row := range rows {
		cells := splitTopLevelTeXCells(row)
		if len(cells) > width {
			width = len(cells)
		}
		diagram.cells[rowIndex] = make([]diagramCell, len(cells))
		for colIndex, cellSource := range cells {
			cell, arrows := parseTikZCDCell(cellSource, rowIndex, colIndex)
			diagram.cells[rowIndex][colIndex] = diagramCell{tex: strings.TrimSpace(cell)}
			diagram.arrows = append(diagram.arrows, arrows...)
		}
	}
	if width == 0 {
		return matrixDiagram{}, errors.New("empty tikzcd rows")
	}
	for rowIndex := range diagram.cells {
		diagram.cells[rowIndex] = append(diagram.cells[rowIndex], make([]diagramCell, width-len(diagram.cells[rowIndex]))...)
	}
	return diagram, nil
}

func parseTikZCDCell(source string, row, col int) (string, []diagramArrow) {
	var output strings.Builder
	var arrows []diagramArrow
	for position := 0; position < len(source); {
		relative := strings.IndexByte(source[position:], '\\')
		if relative < 0 {
			output.WriteString(source[position:])
			break
		}
		start := position + relative
		name, commandEnd := readTeXCommand(source, start)
		if name != "ar" && name != "arrow" {
			output.WriteString(source[position:commandEnd])
			position = commandEnd
			continue
		}
		cursor := skipTeXSpace(source, commandEnd)
		if cursor >= len(source) || source[cursor] != '[' {
			output.WriteString(source[position:commandEnd])
			position = commandEnd
			continue
		}
		options, end, ok := parseDelimited(source, cursor, '[', ']')
		if !ok {
			output.WriteString(source[position:commandEnd])
			position = commandEnd
			continue
		}
		arrow, ok := tikZCDArrow(options, row, col)
		if !ok {
			output.WriteString(source[position:end])
			position = end
			continue
		}
		output.WriteString(source[position:start])
		arrows = append(arrows, arrow)
		position = end
	}
	return output.String(), arrows
}

func tikZCDArrow(options string, row, col int) (diagramArrow, bool) {
	arrow := diagramArrow{fromRow: row, fromCol: col, style: "->"}
	direction := ""
	swap := false
	for _, option := range splitDiagramOptions(options) {
		option = strings.TrimSpace(option)
		if option == "" {
			continue
		}
		if strings.HasPrefix(option, `"`) {
			label, rest, ok := parseQuotedTikZLabel(option)
			if ok {
				if strings.Contains(rest, "swap") || strings.Contains(rest, "'") {
					arrow.lower = label
				} else {
					arrow.upper = label
				}
			}
			continue
		}
		switch {
		case option == "swap" || option == "'":
			swap = true
		case option == "dashed" || option == "dash":
			arrow.style = "-->"
		case strings.Contains(option, "hook"):
			arrow.style = "hook->"
		case option == "equals" || option == "equal":
			arrow.style = "="
		case isXYDirection(option):
			direction = option
		}
	}
	if swap && arrow.upper != "" && arrow.lower == "" {
		arrow.lower, arrow.upper = arrow.upper, ""
	}
	if direction == "" {
		direction = "r"
	}
	toRow, toCol, ok := xyDirectionTarget(row, col, direction)
	if !ok {
		return diagramArrow{}, false
	}
	arrow.toRow, arrow.toCol = toRow, toCol
	return arrow, true
}

func isXYDirection(value string) bool {
	if value == "" {
		return false
	}
	for _, direction := range value {
		if !strings.ContainsRune("udlr", direction) {
			return false
		}
	}
	return true
}

func parseQuotedTikZLabel(source string) (label, rest string, ok bool) {
	if len(source) < 2 || source[0] != '"' {
		return "", source, false
	}
	var output strings.Builder
	for position := 1; position < len(source); position++ {
		if source[position] == '"' && source[position-1] != '\\' {
			return output.String(), strings.TrimSpace(source[position+1:]), true
		}
		output.WriteByte(source[position])
	}
	return "", source, false
}

func splitDiagramOptions(source string) []string {
	var result []string
	start, braceDepth, bracketDepth := 0, 0, 0
	inQuote := false
	for position := 0; position < len(source); position++ {
		switch source[position] {
		case '"':
			if position == 0 || source[position-1] != '\\' {
				inQuote = !inQuote
			}
		case '{':
			if !inQuote {
				braceDepth++
			}
		case '}':
			if !inQuote && braceDepth > 0 {
				braceDepth--
			}
		case '[':
			if !inQuote {
				bracketDepth++
			}
		case ']':
			if !inQuote && bracketDepth > 0 {
				bracketDepth--
			}
		case ',':
			if !inQuote && braceDepth == 0 && bracketDepth == 0 {
				result = append(result, source[start:position])
				start = position + 1
			}
		}
	}
	return append(result, source[start:])
}

func (context *latexRenderContext) renderMatrixDiagram(diagram matrixDiagram, source string, display bool, kind string) (string, error) {
	if len(diagram.cells) == 0 || len(diagram.cells[0]) == 0 {
		return "", errors.New("empty diagram")
	}
	rows, cols := len(diagram.cells), len(diagram.cells[0])
	colWidths := make([]float64, cols)
	rowHeights := make([]float64, rows)
	for row := range diagram.cells {
		for col := range diagram.cells[row] {
			if diagram.cells[row][col].tex == "" {
				continue
			}
			glyph, err := context.mathDiagramGlyph(diagram.cells[row][col].tex)
			if err != nil {
				glyph = plainDiagramGlyph(diagram.cells[row][col].tex)
			}
			diagram.cells[row][col].glyph = glyph
			colWidths[col] = math.Max(colWidths[col], glyph.viewWidth+2*diagramNodePad)
			rowHeights[row] = math.Max(rowHeights[row], glyph.viewHeight+2*diagramNodePad)
		}
	}
	for col := range colWidths {
		colWidths[col] = math.Max(colWidths[col], 240)
	}
	for row := range rowHeights {
		rowHeights[row] = math.Max(rowHeights[row], 760)
	}
	colGaps := make([]float64, max(0, cols-1))
	rowGaps := make([]float64, max(0, rows-1))
	for index := range colGaps {
		colGaps[index] = 980
		if diagram.columnGap > 0 {
			colGaps[index] = diagram.columnGap
		}
	}
	for index := range rowGaps {
		rowGaps[index] = 1080
		if diagram.rowGap > 0 {
			rowGaps[index] = diagram.rowGap
		}
	}
	for index := range diagram.arrows {
		arrow := &diagram.arrows[index]
		if arrow.upper != "" {
			glyph, err := context.mathDiagramGlyph(arrow.upper)
			if err != nil {
				glyph = plainDiagramGlyph(arrow.upper)
			}
			arrow.upperGlyph = glyph
		}
		if arrow.lower != "" {
			glyph, err := context.mathDiagramGlyph(arrow.lower)
			if err != nil {
				glyph = plainDiagramGlyph(arrow.lower)
			}
			arrow.lowerGlyph = glyph
		}
		if arrow.fromRow == arrow.toRow && arrow.fromCol != arrow.toCol {
			left, right := min(arrow.fromCol, arrow.toCol), max(arrow.fromCol, arrow.toCol)
			labelWidth := math.Max(arrow.upperGlyph.viewWidth, arrow.lowerGlyph.viewWidth)
			baseWidth := (colWidths[left] + colWidths[right]) / 2
			for col := left + 1; col < right; col++ {
				baseWidth += colWidths[col]
			}
			for boundary := left; boundary < right; boundary++ {
				baseWidth += colGaps[boundary]
			}
			desired := math.Max(760, labelWidth+(colWidths[left]+colWidths[right])/2+560)
			if shortage := desired - baseWidth; shortage > 0 {
				addition := shortage / float64(right-left)
				for boundary := left; boundary < right; boundary++ {
					colGaps[boundary] += addition
				}
			}
			labelHeight := math.Max(arrow.upperGlyph.viewHeight, arrow.lowerGlyph.viewHeight)
			if labelHeight > 0 {
				rowHeights[arrow.fromRow] = math.Max(rowHeights[arrow.fromRow], 2*(labelHeight+220))
			}
		}
		if arrow.fromCol == arrow.toCol && absInt(arrow.toRow-arrow.fromRow) == 1 {
			boundary := min(arrow.fromRow, arrow.toRow)
			labelHeight := arrow.upperGlyph.viewHeight + arrow.lowerGlyph.viewHeight
			rowGaps[boundary] = math.Max(rowGaps[boundary], labelHeight+560)
		}
	}
	margin := 320.0
	xCenters := make([]float64, cols)
	yCenters := make([]float64, rows)
	x := margin
	for col := range colWidths {
		xCenters[col] = x + colWidths[col]/2
		x += colWidths[col]
		if col < len(colGaps) {
			x += colGaps[col]
		}
	}
	y := margin
	for row := range rowHeights {
		yCenters[row] = y + rowHeights[row]/2
		y += rowHeights[row]
		if row < len(rowGaps) {
			y += rowGaps[row]
		}
	}
	width, height := x+margin, y+margin
	var svg strings.Builder
	fmt.Fprintf(&svg, `<svg xmlns="http://www.w3.org/2000/svg" width="%.3fex" height="%.3fex" viewBox="0 0 %.3f %.3f" role="img" focusable="false" data-diagram="%s">`, width/diagramUnitsPerEx, height/diagramUnitsPerEx, width, height, template.HTMLEscapeString(kind))
	for _, arrow := range diagram.arrows {
		if arrow.fromRow < 0 || arrow.fromRow >= rows || arrow.toRow < 0 || arrow.toRow >= rows || arrow.fromCol < 0 || arrow.fromCol >= cols || arrow.toCol < 0 || arrow.toCol >= cols {
			continue
		}
		fromCell := diagram.cells[arrow.fromRow][arrow.fromCol]
		toCell := diagram.cells[arrow.toRow][arrow.toCol]
		fromWidth, fromHeight := diagramCellBounds(fromCell, colWidths[arrow.fromCol], rowHeights[arrow.fromRow])
		toWidth, toHeight := diagramCellBounds(toCell, colWidths[arrow.toCol], rowHeights[arrow.toRow])
		drawDiagramArrow(&svg, arrow,
			xCenters[arrow.fromCol], yCenters[arrow.fromRow], fromWidth, fromHeight,
			xCenters[arrow.toCol], yCenters[arrow.toRow], toWidth, toHeight)
	}
	for row := range diagram.cells {
		for col, cell := range diagram.cells[row] {
			if cell.glyph.markup == "" {
				continue
			}
			writeDiagramGlyph(&svg, cell.glyph, xCenters[col]-cell.glyph.viewWidth/2, yCenters[row]-cell.glyph.viewHeight/2)
		}
	}
	svg.WriteString(`</svg>`)
	className := "math-inline diagram"
	if display {
		className = "math-display diagram"
	}
	return `<span class="` + className + `" data-tex="` + template.HTMLEscapeString(source) + `">` + svg.String() + `</span>`, nil
}

func diagramCellBounds(cell diagramCell, columnWidth, rowHeight float64) (float64, float64) {
	if cell.glyph.markup == "" {
		return math.Min(columnWidth, 240), math.Min(rowHeight, 240)
	}
	return cell.glyph.viewWidth + 2*diagramNodePad, cell.glyph.viewHeight + 2*diagramNodePad
}

func drawDiagramArrow(output *strings.Builder, arrow diagramArrow, x1, y1, fromWidth, fromHeight, x2, y2, toWidth, toHeight float64) {
	dx, dy := x2-x1, y2-y1
	distance := math.Hypot(dx, dy)
	if distance <= 1 {
		return
	}
	ux, uy := dx/distance, dy/distance
	startTrim := rectangleRayDistance(fromWidth/2, fromHeight/2, ux, uy) + 75
	endTrim := rectangleRayDistance(toWidth/2, toHeight/2, ux, uy) + 95
	if startTrim+endTrim >= distance-40 {
		startTrim, endTrim = 40, 60
	}
	sx, sy := x1+ux*startTrim, y1+uy*startTrim
	ex, ey := x2-ux*endTrim, y2-uy*endTrim
	nx, ny := uy, -ux
	sx, sy = sx+nx*arrow.offset, sy+ny*arrow.offset
	ex, ey = ex+nx*arrow.offset, ey+ny*arrow.offset
	style := strings.ReplaceAll(strings.TrimSpace(arrow.style), " ", "")
	isEqual := strings.Contains(style, "=")
	isDashed := strings.Contains(style, "-->") || strings.Contains(style, "dashed")
	isHook := strings.Contains(style, "(") || strings.Contains(style, "hook")
	if isEqual {
		for _, offset := range []float64{-42, 42} {
			fmt.Fprintf(output, `<line x1="%.3f" y1="%.3f" x2="%.3f" y2="%.3f" fill="none" stroke="currentColor" stroke-width="38"/>`, sx+nx*offset, sy+ny*offset, ex+nx*offset, ey+ny*offset)
		}
	} else {
		dash := ""
		if isDashed {
			dash = ` stroke-dasharray="150 105"`
		}
		if arrow.bend != 0 {
			control1X, control1Y := sx+(ex-sx)/3+nx*arrow.bend, sy+(ey-sy)/3+ny*arrow.bend
			control2X, control2Y := sx+2*(ex-sx)/3+nx*arrow.bend, sy+2*(ey-sy)/3+ny*arrow.bend
			fmt.Fprintf(output, `<path d="M %.3f %.3f C %.3f %.3f %.3f %.3f %.3f %.3f" fill="none" stroke="currentColor" stroke-width="42" stroke-linecap="round"%s/>`, sx, sy, control1X, control1Y, control2X, control2Y, ex, ey, dash)
		} else {
			fmt.Fprintf(output, `<line x1="%.3f" y1="%.3f" x2="%.3f" y2="%.3f" fill="none" stroke="currentColor" stroke-width="42" stroke-linecap="round"%s/>`, sx, sy, ex, ey, dash)
		}
		if isHook {
			hookX, hookY := sx+ux*90, sy+uy*90
			fmt.Fprintf(output, `<path d="M %.3f %.3f C %.3f %.3f %.3f %.3f %.3f %.3f" fill="none" stroke="currentColor" stroke-width="42"/>`, sx+nx*110, sy+ny*110, sx-nx*80, sy-ny*80, hookX-nx*80, hookY-ny*80, hookX, hookY)
		}
		headUX, headUY := ux, uy
		if arrow.bend != 0 {
			control2X, control2Y := sx+2*(ex-sx)/3+nx*arrow.bend, sy+2*(ey-sy)/3+ny*arrow.bend
			if tangent := math.Hypot(ex-control2X, ey-control2Y); tangent > 0 {
				headUX, headUY = (ex-control2X)/tangent, (ey-control2Y)/tangent
			}
		}
		headNX, headNY := headUY, -headUX
		headLength, headWidth := 170.0, 105.0
		bx, by := ex-headUX*headLength, ey-headUY*headLength
		fmt.Fprintf(output, `<path d="M %.3f %.3f L %.3f %.3f L %.3f %.3f Z" fill="currentColor" stroke="none"/>`, ex, ey, bx+headNX*headWidth, by+headNY*headWidth, bx-headNX*headWidth, by-headNY*headWidth)
	}
	mx, my := (sx+ex)/2, (sy+ey)/2
	if arrow.bend != 0 {
		mx += nx * arrow.bend * 0.75
		my += ny * arrow.bend * 0.75
	}
	if arrow.upperGlyph.markup != "" {
		offset := arrow.upperGlyph.viewHeight/2 + 125
		writeDiagramGlyph(output, arrow.upperGlyph, mx+nx*offset-arrow.upperGlyph.viewWidth/2, my+ny*offset-arrow.upperGlyph.viewHeight/2)
	}
	if arrow.lowerGlyph.markup != "" {
		offset := arrow.lowerGlyph.viewHeight/2 + 125
		writeDiagramGlyph(output, arrow.lowerGlyph, mx-nx*offset-arrow.lowerGlyph.viewWidth/2, my-ny*offset-arrow.lowerGlyph.viewHeight/2)
	}
}

func rectangleRayDistance(halfWidth, halfHeight, ux, uy float64) float64 {
	distance := math.Inf(1)
	if math.Abs(ux) > 1e-9 {
		distance = math.Min(distance, halfWidth/math.Abs(ux))
	}
	if math.Abs(uy) > 1e-9 {
		distance = math.Min(distance, halfHeight/math.Abs(uy))
	}
	if math.IsInf(distance, 1) {
		return 0
	}
	return distance
}

func writeDiagramGlyph(output *strings.Builder, glyph diagramGlyph, x, y float64) {
	if glyph.markup == "" {
		return
	}
	fmt.Fprintf(output, `<g transform="translate(%.3f %.3f)">`, x-glyph.viewX, y-glyph.viewY)
	output.WriteString(glyph.markup)
	output.WriteString(`</g>`)
}

func (context *latexRenderContext) mathDiagramGlyph(source string) (diagramGlyph, error) {
	rendered := context.renderMath(source, false)
	root, err := html.Parse(strings.NewReader(`<html><body>` + rendered + `</body></html>`))
	if err != nil {
		return diagramGlyph{}, err
	}
	svg := findHTMLNode(root, "svg")
	if svg == nil {
		return diagramGlyph{}, errors.New("diagram label has no MathJax SVG")
	}
	viewBox := strings.Fields(strings.ReplaceAll(htmlAttribute(svg, "viewbox"), ",", " "))
	if len(viewBox) != 4 {
		return diagramGlyph{}, errors.New("diagram label has invalid MathJax viewBox")
	}
	values := make([]float64, 4)
	for index, value := range viewBox {
		values[index], err = strconv.ParseFloat(value, 64)
		if err != nil {
			return diagramGlyph{}, err
		}
	}
	if values[2] <= 0 || values[3] <= 0 {
		return diagramGlyph{}, errors.New("diagram label has empty MathJax viewBox")
	}
	var markup bytes.Buffer
	for child := svg.FirstChild; child != nil; child = child.NextSibling {
		if err := html.Render(&markup, child); err != nil {
			return diagramGlyph{}, err
		}
	}
	return diagramGlyph{markup: markup.String(), viewX: values[0], viewY: values[1], viewWidth: values[2], viewHeight: values[3]}, nil
}

func plainDiagramGlyph(source string) diagramGlyph {
	text := strings.TrimSpace(texFragmentToPlainText(source))
	if text == "" {
		text = strings.TrimSpace(source)
	}
	width := math.Max(440, float64(len([]rune(text)))*520)
	return diagramGlyph{
		markup:    `<text x="0" y="720" font-size="780px" font-family="serif">` + template.HTMLEscapeString(text) + `</text>`,
		viewWidth: width, viewHeight: 900,
	}
}

func diagramSourceFallback(kind, source string, err error) string {
	return `<div class="diagram-error">Unable to render ` + template.HTMLEscapeString(kind) + `: ` + template.HTMLEscapeString(err.Error()) + `</div><details class="source-fragment diagram-source"><summary>Diagram source</summary><code>` + template.HTMLEscapeString(strings.TrimSpace(source)) + `</code></details>`
}

func absInt(value int) int {
	if value < 0 {
		return -value
	}
	return value
}
