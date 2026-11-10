package latexpdf

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"html"
	"html/template"
	"math"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"
)

const (
	flowchartMaxNodes  = 64
	flowchartMaxEdges  = 128
	flowchartMaxGroups = 16
)

type flowchart struct {
	Direction string
	Nodes     []*flowchartNode
	Edges     []*flowchartEdge
	Groups    []*flowchartGroup
	nodeByID  map[string]*flowchartNode
	groupByID map[string]*flowchartGroup
}

type flowchartNode struct {
	ID, Label, Style, Group string
	Order                   int
	Explicit                bool
	X, Y, Width, Height     int
}

type flowchartEdge struct {
	Source, Target, Label string
	Dashed                bool
}

type flowchartGroup struct {
	ID, Label           string
	Order               int
	X, Y, Width, Height int
}

type flowchartLane struct {
	group               *flowchartGroup
	nodes               []*flowchartNode
	X, Y, Width, Height int
}

type flowchartToken struct {
	value  string
	quoted bool
}

func (context *latexRenderContext) renderFlowchart(source, optionSource string) string {
	chart, err := parseFlowchart(source, optionSource)
	if err != nil {
		return flowchartErrorHTML(source, err)
	}
	layOutFlowchart(chart)
	context.flowcharts++
	digest := sha256.Sum256([]byte(fmt.Sprintf("%d\x00%s", context.flowcharts, source)))
	markerID := "flow-arrow-" + hex.EncodeToString(digest[:6])

	width, height := flowchartBounds(chart)
	var output strings.Builder
	fmt.Fprintf(&output, `<figure class="flowchart" role="group" aria-label="Flow chart"><div class="flowchart-scroll"><div class="flowchart-viewport" data-flowchart-width="%d" data-flowchart-height="%d" style="width:%dpx;height:%dpx"><div class="flowchart-canvas" style="width:%dpx;height:%dpx">`, width, height, width, height, width, height)
	fmt.Fprintf(&output, `<svg class="flowchart-svg" viewBox="0 0 %d %d" width="%d" height="%d" aria-hidden="true" focusable="false"><defs><marker id="%s" viewBox="0 0 10 10" refX="9" refY="5" markerWidth="7" markerHeight="7" orient="auto-start-reverse"><path d="M 0 0 L 10 5 L 0 10 z"></path></marker></defs>`, width, height, width, height, markerID)
	for _, group := range chart.Groups {
		if group.Width == 0 || group.Height == 0 {
			continue
		}
		fmt.Fprintf(&output, `<rect class="flowchart-group-box" x="%d" y="%d" width="%d" height="%d" rx="12"></rect>`, group.X, group.Y, group.Width, group.Height)
	}
	for _, edge := range chart.Edges {
		sourceNode := chart.nodeByID[edge.Source]
		targetNode := chart.nodeByID[edge.Target]
		path, _, _ := flowchartEdgeGeometry(sourceNode, targetNode)
		className := "flowchart-edge"
		if edge.Dashed {
			className += " dashed"
		}
		fmt.Fprintf(&output, `<path class="%s" d="%s" marker-end="url(#%s)"></path>`, className, path, markerID)
	}
	output.WriteString(`</svg>`)

	for _, group := range chart.Groups {
		if group.Width == 0 || group.Height == 0 {
			continue
		}
		fmt.Fprintf(&output, `<div class="flowchart-group-title" data-group="%s" style="left:%dpx;top:%dpx;width:%dpx">%s</div>`,
			template.HTMLEscapeString(group.ID), group.X+18, group.Y+10, group.Width-36, context.renderInline(group.Label))
	}
	for _, edge := range chart.Edges {
		if strings.TrimSpace(edge.Label) == "" {
			continue
		}
		sourceNode := chart.nodeByID[edge.Source]
		targetNode := chart.nodeByID[edge.Target]
		_, labelX, labelY := flowchartEdgeGeometry(sourceNode, targetNode)
		const labelWidth, labelHeight = 180, 40
		labelX = max(8, min(width-labelWidth-8, labelX-labelWidth/2))
		labelY = max(8, min(height-labelHeight-8, labelY-labelHeight/2))
		fmt.Fprintf(&output, `<div class="flowchart-edge-label" style="left:%dpx;top:%dpx;width:%dpx;min-height:%dpx">%s</div>`,
			labelX, labelY, labelWidth, labelHeight, context.renderInline(edge.Label))
	}
	for _, node := range chart.Nodes {
		fmt.Fprintf(&output, `<div class="flowchart-node %s" data-node="%s" style="left:%dpx;top:%dpx;width:%dpx;height:%dpx"><div>%s</div></div>`,
			flowchartStyleClass(node.Style), template.HTMLEscapeString(node.ID), node.X, node.Y, node.Width, node.Height, context.renderInline(node.Label))
	}
	output.WriteString(`</div></div></div></figure>`)
	return output.String()
}

