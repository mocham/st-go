package latexpdf

import (
	"fmt"
	"math"
	"strconv"
	"strings"
)

type renderColor struct {
	red, green, blue int
}

func (color renderColor) hex() string {
	return fmt.Sprintf("#%02x%02x%02x", color.red, color.green, color.blue)
}

func (color renderColor) rgbSpec() string {
	return fmt.Sprintf("%d,%d,%d", color.red, color.green, color.blue)
}

var baseLatexColors = map[string]renderColor{
	"black":          {0, 0, 0},
	"blue":           {0, 0, 255},
	"brown":          {191, 128, 64},
	"cyan":           {0, 255, 255},
	"darkgray":       {64, 64, 64},
	"gray":           {128, 128, 128},
	"green":          {0, 255, 0},
	"lightgray":      {191, 191, 191},
	"lime":           {191, 255, 0},
	"magenta":        {255, 0, 255},
	"olive":          {128, 128, 0},
	"orange":         {255, 128, 0},
	"pink":           {255, 191, 191},
	"purple":         {191, 0, 64},
	"red":            {255, 0, 0},
	"teal":           {0, 128, 128},
	"violet":         {128, 0, 128},
	"white":          {255, 255, 255},
	"yellow":         {255, 255, 0},
	"apricot":        {255, 173, 122},
	"aquamarine":     {0, 181, 190},
	"bittersweet":    {192, 79, 23},
	"bluegreen":      {0, 179, 169},
	"blueviolet":     {71, 46, 232},
	"brickred":       {182, 50, 28},
	"burntorange":    {249, 104, 0},
	"cadetblue":      {95, 158, 160},
	"carnationpink":  {255, 92, 168},
	"cerulean":       {0, 169, 227},
	"cornflowerblue": {100, 149, 237},
	"dandelion":      {240, 225, 48},
	"darkorchid":     {153, 50, 204},
	"emerald":        {0, 169, 157},
	"forestgreen":    {0, 155, 85},
	"fuchsia":        {255, 0, 255},
	"goldenrod":      {255, 223, 66},
	"junglegreen":    {0, 172, 158},
	"lavender":       {230, 230, 250},
	"mahogany":       {166, 25, 46},
	"maroon":         {128, 0, 0},
	"melon":          {255, 138, 128},
	"midnightblue":   {25, 25, 112},
	"mulberry":       {165, 25, 93},
	"navy":           {0, 0, 128},
	"orchid":         {218, 112, 214},
	"peach":          {255, 127, 80},
	"periwinkle":     {204, 204, 255},
	"pinegreen":      {0, 121, 111},
	"plum":           {221, 160, 221},
	"processblue":    {0, 169, 224},
	"rawsienna":      {151, 105, 79},
	"redorange":      {255, 83, 73},
	"redviolet":      {161, 0, 107},
	"rhodamine":      {239, 85, 159},
	"royalblue":      {0, 35, 102},
	"royalpurple":    {97, 64, 81},
	"rubinered":      {237, 0, 94},
	"salmon":         {250, 128, 114},
	"seagreen":       {46, 139, 87},
	"sepia":          {112, 66, 20},
	"skyblue":        {135, 206, 235},
	"springgreen":    {0, 255, 127},
	"tan":            {210, 180, 140},
	"thistle":        {216, 191, 216},
	"turquoise":      {64, 224, 208},
	"violetred":      {208, 32, 144},
	"wildstrawberry": {255, 67, 164},
	"yellowgreen":    {154, 205, 50},
	"yelloworange":   {255, 174, 66},
}

func newLatexColorMap() map[string]renderColor {
	colors := make(map[string]renderColor, len(baseLatexColors))
	for name, color := range baseLatexColors {
		colors[name] = color
	}
	return colors
}

func extractLatexColors(source string) map[string]renderColor {
	colors := newLatexColorMap()
	for position := 0; position < len(source); {
		relative := strings.IndexByte(source[position:], '\\')
		if relative < 0 {
			break
		}
		start := position + relative
		command, commandEnd := readTeXCommand(source, start)
		if commandEnd <= start {
			position = start + 1
			continue
		}
		position = commandEnd
		if command != "definecolor" && command != "providecolor" && command != "colorlet" {
			continue
		}
		cursor := skipTeXSpace(source, commandEnd)
		name, next, ok := parseBraced(source, cursor)
		if !ok {
			continue
		}
		name = strings.ToLower(strings.TrimSpace(name))
		cursor = skipTeXSpace(source, next)
		if command == "colorlet" {
			if cursor < len(source) && source[cursor] == '[' {
				_, optionalEnd, optionalOK := parseDelimited(source, cursor, '[', ']')
				if !optionalOK {
					continue
				}
				cursor = skipTeXSpace(source, optionalEnd)
			}
			spec, end, specOK := parseBraced(source, cursor)
			if specOK {
				if color, found := resolveLatexColor(spec, colors); found && name != "" {
					colors[name] = color
				}
				position = end
			}
			continue
		}
		model, modelEnd, modelOK := parseBraced(source, cursor)
		if !modelOK {
			continue
		}
		cursor = skipTeXSpace(source, modelEnd)
		spec, end, specOK := parseBraced(source, cursor)
		if !specOK {
			continue
		}
		if color, found := parseLatexColorSpec(model, spec, colors); found && name != "" {
			if command != "providecolor" {
				colors[name] = color
			} else if _, exists := colors[name]; !exists {
				colors[name] = color
			}
		}
		position = end
	}
	return colors
}

