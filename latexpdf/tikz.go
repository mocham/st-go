package latexpdf

import (
	"errors"
	"fmt"
	"html/template"
	"math"
	"strconv"
	"strings"
	"unicode"
)

type tikZPoint struct {
	x, y float64
	name string
}

type tikZNode struct {
	name, tex, options   string
	x, y                 float64
	width, height        float64
	minimumWidth         float64
	draw, circle         bool
	anchor               string
	relation, relationTo string
	relationDistance     float64
	glyph                diagramGlyph
}

type tikZPathLabel struct {
	tex, anchor string
	x, y        float64
	glyph       diagramGlyph
}

type tikZPath struct {
	kind                 string
	points               []tikZPoint
	controls             [2]tikZPoint
	radius, gridX, gridY float64
	bend                 float64
	stroke, fill, dashed bool
	lineWidth            float64
	arrowStart, arrowEnd bool
	labels               []tikZPathLabel
}

type tikZCanvas struct {
	xUnit, yUnit, scale float64
	nodeDistance        float64
	nodes               []*tikZNode
	nodeByName          map[string]*tikZNode
	rawPaths            []tikZRawPath
	paths               []tikZPath
	unsupported         []string
	styles              map[string]string
	shiftX, shiftY      float64
}

type tikZRawPath struct {
	source         string
	shiftX, shiftY float64
}

func (context *latexRenderContext) renderTikZPicture(content, options string) string {
	canvas, err := parseTikZPicture(content, options)
	if err != nil {
		return diagramSourceFallback("TikZ", content, err)
	}
	rendered, err := context.renderTikZCanvas(canvas, content)
	if err != nil {
		return diagramSourceFallback("TikZ", content, err)
	}
	return rendered
}

func parseTikZPicture(content, options string) (*tikZCanvas, error) {
	canvas := &tikZCanvas{
		xUnit: 2700, yUnit: 2700, scale: 1, nodeDistance: 1350,
		nodeByName: make(map[string]*tikZNode), styles: make(map[string]string),
	}
	canvas.applyPictureOptions(options)
	content = expandSimpleTikZForeach(stripLatexComments(content))
	canvas.parseContent(content, 0, 0)
	if len(canvas.nodes) == 0 && len(canvas.rawPaths) == 0 {
		return nil, errors.New("TikZ picture contains no supported nodes or paths")
	}
	return canvas, nil
}

func (canvas *tikZCanvas) parseContent(content string, shiftX, shiftY float64) {
	for {
		start := strings.Index(content, `\begin{scope}`)
		if start < 0 {
			canvas.parseFlatContent(content, shiftX, shiftY)
			return
		}
		canvas.parseFlatContent(content[:start], shiftX, shiftY)
		scopeSource := content[start:]
		environment, beginEnd, ok := parseEnvironmentStart(scopeSource)
		if !ok || environment != "scope" {
			canvas.unsupported = append(canvas.unsupported, strings.TrimSpace(scopeSource))
			return
		}
		body, end, found := extractEnvironment(scopeSource, "scope", beginEnd)
		if !found {
			canvas.unsupported = append(canvas.unsupported, strings.TrimSpace(scopeSource))
			return
		}
		options := environmentOptions(scopeSource, "scope", beginEnd)
		dx, dy := canvas.scopeShift(options)
		canvas.parseContent(body, shiftX+dx, shiftY+dy)
		content = scopeSource[end:]
	}
}

func (canvas *tikZCanvas) parseFlatContent(content string, shiftX, shiftY float64) {
	previousX, previousY := canvas.shiftX, canvas.shiftY
	canvas.shiftX, canvas.shiftY = shiftX, shiftY
	defer func() { canvas.shiftX, canvas.shiftY = previousX, previousY }()
	for _, command := range splitTikZCommands(content) {
		command = strings.TrimSpace(command)
		if command == "" {
			continue
		}
		name, _ := readTeXCommand(command, 0)
		switch name {
		case "node", "coordinate":
			node, err := canvas.parseNode(command, name == "coordinate")
			if err != nil {
				canvas.unsupported = append(canvas.unsupported, command)
				continue
			}
			canvas.nodes = append(canvas.nodes, node)
			if node.name != "" {
				canvas.nodeByName[node.name] = node
			}
		case "draw", "path", "fill", "filldraw":
			canvas.rawPaths = append(canvas.rawPaths, tikZRawPath{source: command, shiftX: shiftX, shiftY: shiftY})
		default:
			canvas.unsupported = append(canvas.unsupported, command)
		}
	}
}

func (canvas *tikZCanvas) scopeShift(source string) (float64, float64) {
	var x, y float64
	for _, option := range splitDiagramOptions(source) {
		name, value, ok := strings.Cut(strings.TrimSpace(option), "=")
		if !ok {
			continue
		}
		switch strings.TrimSpace(name) {
		case "xshift":
			x, _ = tikZDimension(strings.TrimSpace(value), canvas.xUnit)
		case "yshift":
			value, parsed := tikZDimension(strings.TrimSpace(value), canvas.yUnit)
			if parsed {
				y = -value
			}
		}
	}
	return x, y
}

func (canvas *tikZCanvas) applyPictureOptions(source string) {
	for _, option := range splitDiagramOptions(source) {
		name, value, hasValue := strings.Cut(strings.TrimSpace(option), "=")
		name, value = strings.TrimSpace(name), strings.TrimSpace(value)
		switch name {
		case "x":
			if hasValue {
				if parsed, ok := tikZDimension(value, canvas.xUnit); ok {
					canvas.xUnit = parsed
				}
			}
		case "y":
			if hasValue {
				if parsed, ok := tikZDimension(value, canvas.yUnit); ok {
					canvas.yUnit = parsed
				}
			}
		case "scale":
			if parsed, err := strconv.ParseFloat(value, 64); hasValue && err == nil && parsed > 0 {
				canvas.scale = parsed
			}
		case "node distance":
			if parsed, ok := tikZDimension(value, canvas.nodeDistance); hasValue && ok {
				canvas.nodeDistance = parsed
			}
		default:
			if strings.HasSuffix(name, "/.style") && hasValue {
				canvas.styles[strings.TrimSpace(strings.TrimSuffix(name, "/.style"))] = strings.TrimSpace(strings.Trim(value, "{}"))
			}
		}
	}
	canvas.xUnit *= canvas.scale
	canvas.yUnit *= canvas.scale
	canvas.nodeDistance *= canvas.scale
}

