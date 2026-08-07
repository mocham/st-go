package main

import (
	"strconv"
	"strings"
)

// XGeometryMask mirrors XParseGeometry's mask bits.
type XGeometryMask int

const (
	XValue      XGeometryMask = 1 << 0 // (i.e. the value in x)
	YValue      XGeometryMask = 1 << 1 // (i.e. the value in y)
	WidthValue  XGeometryMask = 1 << 2
	HeightValue XGeometryMask = 1 << 3
	AllValues   XGeometryMask = XValue | YValue | WidthValue | HeightValue
	XNegative   XGeometryMask = 1 << 4
	YNegative   XGeometryMask = 1 << 5
)

// parseGeometry parses st's -g geometry: "WxH+X+Y", where W/H are columns and
// rows (or blank) and X/Y are the window position. Returns the mask of the
// fields that were present, like XParseGeometry.
func parseGeometry(s string, cols, rows *int, x, y *int) XGeometryMask {
	var mask XGeometryMask
	rest := strings.TrimSpace(s)

	// width x height; a bare number with no 'x' is a width.
	hadX := false
	if i := strings.IndexAny(rest, "xX"); i >= 0 {
		hadX = true
		if i > 0 {
			if v, err := strconv.Atoi(rest[:i]); err == nil && v > 0 {
				*cols = v
				mask |= WidthValue
			}
		}
		rest = rest[i+1:]
	}
	if j := strings.IndexAny(rest, "+-"); j > 0 {
		// a number before the position sign
		if v, err := strconv.Atoi(rest[:j]); err == nil && v > 0 {
			*rows = v
			mask |= HeightValue
		}
		rest = rest[j:]
	} else if j == 0 {
		// no height, only position
	} else if rest != "" && hadX {
		// trailing number after 'x' is the height
		if v, err := strconv.Atoi(rest); err == nil && v > 0 {
			*rows = v
			mask |= HeightValue
		}
		rest = ""
	} else if rest != "" {
		// bare number, no 'x' and no position: width
		if v, err := strconv.Atoi(rest); err == nil && v > 0 {
			*cols = v
			mask |= WidthValue
		}
		rest = ""
	}

	// +x+y or -x-y
	for len(rest) > 0 && (rest[0] == '+' || rest[0] == '-') {
		neg := rest[0] == '-'
		rest = rest[1:]
		j := 0
		for j < len(rest) && rest[j] != '+' && rest[j] != '-' {
			j++
		}
		if j == 0 {
			break
		}
		v, err := strconv.Atoi(rest[:j])
		if err != nil {
			break
		}
		if neg {
			v = -v
		}
		if mask&XValue == 0 {
			*x = v
			mask |= XValue
			if neg {
				mask |= XNegative
			}
		} else {
			*y = v
			mask |= YValue
			if neg {
				mask |= YNegative
			}
		}
		rest = rest[j:]
	}
	return mask
}