func parseLatexColorArgument(source string, commandEnd int, colors map[string]renderColor) (renderColor, int, bool, bool) {
	cursor := skipTeXSpace(source, commandEnd)
	model := ""
	if cursor < len(source) && source[cursor] == '[' {
		var ok bool
		model, cursor, ok = parseDelimited(source, cursor, '[', ']')
		if !ok {
			return renderColor{}, commandEnd, false, false
		}
		cursor = skipTeXSpace(source, cursor)
	}
	spec, end, ok := parseBraced(source, cursor)
	if !ok {
		return renderColor{}, commandEnd, false, false
	}
	color, resolved := parseLatexColorSpec(model, spec, colors)
	return color, end, true, resolved
}

func parseLatexColorSpec(model, spec string, colors map[string]renderColor) (renderColor, bool) {
	model = strings.TrimSpace(model)
	spec = strings.TrimSpace(spec)
	if model == "" || strings.EqualFold(model, "named") {
		return resolveLatexColor(spec, colors)
	}
	if strings.EqualFold(model, "HTML") {
		return parseHexColor(strings.TrimPrefix(spec, "#"))
	}
	values, ok := parseColorNumbers(spec)
	if !ok {
		return renderColor{}, false
	}
	switch model {
	case "RGB":
		return colorFromChannels(values, 255)
	case "Gray":
		return colorFromGray(values, 15)
	case "rgb":
		return colorFromChannels(values, 1)
	case "gray":
		return colorFromGray(values, 1)
	case "cmy":
		if len(values) != 3 {
			return renderColor{}, false
		}
		return normalizedColor(1-values[0], 1-values[1], 1-values[2]), true
	case "cmyk":
		if len(values) != 4 {
			return renderColor{}, false
		}
		return normalizedColor((1-values[0])*(1-values[3]), (1-values[1])*(1-values[3]), (1-values[2])*(1-values[3])), true
	case "hsb", "Hsb":
		if len(values) != 3 {
			return renderColor{}, false
		}
		hue := values[0]
		if model == "Hsb" {
			hue /= 360
		}
		return colorFromHSB(hue, values[1], values[2]), true
	default:
		return renderColor{}, false
	}
}

func resolveLatexColor(spec string, colors map[string]renderColor) (renderColor, bool) {
	spec = strings.TrimSpace(spec)
	if strings.HasPrefix(spec, "#") {
		return parseHexColor(strings.TrimPrefix(spec, "#"))
	}
	parts := strings.Split(spec, "!")
	base, ok := colors[strings.ToLower(strings.TrimSpace(parts[0]))]
	if !ok {
		return renderColor{}, false
	}
	for index := 1; index < len(parts); index += 2 {
		percentage, err := strconv.ParseFloat(strings.TrimSpace(parts[index]), 64)
		if err != nil {
			return renderColor{}, false
		}
		other := colors["white"]
		if index+1 < len(parts) && strings.TrimSpace(parts[index+1]) != "" {
			var found bool
			other, found = colors[strings.ToLower(strings.TrimSpace(parts[index+1]))]
			if !found {
				return renderColor{}, false
			}
		}
		weight := math.Max(0, math.Min(100, percentage)) / 100
		base = renderColor{
			red:   int(math.Round(float64(base.red)*weight + float64(other.red)*(1-weight))),
			green: int(math.Round(float64(base.green)*weight + float64(other.green)*(1-weight))),
			blue:  int(math.Round(float64(base.blue)*weight + float64(other.blue)*(1-weight))),
		}
	}
	return base, true
}