func (canvas *tikZCanvas) parseNode(source string, coordinate bool) (*tikZNode, error) {
	_, position := readTeXCommand(source, 0)
	node := &tikZNode{}
	position = skipTeXSpace(source, position)
	if position < len(source) && source[position] == '[' {
		options, end, ok := parseDelimited(source, position, '[', ']')
		if !ok {
			return nil, errors.New("invalid TikZ node options")
		}
		node.options = canvas.expandStyleOptions(options)
		position = end
	}
	position = skipTeXSpace(source, position)
	if position < len(source) && source[position] == '(' {
		name, end, ok := parseDelimited(source, position, '(', ')')
		if ok && !strings.Contains(name, ",") {
			node.name = strings.TrimSpace(name)
			position = end
		}
	}
	position = skipTeXSpace(source, position)
	if strings.HasPrefix(source[position:], "at") && (position+2 == len(source) || unicode.IsSpace(rune(source[position+2])) || source[position+2] == '(') {
		position = skipTeXSpace(source, position+2)
		point, end, ok := canvas.parseCoordinateAt(source, position)
		if !ok {
			return nil, errors.New("invalid TikZ node coordinate")
		}
		node.x, node.y = point.x, point.y
		if point.name != "" {
			node.relation, node.relationTo = "same", point.name
		}
		position = end
	}
	position = skipTeXSpace(source, position)
	if !coordinate && position < len(source) && source[position] == '{' {
		body, end, ok := parseBraced(source, position)
		if !ok {
			return nil, errors.New("invalid TikZ node body")
		}
		node.tex = tikZNodeMath(body)
		position = end
	}
	if strings.TrimSpace(source[position:]) != "" {
		return nil, errors.New("unsupported TikZ node suffix")
	}
	node.applyOptions(canvas.nodeDistance)
	node.x += canvas.shiftX
	node.y += canvas.shiftY
	return node, nil
}

func (canvas *tikZCanvas) expandStyleOptions(source string) string {
	parts := splitDiagramOptions(source)
	var expanded []string
	for _, part := range parts {
		name := strings.TrimSpace(part)
		if style, ok := canvas.styles[name]; ok {
			expanded = append(expanded, style)
		} else {
			expanded = append(expanded, part)
		}
	}
	return strings.Join(expanded, ",")
}

func (node *tikZNode) applyOptions(defaultDistance float64) {
	for _, option := range splitDiagramOptions(node.options) {
		option = strings.TrimSpace(option)
		name, value, hasValue := strings.Cut(option, "=")
		name, value = strings.TrimSpace(name), strings.TrimSpace(value)
		switch name {
		case "draw":
			node.draw = true
		case "circle":
			node.circle = true
		case "minimum width":
			if parsed, ok := tikZDimension(value, defaultDistance); hasValue && ok {
				node.minimumWidth = parsed
			}
		case "minimum height":
			if parsed, ok := tikZDimension(value, defaultDistance); hasValue && ok {
				node.height = parsed
			}
		case "above", "below", "left", "right":
			if !hasValue {
				node.anchor = name
				continue
			}
			before, target, found := strings.Cut(value, "of")
			if !found {
				node.anchor = name
				if parsed, ok := tikZDimension(value, defaultDistance); ok {
					node.relationDistance = parsed
				}
				continue
			}
			node.relation, node.relationTo = name, strings.TrimSpace(target)
			node.relationDistance = defaultDistance
			if distance := strings.TrimSpace(before); distance != "" {
				if parsed, ok := tikZDimension(distance, defaultDistance); ok {
					node.relationDistance = parsed
				}
			}
		case "anchor":
			if hasValue {
				node.anchor = value
			}
		}
	}
}

