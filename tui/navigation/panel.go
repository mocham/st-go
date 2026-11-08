// Package navigation provides reusable state and geometry for terminal UI
// navigation panels and lists. It performs no I/O or rendering; applications
// retain their own entries, input handling, previews and redraw policy.
package navigation

// Panel describes an optionally visible right-hand navigation overlay.
// Pinned panels reserve columns; floating panels leave preview geometry alone.
type Panel struct {
	Visible bool
	Pinned  bool
}

// DefaultPanel starts visible and floating.
func DefaultPanel() Panel { return Panel{Visible: true} }

func (panel *Panel) Toggle()    { panel.Visible = !panel.Visible }
func (panel *Panel) TogglePin() { panel.Pinned = !panel.Pinned }

func (panel Panel) Floating() bool { return panel.Visible && !panel.Pinned }

// PreviewWidth returns the preview's width in cells. sidebarColumn is the
// sidebar's one-based starting column; its preceding divider is reserved too.
func (panel Panel) PreviewWidth(columns, sidebarColumn int) int {
	if panel.Visible && panel.Pinned {
		return max(1, min(columns, sidebarColumn-2))
	}
	return max(1, columns)
}

// LivePreviewWidth excludes visible navigation even in floating mode. Use it
// for animations whose live rectangle must not overlap ordinary sidebar writes.
func (panel Panel) LivePreviewWidth(columns, sidebarColumn int) int {
	if panel.Visible {
		return max(1, min(columns, sidebarColumn-2))
	}
	return max(1, columns)
}