func parseCSSColor(value string) (renderColor, bool) {
	value = strings.TrimSpace(value)
	if strings.HasPrefix(value, "#") {
		return parseHexColor(strings.TrimPrefix(value, "#"))
	}
	lower := strings.ToLower(value)
	if strings.HasPrefix(lower, "rgb(") && strings.HasSuffix(lower, ")") {
		body := strings.TrimSpace(value[4 : len(value)-1])
		parts := strings.Split(body, ",")
		if len(parts) != 3 {
			return renderColor{}, false
		}
		channels := make([]float64, 3)
		for index, part := range parts {
			part = strings.TrimSpace(part)
			scale := 255.0
			if strings.HasSuffix(part, "%") {
				scale = 100
				part = strings.TrimSpace(strings.TrimSuffix(part, "%"))
			}
			parsed, err := strconv.ParseFloat(part, 64)
			if err != nil {
				return renderColor{}, false
			}
			channels[index] = parsed * 255 / scale
		}
		return renderColor{clampChannel(channels[0]), clampChannel(channels[1]), clampChannel(channels[2])}, true
	}
	color, ok := baseLatexColors[lower]
	return color, ok
}

func parseHexColor(value string) (renderColor, bool) {
	value = strings.TrimSpace(value)
	if len(value) == 3 || len(value) == 4 {
		value = strings.Repeat(value[0:1], 2) + strings.Repeat(value[1:2], 2) + strings.Repeat(value[2:3], 2)
	} else if len(value) == 8 {
		value = value[:6]
	}
	if len(value) != 6 {
		return renderColor{}, false
	}
	parsed, err := strconv.ParseUint(value, 16, 32)
	if err != nil {
		return renderColor{}, false
	}
	return renderColor{int(parsed >> 16), int((parsed >> 8) & 0xff), int(parsed & 0xff)}, true
}

func parseColorNumbers(spec string) ([]float64, bool) {
	parts := strings.FieldsFunc(spec, func(r rune) bool { return r == ',' || r == ' ' || r == '\t' || r == '\n' || r == '\r' })
	if len(parts) == 0 {
		return nil, false
	}
	values := make([]float64, len(parts))
	for index, part := range parts {
		value, err := strconv.ParseFloat(part, 64)
		if err != nil || math.IsNaN(value) || math.IsInf(value, 0) {
			return nil, false
		}
		values[index] = value
	}
	return values, true
}

func colorFromChannels(values []float64, scale float64) (renderColor, bool) {
	if len(values) != 3 || scale <= 0 {
		return renderColor{}, false
	}
	return normalizedColor(values[0]/scale, values[1]/scale, values[2]/scale), true
}

func colorFromGray(values []float64, scale float64) (renderColor, bool) {
	if len(values) != 1 || scale <= 0 {
		return renderColor{}, false
	}
	return normalizedColor(values[0]/scale, values[0]/scale, values[0]/scale), true
}

func normalizedColor(red, green, blue float64) renderColor {
	return renderColor{clampChannel(red * 255), clampChannel(green * 255), clampChannel(blue * 255)}
}

func clampChannel(value float64) int {
	return int(math.Round(math.Max(0, math.Min(255, value))))
}

func colorFromHSB(hue, saturation, brightness float64) renderColor {
	hue = hue - math.Floor(hue)
	saturation = math.Max(0, math.Min(1, saturation))
	brightness = math.Max(0, math.Min(1, brightness))
	sector := hue * 6
	index := int(math.Floor(sector)) % 6
	fraction := sector - math.Floor(sector)
	p := brightness * (1 - saturation)
	q := brightness * (1 - saturation*fraction)
	t := brightness * (1 - saturation*(1-fraction))
	switch index {
	case 0:
		return normalizedColor(brightness, t, p)
	case 1:
		return normalizedColor(q, brightness, p)
	case 2:
		return normalizedColor(p, brightness, t)
	case 3:
		return normalizedColor(p, q, brightness)
	case 4:
		return normalizedColor(t, p, brightness)
	default:
		return normalizedColor(brightness, p, q)
	}
}

func rewriteMathColors(source string, colors map[string]renderColor) string {
	var output strings.Builder
	for position := 0; position < len(source); {
		relative := strings.IndexByte(source[position:], '\\')
		if relative < 0 {
			output.WriteString(source[position:])
			break
		}
		start := position + relative
		output.WriteString(source[position:start])
		command, commandEnd := readTeXCommand(source, start)
		if command != "color" && command != "textcolor" {
			if commandEnd <= start {
				output.WriteByte(source[start])
				position = start + 1
			} else {
				output.WriteString(source[start:commandEnd])
				position = commandEnd
			}
			continue
		}
		color, end, syntaxOK, resolved := parseLatexColorArgument(source, commandEnd, colors)
		if !syntaxOK || !resolved {
			output.WriteString(source[start:commandEnd])
			position = commandEnd
			continue
		}
		output.WriteByte('\\')
		output.WriteString(command)
		output.WriteString("[RGB]{")
		output.WriteString(color.rgbSpec())
		output.WriteByte('}')
		position = end
	}
	return output.String()
}