func flowchartErrorHTML(source string, err error) string {
	return `<div class="notice error flowchart-error"><strong>Flowchart error:</strong> ` +
		template.HTMLEscapeString(err.Error()) + `</div><details class="source-fragment diagram-source"><summary>Flowchart source</summary><code>` +
		template.HTMLEscapeString(strings.TrimSpace(source)) + `</code></details>`
}

func parseFlowchart(source, optionSource string) (*flowchart, error) {
	chart := &flowchart{
		Direction: "down",
		nodeByID:  make(map[string]*flowchartNode),
		groupByID: make(map[string]*flowchartGroup),
	}
	if err := chart.applyOptions(optionSource); err != nil {
		return nil, err
	}
	currentGroup := ""
	for lineIndex, line := range strings.Split(source, "\n") {
		lineNumber := lineIndex + 1
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		tokens, err := lexFlowchartLine(trimmed)
		if err != nil {
			return nil, fmt.Errorf("line %d: %w", lineNumber, err)
		}
		if len(tokens) == 0 {
			continue
		}
		if len(tokens) == 1 && tokens[0].value == "}" {
			if currentGroup == "" {
				return nil, fmt.Errorf("line %d: unexpected group terminator", lineNumber)
			}
			currentGroup = ""
			continue
		}
		switch strings.ToLower(tokens[0].value) {
		case "direction":
			if len(tokens) != 2 || tokens[1].quoted {
				return nil, fmt.Errorf("line %d: direction requires one of down, right, up, or left", lineNumber)
			}
			direction, ok := normalizeFlowchartDirection(tokens[1].value)
			if !ok {
				return nil, fmt.Errorf("line %d: unknown direction %q", lineNumber, tokens[1].value)
			}
			chart.Direction = direction
		case "group":
			if currentGroup != "" {
				return nil, fmt.Errorf("line %d: nested groups are not supported", lineNumber)
			}
			if len(tokens) != 4 || tokens[1].quoted || !tokens[2].quoted || tokens[3].value != "{" {
				return nil, fmt.Errorf("line %d: use group id \"title\" {", lineNumber)
			}
			if err := validateFlowchartID(tokens[1].value); err != nil {
				return nil, fmt.Errorf("line %d: %w", lineNumber, err)
			}
			if len(chart.Groups) >= flowchartMaxGroups {
				return nil, fmt.Errorf("line %d: a chart may contain at most %d groups", lineNumber, flowchartMaxGroups)
			}
			if _, exists := chart.groupByID[tokens[1].value]; exists {
				return nil, fmt.Errorf("line %d: duplicate group %q", lineNumber, tokens[1].value)
			}
			group := &flowchartGroup{ID: tokens[1].value, Label: tokens[2].value, Order: len(chart.Groups)}
			chart.Groups = append(chart.Groups, group)
			chart.groupByID[group.ID] = group
			currentGroup = group.ID
		case "node":
			if err := chart.parseNode(tokens, currentGroup, lineNumber); err != nil {
				return nil, err
			}
		default:
			if err := chart.parseEdges(tokens, currentGroup, lineNumber); err != nil {
				return nil, err
			}
		}
	}
	if currentGroup != "" {
		return nil, fmt.Errorf("group %q is missing its closing }", currentGroup)
	}
	if len(chart.Nodes) == 0 {
		return nil, fmt.Errorf("the chart has no nodes")
	}
	return chart, nil
}