func (context *latexRenderContext) renderTikZCanvas(canvas *tikZCanvas, source string) (string, error) {
	for _, node := range canvas.nodes {
		if node.tex != "" {
			glyph, err := context.mathDiagramGlyph(node.tex)
			if err != nil {
				glyph = plainDiagramGlyph(node.tex)
			}
			node.glyph = glyph
			node.width = math.Max(node.minimumWidth, glyph.viewWidth+300)
			node.height = math.Max(node.height, glyph.viewHeight+260)
		} else {
			node.width, node.height = math.Max(node.minimumWidth, 120), 120
		}
		if node.circle {
			diameter := math.Max(node.width, node.height)
			node.width, node.height = diameter, diameter
		}
		if node.relationTo != "" {
			base := canvas.nodeByName[strings.TrimSuffix(strings.TrimSpace(node.relationTo), ".center")]
			if base == nil {
				continue
			}
			distance := node.relationDistance
			if distance <= 0 {
				distance = canvas.nodeDistance
			}
			switch node.relation {
			case "right":
				node.x, node.y = base.x+(base.width+node.width)/2+distance, base.y
			case "left":
				node.x, node.y = base.x-(base.width+node.width)/2-distance, base.y
			case "above":
				node.x, node.y = base.x, base.y-(base.height+node.height)/2-distance
			case "below":
				node.x, node.y = base.x, base.y+(base.height+node.height)/2+distance
			case "same":
				node.x, node.y = base.x, base.y
			}
		} else if node.anchor != "" {
			distance := node.relationDistance
			switch node.anchor {
			case "right", "east":
				node.x += node.width/2 + distance
			case "left", "west":
				node.x -= node.width/2 + distance
			case "above", "north":
				node.y -= node.height/2 + distance
			case "below", "south":
				node.y += node.height/2 + distance
			}
		}
	}
	for _, raw := range canvas.rawPaths {
		canvas.shiftX, canvas.shiftY = raw.shiftX, raw.shiftY
		path, err := canvas.parsePath(raw.source)
		if err != nil {
			canvas.unsupported = append(canvas.unsupported, raw.source)
			continue
		}
		for index := range path.labels {
			glyph, err := context.mathDiagramGlyph(path.labels[index].tex)
			if err != nil {
				glyph = plainDiagramGlyph(path.labels[index].tex)
			}
			path.labels[index].glyph = glyph
		}
		adjustTikZBoxedPathLabels(&path, canvas.nodeByName)
		canvas.paths = append(canvas.paths, path)
	}
	if len(canvas.nodes) == 0 && len(canvas.paths) == 0 {
		return "", errors.New("TikZ picture contains no drawable supported content")
	}
	minX, minY := math.Inf(1), math.Inf(1)
	maxX, maxY := math.Inf(-1), math.Inf(-1)
	include := func(x1, y1, x2, y2 float64) {
		minX, minY = math.Min(minX, x1), math.Min(minY, y1)
		maxX, maxY = math.Max(maxX, x2), math.Max(maxY, y2)
	}
	for _, node := range canvas.nodes {
		include(node.x-node.width/2, node.y-node.height/2, node.x+node.width/2, node.y+node.height/2)
	}
	for _, path := range canvas.paths {
		for _, point := range path.points {
			include(point.x-path.radius, point.y-path.radius, point.x+path.radius, point.y+path.radius)
		}
		for _, point := range path.controls {
			include(point.x, point.y, point.x, point.y)
		}
		for _, label := range path.labels {
			x, y := tikZLabelTopLeft(label)
			include(x, y, x+label.glyph.viewWidth, y+label.glyph.viewHeight)
		}
	}
	if math.IsInf(minX, 1) {
		return "", errors.New("TikZ picture has empty bounds")
	}
	margin := 260.0
	width, height := maxX-minX+2*margin, maxY-minY+2*margin
	shiftX, shiftY := -minX+margin, -minY+margin
	var svg strings.Builder
	fmt.Fprintf(&svg, `<svg xmlns="http://www.w3.org/2000/svg" width="%.3fex" height="%.3fex" viewBox="0 0 %.3f %.3f" role="img" focusable="false" data-diagram="tikz" data-unsupported="%d">`, width/diagramUnitsPerEx, height/diagramUnitsPerEx, width, height, len(canvas.unsupported))
	if len(canvas.unsupported) > 0 {
		fmt.Fprintf(&svg, `<title>%d unsupported TikZ command(s): %s</title>`, len(canvas.unsupported), template.HTMLEscapeString(collapseWhitespace(strings.Join(canvas.unsupported, " | "))))
	}
	fmt.Fprintf(&svg, `<g transform="translate(%.3f %.3f)">`, shiftX, shiftY)
	for _, path := range canvas.paths {
		writeTikZPath(&svg, path, canvas.nodeByName)
	}
	for _, node := range canvas.nodes {
		writeTikZNode(&svg, node)
	}
	for _, path := range canvas.paths {
		for _, label := range path.labels {
			x, y := tikZLabelTopLeft(label)
			writeDiagramGlyph(&svg, label.glyph, x, y)
		}
	}
	svg.WriteString(`</g></svg>`)
	return `<span class="math-display diagram tikz-diagram" data-tex="` + template.HTMLEscapeString(source) + `">` + svg.String() + `</span>`, nil
}

func adjustTikZBoxedPathLabels(path *tikZPath, nodes map[string]*tikZNode) {
	if len(path.points) < 2 {
		return
	}
	start, end := nodes[path.points[0].name], nodes[path.points[len(path.points)-1].name]
	if (start == nil || !start.draw) && (end == nil || !end.draw) {
		return
	}
	var halfWidth, halfHeight float64
	for _, node := range []*tikZNode{start, end} {
		if node != nil {
			halfWidth = math.Max(halfWidth, node.width/2)
			halfHeight = math.Max(halfHeight, node.height/2)
		}
	}
	dx := math.Abs(path.points[len(path.points)-1].x - path.points[0].x)
	dy := math.Abs(path.points[len(path.points)-1].y - path.points[0].y)
	for index := range path.labels {
		if dx >= dy {
			switch path.labels[index].anchor {
			case "above", "north":
				path.labels[index].y -= halfHeight
			case "below", "south", "below left", "below right":
				path.labels[index].y += halfHeight
			}
		} else {
			switch path.labels[index].anchor {
			case "left", "west", "below left":
				path.labels[index].x -= halfWidth
			case "right", "east", "below right":
				path.labels[index].x += halfWidth
			}
		}
	}
}

func writeTikZNode(output *strings.Builder, node *tikZNode) {
	if node.draw {
		if node.circle {
			writeTikZCircle(output, node.x, node.y, node.width/2, true, false, 38)
		} else {
			fmt.Fprintf(output, `<rect x="%.3f" y="%.3f" width="%.3f" height="%.3f" rx="90" fill="none" stroke="currentColor" stroke-width="38"/>`, node.x-node.width/2, node.y-node.height/2, node.width, node.height)
		}
	}
	if node.glyph.markup != "" {
		writeDiagramGlyph(output, node.glyph, node.x-node.glyph.viewWidth/2, node.y-node.glyph.viewHeight/2)
	}
}

func tikZLabelTopLeft(label tikZPathLabel) (float64, float64) {
	x, y := label.x-label.glyph.viewWidth/2, label.y-label.glyph.viewHeight/2
	switch label.anchor {
	case "above", "north":
		y -= label.glyph.viewHeight/2 + 150
	case "below", "south":
		y += label.glyph.viewHeight/2 + 150
	case "left", "west", "below left":
		x -= label.glyph.viewWidth/2 + 150
		if label.anchor == "below left" {
			y += label.glyph.viewHeight/2 + 90
		}
	case "right", "east", "below right":
		x += label.glyph.viewWidth/2 + 150
		if label.anchor == "below right" {
			y += label.glyph.viewHeight/2 + 90
		}
	}
	return x, y
}

