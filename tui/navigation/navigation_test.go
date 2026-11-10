package navigation

import "testing"

func TestPanelDefaultsAndModes(t *testing.T) {
	panel := DefaultPanel()
	if !panel.Floating() || panel.PreviewWidth(80, 47) != 80 || panel.LivePreviewWidth(80, 47) != 45 {
		t.Fatalf("default panel is not a visible overlay: %+v", panel)
	}
	panel.TogglePin()
	if panel.Floating() || panel.PreviewWidth(80, 47) != 45 {
		t.Fatal("pinned panel did not reserve the sidebar and divider")
	}
	panel.Toggle()
	if panel.Visible || panel.PreviewWidth(80, 47) != 80 || panel.LivePreviewWidth(80, 47) != 80 {
		t.Fatal("hidden panel still reserved preview columns")
	}
	panel.TogglePin()
	panel.Toggle()
	if !panel.Floating() {
		t.Fatal("panel did not restore its floating mode")
	}
	if panel.PreviewWidth(0, 0) != 1 || panel.LivePreviewWidth(1, 1) != 1 {
		t.Fatal("small layouts emitted invalid preview dimensions")
	}
}

func TestSelectionClampsAndScrolls(t *testing.T) {
	for _, tc := range []struct {
		name                               string
		index, top, count, rows, want, pos int
	}{
		{"empty", 3, 8, 0, 4, -1, 0},
		{"negative", -4, 8, 10, 4, 0, 0},
		{"past end", 30, 0, 10, 4, 9, 6},
		{"scroll down", 6, 1, 10, 4, 6, 3},
		{"scroll up", 2, 5, 10, 4, 2, 2},
		{"short list", 2, 5, 3, 10, 2, 0},
		{"zero rows", 2, 0, 3, 0, 2, 2},
		{"negative top", 2, -50, 10, 4, 2, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			selection := Selection{Index: tc.index, Top: tc.top}
			selection.Normalize(tc.count, tc.rows)
			if selection.Index != tc.want || selection.Top != tc.pos {
				t.Fatalf("selection = %+v, want index=%d top=%d", selection, tc.want, tc.pos)
			}
		})
	}
	selection := Selection{}
	if !selection.Select(5, 10, 3) || selection.Top != 3 || selection.Select(5, 10, 3) {
		t.Fatalf("select did not report movement correctly: %+v", selection)
	}
}

func TestPageKeepsBoundaryRowAndFocus(t *testing.T) {
	for _, tc := range []struct {
		name                                          string
		index, top, count, rows, direction, want, pos int
	}{
		{"down ignores cursor row", 4, 0, 100, 15, 1, 14, 14},
		{"down from selected bottom", 14, 0, 100, 15, 1, 14, 14},
		{"up ignores cursor row", 25, 14, 100, 15, -1, 14, 0},
		{"up from selected top", 14, 14, 100, 15, -1, 14, 0},
		{"last full page", 90, 85, 100, 15, 1, 99, 85},
		{"first page", 10, 0, 100, 15, -1, 0, 0},
		{"short down", 0, 0, 3, 15, 1, 2, 0},
		{"short up", 2, 0, 3, 15, -1, 0, 0},
		{"single row down", 4, 4, 10, 1, 1, 5, 5},
		{"single row up", 4, 4, 10, 1, -1, 3, 3},
		{"empty", -1, 0, 0, 15, 1, -1, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			selection := Selection{Index: tc.index, Top: tc.top}
			previous := selection
			changed := selection.Page(tc.direction, tc.count, tc.rows)
			if selection.Index != tc.want || selection.Top != tc.pos || changed != (selection != previous) {
				t.Fatalf("page = %+v (changed=%t), want index=%d top=%d", selection, changed, tc.want, tc.pos)
			}
		})
	}
}