func (chart *flowchart) applyOptions(source string) error {
	for _, raw := range strings.Split(source, ",") {
		option := strings.TrimSpace(raw)
		if option == "" {
			continue
		}
		key, value, hasValue := strings.Cut(option, "=")
		if !hasValue {
			if direction, ok := normalizeFlowchartDirection(key); ok {
				chart.Direction = direction
				continue
			}
			return fmt.Errorf("unknown flowchart option %q", option)
		}
		if strings.TrimSpace(strings.ToLower(key)) != "direction" {
			return fmt.Errorf("unknown flowchart option %q", strings.TrimSpace(key))
		}
		direction, ok := normalizeFlowchartDirection(strings.TrimSpace(value))
		if !ok {
			return fmt.Errorf("unknown flowchart direction %q", strings.TrimSpace(value))
		}
		chart.Direction = direction
	}
	return nil
}

func (chart *flowchart) parseNode(tokens []flowchartToken, group string, lineNumber int) error {
	if len(tokens) != 3 && len(tokens) != 6 {
		return fmt.Errorf("line %d: use node id \"label\" or node id \"label\" [style]", lineNumber)
	}
	if tokens[1].quoted || !tokens[2].quoted {
		return fmt.Errorf("line %d: node IDs are bare words and labels are quoted", lineNumber)
	}
	if err := validateFlowchartID(tokens[1].value); err != nil {
		return fmt.Errorf("line %d: %w", lineNumber, err)
	}
	style := "default"
	if len(tokens) == 6 {
		if tokens[3].value != "[" || tokens[4].quoted || tokens[5].value != "]" {
			return fmt.Errorf("line %d: malformed node style", lineNumber)
		}
		var ok bool
		style, ok = normalizeFlowchartStyle(tokens[4].value)
		if !ok {
			return fmt.Errorf("line %d: unknown node style %q", lineNumber, tokens[4].value)
		}
	}
	node, exists := chart.nodeByID[tokens[1].value]
	if exists && node.Explicit {
		return fmt.Errorf("line %d: duplicate node %q", lineNumber, node.ID)
	}
	if !exists {
		if len(chart.Nodes) >= flowchartMaxNodes {
			return fmt.Errorf("line %d: a chart may contain at most %d nodes", lineNumber, flowchartMaxNodes)
		}
		node = &flowchartNode{ID: tokens[1].value, Order: len(chart.Nodes)}
		chart.Nodes = append(chart.Nodes, node)
		chart.nodeByID[node.ID] = node
	}
	node.Label, node.Style, node.Group, node.Explicit = tokens[2].value, style, group, true
	return nil
}

func (chart *flowchart) parseEdges(tokens []flowchartToken, group string, lineNumber int) error {
	if len(tokens) < 3 || tokens[0].quoted {
		return fmt.Errorf("line %d: expected a node, group, direction, or edge chain", lineNumber)
	}
	label := ""
	if tokens[len(tokens)-1].quoted {
		label = tokens[len(tokens)-1].value
		tokens = tokens[:len(tokens)-1]
	}
	if len(tokens)%2 == 0 {
		return fmt.Errorf("line %d: malformed edge chain", lineNumber)
	}
	if label != "" && len(tokens) != 3 {
		return fmt.Errorf("line %d: a labeled edge must have exactly one source and target", lineNumber)
	}
	for index := 0; index < len(tokens); index += 2 {
		if tokens[index].quoted {
			return fmt.Errorf("line %d: edge node IDs must be bare words", lineNumber)
		}
		if err := validateFlowchartID(tokens[index].value); err != nil {
			return fmt.Errorf("line %d: %w", lineNumber, err)
		}
		if _, err := chart.ensureNode(tokens[index].value, group, lineNumber); err != nil {
			return err
		}
		if index+1 >= len(tokens) {
			continue
		}
		arrow := tokens[index+1].value
		if arrow != "->" && arrow != "-->" && arrow != "-.->" {
			return fmt.Errorf("line %d: unknown edge operator %q", lineNumber, arrow)
		}
	}
	for index := 0; index+2 < len(tokens); index += 2 {
		if len(chart.Edges) >= flowchartMaxEdges {
			return fmt.Errorf("line %d: a chart may contain at most %d edges", lineNumber, flowchartMaxEdges)
		}
		chart.Edges = append(chart.Edges, &flowchartEdge{
			Source: tokens[index].value, Target: tokens[index+2].value,
			Label: label, Dashed: tokens[index+1].value == "-.->",
		})
	}
	return nil
}

