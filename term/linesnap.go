package term

// LineText returns the visible text of a screen row (test helper).
func (t *Term) LineText(y int) string {
	s := ""
	for x := 0; x < t.col; x++ {
		if t.line[y][x].Mode&ATTRWdummy != 0 {
			continue
		}
		if t.line[y][x].U != 0 {
			s += string(t.line[y][x].U)
		}
	}
	return s
}

// Cols returns the number of columns (test helper).
func (t *Term) Cols() int { return t.col }

// LineAt returns the glyph at cell (x, y) (test helper).
func (t *Term) LineAt(x, y int) Glyph {
	if y < 0 || y >= t.row || x < 0 || x >= t.col {
		return Glyph{}
	}
	return t.line[y][x]
}