func writeTikZPath(output *strings.Builder, path tikZPath, nodes map[string]*tikZNode) {
	if len(path.points) == 0 {
		return
	}
	if path.kind == "circle" {
		writeTikZCircle(output, path.points[0].x, path.points[0].y, path.radius, path.stroke, path.fill, path.lineWidth)
		return
	}
	points := append([]tikZPoint(nil), path.points...)
	if len(points) >= 2 {
		trimTikZNamedEndpoints(points, nodes)
	}
	fill := "none"
	if path.fill {
		fill = "currentColor"
	}
	stroke := "none"
	if path.stroke {
		stroke = "currentColor"
	}
	dash := ""
	if path.dashed {
		dash = ` stroke-dasharray="150 105"`
	}
	var data strings.Builder
	fmt.Fprintf(&data, "M %.3f %.3f", points[0].x, points[0].y)
	switch path.kind {
	case "curve":
		fmt.Fprintf(&data, " C %.3f %.3f %.3f %.3f %.3f %.3f", path.controls[0].x, path.controls[0].y, path.controls[1].x, path.controls[1].y, points[1].x, points[1].y)
	case "grid":
		if len(points) >= 2 && path.gridX > 0 && path.gridY > 0 {
			minX, maxX := math.Min(points[0].x, points[1].x), math.Max(points[0].x, points[1].x)
			minY, maxY := math.Min(points[0].y, points[1].y), math.Max(points[0].y, points[1].y)
			data.Reset()
			for x := minX; x <= maxX+path.gridX*1e-6; x += path.gridX {
				fmt.Fprintf(&data, "M %.3f %.3f L %.3f %.3f ", x, minY, x, maxY)
			}
			for y := minY; y <= maxY+path.gridY*1e-6; y += path.gridY {
				fmt.Fprintf(&data, "M %.3f %.3f L %.3f %.3f ", minX, y, maxX, y)
			}
		}
	case "rectangle":
		if len(points) >= 2 {
			fmt.Fprintf(&data, " L %.3f %.3f L %.3f %.3f L %.3f %.3f Z", points[1].x, points[0].y, points[1].x, points[1].y, points[0].x, points[1].y)
		}
	default:
		for _, point := range points[1:] {
			fmt.Fprintf(&data, " L %.3f %.3f", point.x, point.y)
		}
	}
	fmt.Fprintf(output, `<path d="%s" fill="%s" stroke="%s" stroke-width="%.3f" stroke-linecap="round" stroke-linejoin="round"%s/>`, data.String(), fill, stroke, path.lineWidth, dash)
	if path.arrowEnd && len(points) >= 2 {
		previous := points[len(points)-2]
		if path.kind == "curve" {
			previous = path.controls[1]
		}
		writeTikZArrowHead(output, previous, points[len(points)-1])
	}
	if path.arrowStart && len(points) >= 2 {
		next := points[1]
		if path.kind == "curve" {
			next = path.controls[0]
		}
		writeTikZArrowHead(output, next, points[0])
	}
}

func trimTikZNamedEndpoints(points []tikZPoint, nodes map[string]*tikZNode) {
	if len(points) < 2 {
		return
	}
	if node := nodes[points[0].name]; node != nil {
		dx, dy := points[1].x-points[0].x, points[1].y-points[0].y
		if distance := math.Hypot(dx, dy); distance > 0 {
			trim := rectangleRayDistance(node.width/2, node.height/2, dx/distance, dy/distance) + 45
			points[0].x += dx / distance * trim
			points[0].y += dy / distance * trim
		}
	}
	last := len(points) - 1
	if node := nodes[points[last].name]; node != nil {
		dx, dy := points[last-1].x-points[last].x, points[last-1].y-points[last].y
		if distance := math.Hypot(dx, dy); distance > 0 {
			trim := rectangleRayDistance(node.width/2, node.height/2, dx/distance, dy/distance) + 70
			points[last].x += dx / distance * trim
			points[last].y += dy / distance * trim
		}
	}
}

func writeTikZArrowHead(output *strings.Builder, from, to tikZPoint) {
	dx, dy := to.x-from.x, to.y-from.y
	distance := math.Hypot(dx, dy)
	if distance <= 0 {
		return
	}
	ux, uy := dx/distance, dy/distance
	nx, ny := uy, -ux
	bx, by := to.x-ux*170, to.y-uy*170
	fmt.Fprintf(output, `<path d="M %.3f %.3f L %.3f %.3f L %.3f %.3f Z" fill="currentColor" stroke="none"/>`, to.x, to.y, bx+nx*100, by+ny*100, bx-nx*100, by-ny*100)
}

func writeTikZCircle(output *strings.Builder, x, y, radius float64, stroke, fill bool, lineWidth float64) {
	k := radius * 0.5522847498
	fillValue, strokeValue := "none", "none"
	if fill {
		fillValue = "currentColor"
	}
	if stroke {
		strokeValue = "currentColor"
	}
	fmt.Fprintf(output, `<path d="M %.3f %.3f C %.3f %.3f %.3f %.3f %.3f %.3f C %.3f %.3f %.3f %.3f %.3f %.3f C %.3f %.3f %.3f %.3f %.3f %.3f C %.3f %.3f %.3f %.3f %.3f %.3f Z" fill="%s" stroke="%s" stroke-width="%.3f"/>`,
		x+radius, y, x+radius, y+k, x+k, y+radius, x, y+radius,
		x-k, y+radius, x-radius, y+k, x-radius, y,
		x-radius, y-k, x-k, y-radius, x, y-radius,
		x+k, y-radius, x+radius, y-k, x+radius, y,
		fillValue, strokeValue, lineWidth)
}