func (chart *flowchart) ensureNode(id, group string, lineNumber int) (*flowchartNode, error) {
	if node, ok := chart.nodeByID[id]; ok {
		return node, nil
	}
	if len(chart.Nodes) >= flowchartMaxNodes {
		return nil, fmt.Errorf("line %d: a chart may contain at most %d nodes", lineNumber, flowchartMaxNodes)
	}
	node := &flowchartNode{ID: id, Label: id, Style: "default", Group: group, Order: len(chart.Nodes)}
	chart.Nodes = append(chart.Nodes, node)
	chart.nodeByID[id] = node
	return node, nil
}

func lexFlowchartLine(line string) ([]flowchartToken, error) {
	var tokens []flowchartToken
	for position := 0; position < len(line); {
		character, size := utf8.DecodeRuneInString(line[position:])
		if unicode.IsSpace(character) {
			position += size
			continue
		}
		if character == '"' {
			var value strings.Builder
			position += size
			closed := false
			for position < len(line) {
				if line[position] == '"' {
					position++
					closed = true
					break
				}
				if line[position] == '\\' && position+1 < len(line) && line[position+1] == '"' {
					value.WriteByte('"')
					position += 2
					continue
				}
				_, runeSize := utf8.DecodeRuneInString(line[position:])
				if runeSize <= 0 {
					break
				}
				value.WriteString(line[position : position+runeSize])
				position += runeSize
			}
			if !closed {
				return nil, fmt.Errorf("unterminated quoted label")
			}
			tokens = append(tokens, flowchartToken{value: value.String(), quoted: true})
			continue
		}
		matched := false
		for _, operator := range []string{"-.->", "-->", "->"} {
			if strings.HasPrefix(line[position:], operator) {
				tokens = append(tokens, flowchartToken{value: operator})
				position += len(operator)
				matched = true
				break
			}
		}
		if matched {
			continue
		}
		if strings.ContainsRune("{}[]", character) {
			tokens = append(tokens, flowchartToken{value: string(character)})
			position += size
			continue
		}
		start := position
		for position < len(line) {
			character, size = utf8.DecodeRuneInString(line[position:])
			if unicode.IsSpace(character) || strings.ContainsRune("\"{}[]", character) ||
				strings.HasPrefix(line[position:], "->") || strings.HasPrefix(line[position:], "-->") || strings.HasPrefix(line[position:], "-.->") {
				break
			}
			position += size
		}
		if position == start {
			return nil, fmt.Errorf("unexpected character %q", character)
		}
		tokens = append(tokens, flowchartToken{value: line[start:position]})
	}
	return tokens, nil
}

func validateFlowchartID(id string) error {
	if id == "" {
		return fmt.Errorf("empty chart ID")
	}
	for index, character := range id {
		if (index == 0 && !unicode.IsLetter(character) && character != '_') ||
			(index > 0 && !unicode.IsLetter(character) && !unicode.IsDigit(character) && character != '_' && character != '-') {
			return fmt.Errorf("invalid chart ID %q", id)
		}
	}
	return nil
}

