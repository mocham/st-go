package api

// HTMLDocument is immutable after publication. It is presentation content, not
// a terminal emulator or a script executing in the host page.
type HTMLDocument struct {
	HTML string `json:"html"`
	CSS  string `json:"css,omitempty"`
}

// HTMLView retains document/navigation separately so resizing and tab switches
// do not re-render the document. Scroll is cumulative within a PositionID.
type HTMLView struct {
	Document   *HTMLDocument `json:"document,omitempty"`
	PositionID uint64        `json:"positionID"`
	Position   int           `json:"position"`             // millionths of the scroll range
	Scroll     int           `json:"scroll"`               // wheel steps since PositionID
	CaptureTab bool          `json:"captureTab,omitempty"` // application owns Tab/Shift+Tab even while preview is focused
}

type HTMLSupport interface{ SupportsHTML() bool }

// HTMLSurface uses the same placement, visibility and lifetime as pixel panes.
// A nil document restores pixel presentation. Generation guards widget changes.
type HTMLSurface interface {
	PresentHTML(generation uint64, view HTMLView) bool
}