func (canvas *tikZCanvas) parsePath(source string) (tikZPath, error) {
	command, position := readTeXCommand(source, 0)
	path := tikZPath{kind: "line", lineWidth: 38}
	switch command {
	case "draw", "path":
		path.stroke = true
	case "fill":
		path.fill = true
	case "filldraw":
		path.fill, path.stroke = true, true
	default:
		return path, errors.New("unsupported TikZ path command")
	}
	position = skipTeXSpace(source, position)
	options := ""
	if position < len(source) && source[position] == '[' {
		var ok bool
		options, position, ok = parseDelimited(source, position, '[', ']')
		if !ok {
			return path, errors.New("invalid TikZ path options")
		}
	}
	decorationText := ""
	for _, option := range splitDiagramOptions(options) {
		option = strings.TrimSpace(option)
		switch {
		case option == "->" || strings.Contains(option, "Stealth"):
			path.arrowEnd = true
		case option == "<-":
			path.arrowStart = true
		case option == "<->":
			path.arrowStart, path.arrowEnd = true, true
		case option == "dashed" || option == "dotted":
			path.dashed = true
		case option == "thick":
			path.lineWidth = 55
		case option == "very thick":
			path.lineWidth = 75
		case strings.HasPrefix(option, "fill="):
			path.fill = true
		case strings.HasPrefix(option, "step="):
			if value, ok := tikZDimension(strings.TrimSpace(strings.TrimPrefix(option, "step=")), canvas.xUnit); ok {
				path.gridX, path.gridY = value, value
			}
		case strings.HasPrefix(option, "xstep="):
			path.gridX, _ = tikZDimension(strings.TrimSpace(strings.TrimPrefix(option, "xstep=")), canvas.xUnit)
		case strings.HasPrefix(option, "ystep="):
			path.gridY, _ = tikZDimension(strings.TrimSpace(strings.TrimPrefix(option, "ystep=")), canvas.yUnit)
		case strings.HasPrefix(option, "decoration="):
			decorationText = tikZDecorationText(strings.TrimSpace(strings.TrimPrefix(option, "decoration=")))
		case strings.HasPrefix(option, "bend right"):
			path.bend = tikZBendAngle(option, 20)
		case strings.HasPrefix(option, "bend left"):
			path.bend = -tikZBendAngle(option, 20)
		case strings.HasPrefix(option, "line width="):
			if value, ok := tikZDimension(strings.TrimSpace(strings.TrimPrefix(option, "line width=")), 38); ok {
				path.lineWidth = value
			}
		}
	}
	position = skipTeXSpace(source, position)
	if strings.HasPrefix(source[position:], "plot") {
		return canvas.parsePlotPath(source, position+len("plot"), options, path)
	}
	first, end, ok := canvas.parseCoordinateAt(source, position)
	if !ok {
		return path, errors.New("TikZ path has no initial coordinate")
	}
	path.points = append(path.points, first)
	position = end
	for position < len(source) {
		position = skipTeXSpace(source, position)
		if strings.HasPrefix(source[position:], "node") {
			label, next, parsed := canvas.parsePathLabel(source, position, path.points[len(path.points)-1])
			if !parsed {
				break
			}
			path.labels = append(path.labels, label)
			position = next
			continue
		}
		if strings.HasPrefix(source[position:], "rectangle") {
			position = skipTeXSpace(source, position+len("rectangle"))
			point, next, parsed := canvas.parseCoordinateAt(source, position)
			if !parsed {
				return path, errors.New("invalid TikZ rectangle")
			}
			path.kind = "rectangle"
			path.points = append(path.points, point)
			position = next
			continue
		}
		if strings.HasPrefix(source[position:], "grid") {
			position = skipTeXSpace(source, position+len("grid"))
			point, next, parsed := canvas.parseCoordinateAt(source, position)
			if !parsed {
				return path, errors.New("invalid TikZ grid endpoint")
			}
			path.kind = "grid"
			if path.gridX <= 0 {
				path.gridX = canvas.xUnit
			}
			if path.gridY <= 0 {
				path.gridY = canvas.yUnit
			}
			path.points = append(path.points, point)
			position = next
			continue
		}
		if strings.HasPrefix(source[position:], "circle") {
			position = skipTeXSpace(source, position+len("circle"))
			value, next, parsed := parseDelimited(source, position, '(', ')')
			if !parsed {
				return path, errors.New("invalid TikZ circle")
			}
			radius, parsed := tikZDimension(strings.TrimSpace(value), canvas.xUnit)
			if !parsed {
				return path, errors.New("invalid TikZ circle radius")
			}
			path.kind, path.radius, position = "circle", radius, next
			continue
		}
		if strings.HasPrefix(source[position:], "arc") {
			position = skipTeXSpace(source, position+len("arc"))
			value, next, parsed := parseDelimited(source, position, '(', ')')
			if !parsed {
				return path, errors.New("invalid TikZ arc")
			}
			points, parsed := canvas.tikZArcPoints(path.points[len(path.points)-1], value)
			if !parsed {
				return path, errors.New("unsupported TikZ arc")
			}
			path.points = append(path.points, points...)
			if decorationText != "" && len(path.points) > 1 {
				middle := path.points[len(path.points)/2]
				path.labels = append(path.labels, tikZPathLabel{tex: tikZNodeMath(strings.ReplaceAll(strings.ReplaceAll(decorationText, "{$", "$"), "$}", "$")), x: middle.x, y: middle.y})
			}
			position = next
			continue
		}
		if strings.HasPrefix(source[position:], "..") {
			position = skipTeXSpace(source, position+2)
			if !strings.HasPrefix(source[position:], "controls") {
				return path, errors.New("unsupported TikZ curve")
			}
			position = skipTeXSpace(source, position+len("controls"))
			control1, next, parsed := canvas.parseCoordinateAt(source, position)
			if !parsed {
				return path, errors.New("invalid first TikZ control point")
			}
			position = skipTeXSpace(source, next)
			control2 := control1
			if strings.HasPrefix(source[position:], "and") {
				position = skipTeXSpace(source, position+len("and"))
				control2, next, parsed = canvas.parseCoordinateAt(source, position)
				if !parsed {
					return path, errors.New("invalid second TikZ control point")
				}
				position = skipTeXSpace(source, next)
			}
			if !strings.HasPrefix(source[position:], "..") {
				return path, errors.New("unterminated TikZ curve controls")
			}
			position = skipTeXSpace(source, position+2)
			point, next, parsed := canvas.parseCoordinateAt(source, position)
			if !parsed {
				return path, errors.New("invalid TikZ curve endpoint")
			}
			path.kind, path.controls = "curve", [2]tikZPoint{control1, control2}
			path.points = append(path.points, point)
			position = next
			continue
		}
		if strings.HasPrefix(source[position:], "--") {
			position = skipTeXSpace(source, position+2)
			var pending []tikZPathLabel
			for strings.HasPrefix(source[position:], "node") {
				label, next, parsed := canvas.parsePathLabel(source, position, tikZPoint{})
				if !parsed {
					break
				}
				pending = append(pending, label)
				position = skipTeXSpace(source, next)
			}
			point, next, parsed := canvas.parseCoordinateAt(source, position)
			if !parsed {
				return path, errors.New("invalid TikZ line endpoint")
			}
			previous := path.points[len(path.points)-1]
			if path.bend != 0 && len(path.points) == 1 {
				path.kind = "curve"
				path.controls = tikZBendControls(previous, point, path.bend)
			}
			for index := range pending {
				pending[index].x, pending[index].y = tikZPathMidpoint(previous, point, path)
				path.labels = append(path.labels, pending[index])
			}
			path.points = append(path.points, point)
			position = next
			continue
		}
		if strings.HasPrefix(source[position:], "to") && (position+2 == len(source) || unicode.IsSpace(rune(source[position+2])) || strings.HasPrefix(source[position+2:], "node")) {
			position = skipTeXSpace(source, position+2)
			var pending []tikZPathLabel
			for strings.HasPrefix(source[position:], "node") {
				label, next, parsed := canvas.parsePathLabel(source, position, tikZPoint{})
				if !parsed {
					break
				}
				pending = append(pending, label)
				position = skipTeXSpace(source, next)
			}
			point, next, parsed := canvas.parseCoordinateAt(source, position)
			if !parsed {
				return path, errors.New("invalid TikZ to-path endpoint")
			}
			previous := path.points[len(path.points)-1]
			for index := range pending {
				pending[index].x, pending[index].y = (previous.x+point.x)/2, (previous.y+point.y)/2
				path.labels = append(path.labels, pending[index])
			}
			path.points = append(path.points, point)
			position = next
			continue
		}
		break
	}
	if strings.TrimSpace(source[position:]) != "" {
		return path, errors.New("unsupported TikZ path syntax")
	}
	return path, nil
}