func normalizeFlowchartDirection(value string) (string, bool) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "down", "tb":
		return "down", true
	case "right", "lr":
		return "right", true
	case "up", "bt":
		return "up", true
	case "left", "rl":
		return "left", true
	default:
		return "", false
	}
}

func normalizeFlowchartStyle(value string) (string, bool) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "default", "accent", "success", "warning", "muted":
		return strings.ToLower(strings.TrimSpace(value)), true
	default:
		return "", false
	}
}

func flowchartStyleClass(style string) string {
	if normalized, ok := normalizeFlowchartStyle(style); ok {
		return normalized
	}
	return "default"
}

func layOutFlowchart(chart *flowchart) {
	const margin, laneGap = 26, 34
	groupNodes := make(map[string][]*flowchartNode)
	for _, node := range chart.Nodes {
		measureFlowchartNode(node)
		groupNodes[node.Group] = append(groupNodes[node.Group], node)
	}
	lanes := make([]*flowchartLane, 0, len(chart.Groups)+1)
	for _, group := range chart.Groups {
		if nodes := groupNodes[group.ID]; len(nodes) > 0 {
			lane := &flowchartLane{group: group, nodes: nodes}
			layOutFlowchartLane(chart, lane)
			lanes = append(lanes, lane)
		}
	}
	if nodes := groupNodes[""]; len(nodes) > 0 {
		lane := &flowchartLane{nodes: nodes}
		layOutFlowchartLane(chart, lane)
		lanes = append(lanes, lane)
	}

	cursor := margin
	for _, lane := range lanes {
		if chart.Direction == "down" || chart.Direction == "up" {
			lane.X, lane.Y = cursor, margin
			cursor += lane.Width + laneGap
		} else {
			lane.X, lane.Y = margin, cursor
			cursor += lane.Height + laneGap
		}
		for _, node := range lane.nodes {
			node.X += lane.X
			node.Y += lane.Y
		}
		if lane.group != nil {
			lane.group.X, lane.group.Y = lane.X, lane.Y
			lane.group.Width, lane.group.Height = lane.Width, lane.Height
		}
	}
}

func layOutFlowchartLane(chart *flowchart, lane *flowchartLane) {
	const groupPadding, titleHeight, crossGap, rankGap = 24, 42, 26, 64
	ranks := flowchartRanks(chart, lane.nodes)
	vertical := chart.Direction == "down" || chart.Direction == "up"
	reverse := chart.Direction == "up" || chart.Direction == "left"
	title := 0
	padding := 8
	if lane.group != nil {
		title, padding = titleHeight, groupPadding
	}

	if vertical {
		contentWidth, contentHeight := 0, 0
		rankWidths := make([]int, len(ranks))
		rankHeights := make([]int, len(ranks))
		for index, rank := range ranks {
			for nodeIndex, node := range rank {
				rankWidths[index] += node.Width
				if nodeIndex > 0 {
					rankWidths[index] += crossGap
				}
				rankHeights[index] = max(rankHeights[index], node.Height)
			}
			contentWidth = max(contentWidth, rankWidths[index])
			contentHeight += rankHeights[index]
			if index > 0 {
				contentHeight += rankGap
			}
		}
		lane.Width = contentWidth + 2*padding
		lane.Height = title + contentHeight + 2*padding
		positionY := title + padding
		order := flowchartRankOrder(len(ranks), reverse)
		for _, rankIndex := range order {
			positionX := padding + (contentWidth-rankWidths[rankIndex])/2
			for _, node := range ranks[rankIndex] {
				node.X = positionX
				node.Y = positionY + (rankHeights[rankIndex]-node.Height)/2
				positionX += node.Width + crossGap
			}
			positionY += rankHeights[rankIndex] + rankGap
		}
		return
	}

	contentWidth, contentHeight := 0, 0
	rankWidths := make([]int, len(ranks))
	rankHeights := make([]int, len(ranks))
	for index, rank := range ranks {
		for nodeIndex, node := range rank {
			rankWidths[index] = max(rankWidths[index], node.Width)
			rankHeights[index] += node.Height
			if nodeIndex > 0 {
				rankHeights[index] += crossGap
			}
		}
		contentHeight = max(contentHeight, rankHeights[index])
		contentWidth += rankWidths[index]
		if index > 0 {
			contentWidth += rankGap
		}
	}
	lane.Width = contentWidth + 2*padding
	lane.Height = title + contentHeight + 2*padding
	positionX := padding
	order := flowchartRankOrder(len(ranks), reverse)
	for _, rankIndex := range order {
		positionY := title + padding + (contentHeight-rankHeights[rankIndex])/2
		for _, node := range ranks[rankIndex] {
			node.X = positionX + (rankWidths[rankIndex]-node.Width)/2
			node.Y = positionY
			positionY += node.Height + crossGap
		}
		positionX += rankWidths[rankIndex] + rankGap
	}
}

