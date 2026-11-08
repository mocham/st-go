// Package pdfmeta defines the bounded metadata replies used by terminal PDF
// clients. Extraction remains in st-go's native PDF worker, not in the TUI.
package pdfmeta

const (
	MaxBytes   = 1 << 20
	ChunkBytes = 3072
	MaxEntries = 4096
	TextBytes  = MaxBytes / 4
)

type Entry struct {
	Title string `json:"title"`
	Page  int    `json:"page"` // One-based; zero means no local page destination.
	Level int    `json:"level"`
}

type Payload struct {
	TOC       []Entry `json:"toc,omitempty"`
	Text      string  `json:"text,omitempty"`
	Truncated bool    `json:"truncated,omitempty"`
	Error     string  `json:"error,omitempty"`
}