func (canvas *tikZCanvas) tikZArcPoints(first tikZPoint, source string) ([]tikZPoint, bool) {
	parts := strings.Split(source, ":")
	if len(parts) != 3 {
		return nil, false
	}
	start, okStart := parseTikZNumber(parts[0])
	end, okEnd := parseTikZNumber(parts[1])
	radiusX, okX := tikZDimension(parts[2], canvas.xUnit)
	radiusY, okY := tikZDimension(parts[2], canvas.yUnit)
	if !okStart || !okEnd || !okX || !okY || radiusX <= 0 || radiusY <= 0 {
		return nil, false
	}
	startRadians := start * math.Pi / 180
	centerX := first.x - radiusX*math.Cos(startRadians)
	centerY := first.y + radiusY*math.Sin(startRadians)
	steps := max(8, int(math.Ceil(math.Abs(end-start)/7.5)))
	points := make([]tikZPoint, 0, steps)
	for index := 1; index <= steps; index++ {
		angle := (start + (end-start)*float64(index)/float64(steps)) * math.Pi / 180
		points = append(points, tikZPoint{x: centerX + radiusX*math.Cos(angle), y: centerY - radiusY*math.Sin(angle)})
	}
	return points, true
}

func tikZDecorationText(source string) string {
	source = strings.TrimSpace(source)
	if len(source) >= 2 && source[0] == '{' {
		if inner, end, ok := parseBraced(source, 0); ok && skipTeXSpace(source, end) == len(source) {
			source = inner
		}
	}
	for _, option := range splitDiagramOptions(source) {
		name, value, ok := strings.Cut(strings.TrimSpace(option), "=")
		if !ok || strings.TrimSpace(name) != "text" {
			continue
		}
		value = strings.TrimSpace(value)
		if len(value) >= 2 && value[0] == '{' {
			if text, end, parsed := parseBraced(value, 0); parsed && skipTeXSpace(value, end) == len(value) {
				return text
			}
		}
		return value
	}
	return ""
}

func tikZBendAngle(option string, fallback float64) float64 {
	_, value, ok := strings.Cut(option, "=")
	if !ok {
		return fallback
	}
	parsed, err := strconv.ParseFloat(strings.TrimSpace(value), 64)
	if err != nil {
		return fallback
	}
	return parsed
}

func tikZBendControls(from, to tikZPoint, angle float64) [2]tikZPoint {
	dx, dy := to.x-from.x, to.y-from.y
	distance := math.Hypot(dx, dy)
	if distance == 0 {
		return [2]tikZPoint{from, to}
	}
	nx, ny := dy/distance, -dx/distance
	offset := math.Tan(angle*math.Pi/180) * distance * 0.35
	return [2]tikZPoint{
		{x: from.x + dx/3 + nx*offset, y: from.y + dy/3 + ny*offset},
		{x: from.x + 2*dx/3 + nx*offset, y: from.y + 2*dy/3 + ny*offset},
	}
}

func tikZPathMidpoint(from, to tikZPoint, path tikZPath) (float64, float64) {
	if path.kind != "curve" {
		return (from.x + to.x) / 2, (from.y + to.y) / 2
	}
	return (from.x + 3*path.controls[0].x + 3*path.controls[1].x + to.x) / 8,
		(from.y + 3*path.controls[0].y + 3*path.controls[1].y + to.y) / 8
}