func flowchartRankOrder(count int, reverse bool) []int {
	order := make([]int, count)
	for index := range order {
		order[index] = index
		if reverse {
			order[index] = count - index - 1
		}
	}
	return order
}

func flowchartRanks(chart *flowchart, nodes []*flowchartNode) [][]*flowchartNode {
	inLane := make(map[string]bool, len(nodes))
	indegree := make(map[string]int, len(nodes))
	adjacency := make(map[string][]string, len(nodes))
	deduplicate := make(map[string]bool)
	rank := make(map[string]int, len(nodes))
	for _, node := range nodes {
		inLane[node.ID] = true
	}
	for _, edge := range chart.Edges {
		if edge.Source == edge.Target || !inLane[edge.Source] || !inLane[edge.Target] {
			continue
		}
		key := edge.Source + "\x00" + edge.Target
		if deduplicate[key] {
			continue
		}
		deduplicate[key] = true
		adjacency[edge.Source] = append(adjacency[edge.Source], edge.Target)
		indegree[edge.Target]++
	}
	queue := make([]*flowchartNode, 0, len(nodes))
	for _, node := range nodes {
		if indegree[node.ID] == 0 {
			queue = append(queue, node)
		}
	}
	processed := make(map[string]bool, len(nodes))
	for len(queue) > 0 {
		node := queue[0]
		queue = queue[1:]
		if processed[node.ID] {
			continue
		}
		processed[node.ID] = true
		for _, target := range adjacency[node.ID] {
			rank[target] = max(rank[target], rank[node.ID]+1)
			indegree[target]--
			if indegree[target] == 0 {
				queue = append(queue, chart.nodeByID[target])
			}
		}
	}
	maxRank := 0
	for _, value := range rank {
		maxRank = max(maxRank, value)
	}
	for _, node := range nodes {
		if processed[node.ID] {
			continue
		}
		maxRank++
		rank[node.ID] = maxRank
		processed[node.ID] = true
	}
	ranks := make([][]*flowchartNode, maxRank+1)
	for _, node := range nodes {
		ranks[rank[node.ID]] = append(ranks[rank[node.ID]], node)
	}
	compact := ranks[:0]
	for _, level := range ranks {
		if len(level) == 0 {
			continue
		}
		sort.SliceStable(level, func(i, j int) bool { return level[i].Order < level[j].Order })
		compact = append(compact, level)
	}
	return compact
}

func measureFlowchartNode(node *flowchartNode) {
	const minWidth, maxWidth, horizontalPadding, lineHeight = 156, 286, 32, 23
	// Allow for the presentation's monospace font, including its heavier
	// node labels. Underestimating glyph width also undercounts wrapped lines.
	const characterWidth = 10
	lines := strings.Split(node.Label, `\\`)
	if len(lines) == 0 {
		lines = []string{node.Label}
	}
	maxCharacters := 1
	for _, line := range lines {
		plain := html.UnescapeString(texFragmentToPlainText(line))
		characters := utf8.RuneCountInString(plain) + flowchartCommandCount(line)
		maxCharacters = max(maxCharacters, characters)
	}
	node.Width = min(maxWidth, max(minWidth, maxCharacters*characterWidth+horizontalPadding))
	charactersPerLine := max(12, (node.Width-horizontalPadding)/characterWidth)
	visualLines := 0
	for _, line := range lines {
		plain := html.UnescapeString(texFragmentToPlainText(line))
		characters := max(1, utf8.RuneCountInString(plain)+flowchartCommandCount(line))
		visualLines += max(1, int(math.Ceil(float64(characters)/float64(charactersPerLine))))
	}
	node.Height = max(62, visualLines*lineHeight+28)
}

