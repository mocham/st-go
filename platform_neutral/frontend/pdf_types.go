package frontend

import "image"

// Rectangles are immutable, page-relative millionths, independent of zoom.
type pdfMatch struct {
	page int // zero-based
	box  image.Rectangle
}

type pdfMatches struct {
	query     string
	hits      []pdfMatch
	active    int
	truncated bool
}

type pdfPageSize struct{ width, height float64 }