func (canvas *tikZCanvas) parsePlotPath(source string, position int, options string, path tikZPath) (tikZPath, error) {
	position = skipTeXSpace(source, position)
	coordinate, end, ok := parseDelimited(source, position, '(', ')')
	if !ok {
		return path, errors.New("invalid TikZ plot coordinate")
	}
	if strings.TrimSpace(source[end:]) != "" {
		return path, errors.New("unsupported TikZ plot suffix")
	}
	domainStart, domainEnd, samples := -1.0, 1.0, 60
	for _, option := range splitDiagramOptions(options) {
		name, value, hasValue := strings.Cut(strings.TrimSpace(option), "=")
		if !hasValue {
			continue
		}
		switch strings.TrimSpace(name) {
		case "domain":
			bounds := strings.SplitN(strings.TrimSpace(value), ":", 2)
			if len(bounds) == 2 {
				if first, parsed := parseTikZNumber(bounds[0]); parsed {
					domainStart = first
				}
				if last, parsed := parseTikZNumber(bounds[1]); parsed {
					domainEnd = last
				}
			}
		case "samples":
			if parsed, err := strconv.Atoi(strings.TrimSpace(value)); err == nil && parsed >= 2 && parsed <= 400 {
				samples = parsed
			}
		}
	}
	parts := splitDiagramOptions(coordinate)
	if len(parts) != 2 {
		return path, errors.New("TikZ plot coordinate must have two components")
	}
	for index := 0; index < samples; index++ {
		parameter := domainStart + (domainEnd-domainStart)*float64(index)/float64(samples-1)
		replacement := strconv.FormatFloat(parameter, 'g', 12, 64)
		xExpression := strings.ReplaceAll(strings.Trim(strings.TrimSpace(parts[0]), "{}"), `\x`, replacement)
		yExpression := strings.ReplaceAll(strings.Trim(strings.TrimSpace(parts[1]), "{}"), `\x`, replacement)
		x, okX := parseTikZNumber(xExpression)
		y, okY := parseTikZNumber(yExpression)
		if !okX || !okY {
			return path, errors.New("unsupported TikZ plot expression")
		}
		path.points = append(path.points, tikZPoint{x: x*canvas.xUnit + canvas.shiftX, y: -y*canvas.yUnit + canvas.shiftY})
	}
	return path, nil
}

func (canvas *tikZCanvas) parsePathLabel(source string, position int, point tikZPoint) (tikZPathLabel, int, bool) {
	if !strings.HasPrefix(source[position:], "node") {
		return tikZPathLabel{}, position, false
	}
	position += len("node")
	position = skipTeXSpace(source, position)
	options := ""
	if position < len(source) && source[position] == '[' {
		var ok bool
		options, position, ok = parseDelimited(source, position, '[', ']')
		if !ok {
			return tikZPathLabel{}, position, false
		}
	}
	position = skipTeXSpace(source, position)
	body, end, ok := parseBraced(source, position)
	if !ok {
		return tikZPathLabel{}, position, false
	}
	anchor := ""
	for _, option := range splitDiagramOptions(options) {
		option = strings.TrimSpace(option)
		if strings.Contains(" above below left right north south east west below left below right ", " "+option+" ") {
			anchor = option
		}
	}
	return tikZPathLabel{tex: tikZNodeMath(body), anchor: anchor, x: point.x, y: point.y}, end, true
}

func (canvas *tikZCanvas) parseCoordinateAt(source string, position int) (tikZPoint, int, bool) {
	position = skipTeXSpace(source, position)
	if position >= len(source) || source[position] != '(' {
		return tikZPoint{}, position, false
	}
	value, end, ok := parseDelimited(source, position, '(', ')')
	if !ok {
		return tikZPoint{}, position, false
	}
	value = strings.TrimSpace(value)
	if !strings.Contains(value, ",") {
		name := strings.TrimSpace(strings.Split(value, ".")[0])
		if node := canvas.nodeByName[name]; node != nil {
			return tikZPoint{x: node.x, y: node.y, name: name}, end, true
		}
		return tikZPoint{name: name}, end, name != ""
	}
	parts := splitDiagramOptions(value)
	if len(parts) != 2 {
		return tikZPoint{}, position, false
	}
	x, okX := parseTikZNumber(parts[0])
	y, okY := parseTikZNumber(parts[1])
	if !okX || !okY {
		return tikZPoint{}, position, false
	}
	return tikZPoint{x: x*canvas.xUnit + canvas.shiftX, y: -y*canvas.yUnit + canvas.shiftY}, end, true
}

func splitTikZCommands(source string) []string {
	var result []string
	start, braceDepth, bracketDepth, parenDepth := 0, 0, 0, 0
	for position := 0; position < len(source); position++ {
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
		case '(':
			parenDepth++
		case ')':
			if parenDepth > 0 {
				parenDepth--
			}
		case ';':
			if braceDepth == 0 && bracketDepth == 0 && parenDepth == 0 {
				result = append(result, source[start:position])
				start = position + 1
			}
		}
	}
	if strings.TrimSpace(source[start:]) != "" {
		result = append(result, source[start:])
	}
	return result
}

func expandSimpleTikZForeach(source string) string {
	for pass := 0; pass < 16; pass++ {
		start := strings.Index(source, `\foreach`)
		if start < 0 {
			return source
		}
		position := skipTeXSpace(source, start+len(`\foreach`))
		inPosition := strings.Index(source[position:], " in ")
		if inPosition < 0 {
			return source
		}
		variables := strings.TrimSpace(source[position : position+inPosition])
		position = skipTeXSpace(source, position+inPosition+len(" in "))
		values, end, ok := parseBraced(source, position)
		if !ok {
			return source
		}
		position = skipTeXSpace(source, end)
		body, bodyEnd, ok := parseBraced(source, position)
		if !ok {
			return source
		}
		variableNames := strings.Split(variables, "/")
		var expanded strings.Builder
		for _, item := range expandTikZForeachValues(values) {
			itemValues := strings.Split(strings.TrimSpace(item), "/")
			instance := body
			for index, name := range variableNames {
				if index < len(itemValues) {
					instance = replaceTikZVariable(instance, strings.TrimSpace(name), strings.TrimSpace(itemValues[index]))
				}
			}
			expanded.WriteString(instance)
			expanded.WriteByte('\n')
		}
		source = source[:start] + expanded.String() + source[bodyEnd:]
	}
	return source
}

