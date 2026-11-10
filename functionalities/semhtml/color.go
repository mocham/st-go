package semhtml

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

var cssNamedColors = map[string]renderColor{
	"black": {0, 0, 0}, "silver": {192, 192, 192}, "gray": {128, 128, 128},
	"white": {255, 255, 255}, "maroon": {128, 0, 0}, "red": {255, 0, 0},
	"purple": {128, 0, 128}, "fuchsia": {255, 0, 255}, "green": {0, 128, 0},
	"lime": {0, 255, 0}, "olive": {128, 128, 0}, "yellow": {255, 255, 0},
	"navy": {0, 0, 128}, "blue": {0, 0, 255}, "teal": {0, 128, 128},
	"aqua": {0, 255, 255}, "cyan": {0, 255, 255}, "orange": {255, 165, 0},
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
	color, ok := cssNamedColors[lower]
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

func clampChannel(value float64) int {
	return int(math.Round(math.Max(0, math.Min(255, value))))
}