func flowchartCommandCount(source string) int {
	count := 0
	for position := 0; position < len(source); {
		if source[position] != '\\' {
			_, size := utf8.DecodeRuneInString(source[position:])
			position += max(1, size)
			continue
		}
		_, end := readTeXCommand(source, position)
		if end <= position {
			position++
			continue
		}
		count++
		position = end
	}
	return count
}

func flowchartBounds(chart *flowchart) (int, int) {
	const margin = 26
	width, height := 0, 0
	for _, node := range chart.Nodes {
		width = max(width, node.X+node.Width)
		height = max(height, node.Y+node.Height)
	}
	for _, group := range chart.Groups {
		width = max(width, group.X+group.Width)
		height = max(height, group.Y+group.Height)
	}
	return width + margin, height + margin
}

func flowchartEdgeGeometry(source, target *flowchartNode) (string, int, int) {
	if source == target {
		startX := source.X + source.Width
		startY := source.Y + source.Height/3
		endX := source.X + source.Width
		endY := source.Y + 2*source.Height/3
		loopX := source.X + source.Width + 54
		path := fmt.Sprintf("M %d %d C %d %d, %d %d, %d %d", startX, startY, loopX, startY-36, loopX, endY+36, endX, endY)
		return path, loopX, source.Y + source.Height/2
	}
	sourceCenterX, sourceCenterY := source.X+source.Width/2, source.Y+source.Height/2
	targetCenterX, targetCenterY := target.X+target.Width/2, target.Y+target.Height/2
	startX, startY := flowchartRectangleBoundary(sourceCenterX, sourceCenterY, source.Width, source.Height, targetCenterX, targetCenterY)
	endX, endY := flowchartRectangleBoundary(targetCenterX, targetCenterY, target.Width, target.Height, sourceCenterX, sourceCenterY)
	control1X, control1Y, control2X, control2Y := startX, startY, endX, endY
	if abs(targetCenterX-sourceCenterX) > abs(targetCenterY-sourceCenterY) {
		middle := (startX + endX) / 2
		control1X, control2X = middle, middle
	} else {
		middle := (startY + endY) / 2
		control1Y, control2Y = middle, middle
	}
	path := fmt.Sprintf("M %d %d C %d %d, %d %d, %d %d", startX, startY, control1X, control1Y, control2X, control2Y, endX, endY)
	labelX := (startX + 3*control1X + 3*control2X + endX) / 8
	labelY := (startY + 3*control1Y + 3*control2Y + endY) / 8
	return path, labelX, labelY
}

func flowchartRectangleBoundary(centerX, centerY, width, height, towardX, towardY int) (int, int) {
	deltaX, deltaY := towardX-centerX, towardY-centerY
	if deltaX == 0 && deltaY == 0 {
		return centerX, centerY
	}
	scaleX, scaleY := math.Inf(1), math.Inf(1)
	if deltaX != 0 {
		scaleX = float64(width/2) / math.Abs(float64(deltaX))
	}
	if deltaY != 0 {
		scaleY = float64(height/2) / math.Abs(float64(deltaY))
	}
	scale := math.Min(scaleX, scaleY)
	return centerX + int(math.Round(float64(deltaX)*scale)), centerY + int(math.Round(float64(deltaY)*scale))
}

func abs(value int) int {
	if value < 0 {
		return -value
	}
	return value
}