func expandTikZForeachValues(source string) []string {
	values := splitDiagramOptions(source)
	if len(values) == 3 && strings.TrimSpace(values[1]) == "..." {
		start, startErr := strconv.Atoi(strings.TrimSpace(values[0]))
		end, endErr := strconv.Atoi(strings.TrimSpace(values[2]))
		if startErr == nil && endErr == nil {
			step := 1
			if end < start {
				step = -1
			}
			values = values[:0]
			for value := start; ; value += step {
				values = append(values, strconv.Itoa(value))
				if value == end {
					break
				}
			}
		}
	}
	return values
}

func replaceTikZVariable(source, name, value string) string {
	if name == "" {
		return source
	}
	var output strings.Builder
	for position := 0; position < len(source); {
		relative := strings.Index(source[position:], name)
		if relative < 0 {
			output.WriteString(source[position:])
			break
		}
		start := position + relative
		end := start + len(name)
		output.WriteString(source[position:start])
		boundary := end == len(source) || !isTeXCommandLetter(source[end])
		if boundary {
			output.WriteString(value)
		} else {
			output.WriteString(name)
		}
		position = end
	}
	return output.String()
}

func isTeXCommandLetter(value byte) bool {
	return (value >= 'A' && value <= 'Z') || (value >= 'a' && value <= 'z') || value == '@'
}

func tikZNodeMath(source string) string {
	source = strings.TrimSpace(source)
	if len(source) >= 2 && source[0] == '$' && source[len(source)-1] == '$' && strings.Count(source, "$") == 2 {
		return strings.TrimSpace(source[1 : len(source)-1])
	}
	if strings.Contains(source, "$") {
		var result strings.Builder
		mathMode := false
		start := 0
		for position := 0; position < len(source); position++ {
			if source[position] != '$' || texCharacterEscaped(source, position) {
				continue
			}
			segment := strings.TrimSpace(source[start:position])
			if segment != "" {
				if mathMode {
					result.WriteString(segment)
				} else {
					result.WriteString(`\text{` + strings.ReplaceAll(segment, `\\`, " ") + `}`)
				}
			}
			mathMode = !mathMode
			start = position + 1
		}
		if tail := strings.TrimSpace(source[start:]); tail != "" {
			if mathMode {
				result.WriteString(tail)
			} else {
				result.WriteString(`\text{` + strings.ReplaceAll(tail, `\\`, " ") + `}`)
			}
		}
		return result.String()
	}
	if strings.IndexFunc(source, func(value rune) bool { return unicode.Is(unicode.Han, value) || unicode.IsSpace(value) }) >= 0 && !strings.HasPrefix(source, `\`) {
		return `\text{` + source + `}`
	}
	return source
}

func texCharacterEscaped(source string, position int) bool {
	backslashes := 0
	for position > backslashes && source[position-backslashes-1] == '\\' {
		backslashes++
	}
	return backslashes%2 == 1
}

func tikZDimension(source string, defaultUnit float64) (float64, bool) {
	source = strings.TrimSpace(strings.Trim(source, "{}"))
	units := []struct {
		suffix string
		factor float64
	}{{"cm", 2700}, {"mm", 270}, {"bp", 95}, {"pt", 95}, {"ex", diagramUnitsPerEx}, {"em", 2 * diagramUnitsPerEx}}
	for _, unit := range units {
		if strings.HasSuffix(source, unit.suffix) {
			value, ok := parseTikZNumber(strings.TrimSpace(strings.TrimSuffix(source, unit.suffix)))
			return value * unit.factor, ok
		}
	}
	value, ok := parseTikZNumber(source)
	return value * defaultUnit, ok
}

type tikZExpressionParser struct {
	source   string
	position int
}

func parseTikZNumber(source string) (float64, bool) {
	parser := &tikZExpressionParser{source: strings.TrimSpace(strings.Trim(source, "{}"))}
	value, ok := parser.sum()
	parser.space()
	return value, ok && parser.position == len(parser.source)
}

func (parser *tikZExpressionParser) space() {
	for parser.position < len(parser.source) && unicode.IsSpace(rune(parser.source[parser.position])) {
		parser.position++
	}
}

func (parser *tikZExpressionParser) sum() (float64, bool) {
	value, ok := parser.product()
	for ok {
		parser.space()
		if parser.position >= len(parser.source) || (parser.source[parser.position] != '+' && parser.source[parser.position] != '-') {
			return value, true
		}
		operator := parser.source[parser.position]
		parser.position++
		right, parsed := parser.product()
		if !parsed {
			return 0, false
		}
		if operator == '+' {
			value += right
		} else {
			value -= right
		}
	}
	return 0, false
}

func (parser *tikZExpressionParser) product() (float64, bool) {
	value, ok := parser.atom()
	for ok {
		parser.space()
		if parser.position >= len(parser.source) || (parser.source[parser.position] != '*' && parser.source[parser.position] != '/') {
			return value, true
		}
		operator := parser.source[parser.position]
		parser.position++
		right, parsed := parser.atom()
		if !parsed || (operator == '/' && right == 0) {
			return 0, false
		}
		if operator == '*' {
			value *= right
		} else {
			value /= right
		}
	}
	return 0, false
}

func (parser *tikZExpressionParser) atom() (float64, bool) {
	parser.space()
	if parser.position >= len(parser.source) {
		return 0, false
	}
	if parser.source[parser.position] == '(' {
		parser.position++
		value, ok := parser.sum()
		parser.space()
		if !ok || parser.position >= len(parser.source) || parser.source[parser.position] != ')' {
			return 0, false
		}
		parser.position++
		return value, true
	}
	start := parser.position
	if parser.source[parser.position] == '+' || parser.source[parser.position] == '-' {
		parser.position++
	}
	for parser.position < len(parser.source) && ((parser.source[parser.position] >= '0' && parser.source[parser.position] <= '9') || parser.source[parser.position] == '.') {
		parser.position++
	}
	if parser.position == start || (parser.position == start+1 && (parser.source[start] == '+' || parser.source[start] == '-')) {
		return 0, false
	}
	value, err := strconv.ParseFloat(parser.source[start:parser.position], 64)
	return value, err == nil
}
