package navigation

// Selection uses zero-based indexes. Index is -1 when the list is empty.
type Selection struct {
	Index int
	Top   int
}

func ClampIndex(index, count int) int {
	if count <= 0 {
		return -1
	}
	return max(0, min(index, count-1))
}

// Normalize clamps selection and scroll position and brings the selected row
// into view. A zero-height transient layout is treated as a one-row viewport.
func (selection *Selection) Normalize(count, rows int) {
	selection.Index = ClampIndex(selection.Index, count)
	if selection.Index < 0 {
		selection.Top = 0
		return
	}
	rows = max(1, rows)
	selection.Top = max(0, min(selection.Top, max(0, count-rows)))
	if selection.Index < selection.Top {
		selection.Top = selection.Index
	} else if selection.Index-selection.Top >= rows {
		selection.Top = selection.Index - rows + 1
	}
}

// Select updates the selection and viewport, reporting whether the index moved.
func (selection *Selection) Select(index, count, rows int) bool {
	previous := selection.Index
	selection.Index = index
	selection.Normalize(count, rows)
	return selection.Index != previous
}
