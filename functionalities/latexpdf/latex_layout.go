package latexpdf

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// Sizes are relative to the document's normal text, not to the previous
// declaration. TeX declarations last until the enclosing group/environment ends.
var latexTextSizes = map[string]float64{
	"tiny": .5, "scriptsize": .7, "footnotesize": .8, "small": .9,
	"normalsize": 1, "large": 1.2, "Large": 1.44, "LARGE": 1.728,
	"huge": 2.074, "Huge": 2.488,
}

func latexTextAlignment(command string) string {
	switch command {
	case "centering":
		return "center"
	case "raggedleft":
		return "right"
	case "raggedright":
		return "left"
	}
	return ""
}

func texSizeAttributes(size float64) string {
	if size == 0 || size == 1 {
		return ""
	}
	return fmt.Sprintf(` data-tex-size="%g" style="font-size:%gem"`, size, size)
}

func (context *latexRenderContext) sizedBlock(rendered string) string {
	if context.textSize == 0 || context.textSize == 1 {
		return rendered
	}
	return `<div` + texSizeAttributes(context.textSize) + `>` + rendered + `</div>`
}

var texLengthPattern = regexp.MustCompile(`^([+-]?(?:[0-9]+(?:\.[0-9]*)?|\.[0-9]+))?\s*(\\(?:textwidth|linewidth|columnwidth|paperwidth|textheight|paperheight)|pt|bp|mm|cm|in|em|ex)$`)

// texLengthCSS retains relative lengths for the layout that owns them. Widths
// such as .47\textwidth must not be rendered as text in column/minipage bodies.
func texLengthCSS(source string) (string, bool) {
	match := texLengthPattern.FindStringSubmatch(strings.TrimSpace(source))
	if match == nil {
		return "", false
	}
	value := 1.0
	if match[1] != "" {
		value, _ = strconv.ParseFloat(match[1], 64)
	}
	unit := match[2]
	if strings.HasPrefix(unit, `\`) {
		value *= 100
		unit = "%"
	} else if unit == "bp" {
		unit = "pt"
	}
	return fmt.Sprintf("%g%s", value, unit), true
}

func skipOverlay(source string, position int) int {
	position = skipTeXSpace(source, position)
	if _, end, ok := parseDelimited(source, position, '<', '>'); ok {
		return skipTeXSpace(source, end)
	}
	return position
}

func skipOptionalArguments(source string, position int) int {
	position = skipTeXSpace(source, position)
	for position < len(source) && source[position] == '[' {
		_, end, ok := parseDelimited(source, position, '[', ']')
		if !ok {
			break
		}
		position = skipTeXSpace(source, end)
	}
	return position
}
