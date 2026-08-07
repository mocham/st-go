package term

// Tresize resizes the terminal screen, ported from st.c.
func (t *Term) Tresize(col, row int) { t.tresize(col, row) }

func (t *Term) tresize(col, row int) {
	if col < 1 || row < 1 {
		logf("tresize: error resizing to %dx%d\n", col, row)
		return
	}

	// slide screen to keep cursor where we expect it
	i := 0
	for ; i <= t.c.y-row; i++ {
		t.line[i] = nil
		t.alt[i] = nil
	}
	if i > 0 {
		// ensure both src and dst are not nil
		copy(t.line, t.line[i:i+row])
		copy(t.alt, t.alt[i:i+row])
	}
	for j := i + row; j < t.row; j++ {
		t.line[j] = nil
		t.alt[j] = nil
	}

	// resize to new height
	if len(t.line) < row {
		for len(t.line) < row {
			t.line = append(t.line, nil)
			t.alt = append(t.alt, nil)
		}
		t.dirty = append(t.dirty, make([]bool, row-len(t.dirty))...)
	} else {
		t.line = t.line[:row]
		t.alt = t.alt[:row]
		t.dirty = t.dirty[:row]
	}
	if len(t.tabs) < col {
		t.tabs = append(t.tabs, make([]bool, col-len(t.tabs))...)
	} else {
		t.tabs = t.tabs[:col]
	}

	// resize each row to new width, zero-pad if needed
	minrow := min(row, t.row)
	for i := 0; i < minrow; i++ {
		t.line[i] = resizeLine(t.line[i], col)
		t.alt[i] = resizeLine(t.alt[i], col)
	}

	// allocate any new rows
	for i = minrow; i < row; i++ {
		t.line[i] = make(Line, col)
		t.alt[i] = make(Line, col)
	}

	if col > t.col {
		bp := t.col
		for k := bp; k < col; k++ {
			t.tabs[k] = false
		}
		// find last set tab then continue by tabspaces
		last := 0
		for k := 0; k < col; k++ {
			if t.tabs[k] {
				last = k
			}
		}
		for k := last + int(t.cfg.Tabspaces); k < col; k += int(t.cfg.Tabspaces) {
			t.tabs[k] = true
		}
	}

	// update terminal size
	t.col = col
	t.row = row
	// reset scrolling region
	t.tsetscroll(0, row-1)
	// make use of the LIMIT in tmoveto
	t.tmoveto(t.c.x, t.c.y)
	// Clearing both screens (it makes dirty all lines)
	c := t.c
	for i = 0; i < 2; i++ {
		if mincol := min(col, t.col); mincol < col && 0 < minrow {
			t.tclearregion(mincol, 0, col-1, minrow-1)
		}
		if 0 < col && minrow < row {
			t.tclearregion(0, minrow, col-1, row-1)
		}
		t.tswapscreen()
		t.tcursor(cursorLoad)
	}
	t.c = c
}

func resizeLine(l Line, col int) Line {
	if l == nil {
		return make(Line, col)
	}
	if len(l) < col {
		l = append(l, make(Line, col-len(l))...)
	} else if len(l) > col {
		l = l[:col]
	}
	return l
}
