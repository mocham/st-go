package stterminal

import (
	"fmt"
	"strconv"
	"unicode/utf8"

	"st-go/limits"
	"st-go/pdfmeta"
)

// PDFSearchHooks serves bounded search results for ordinary fixed-page previews.
// Token zero cancels; nonempty Path starts a search; otherwise Offset requests
// the next cached reply chunk. Coordinates remain private to the frontend.
type PDFSearchRequest struct {
	Token, Offset int
	Path, Text    string
}

type PDFSearchHooks interface{ SearchPDF(PDFSearchRequest) error }

func (t *Term) dslPDFSearch(args []string) error {
	hooks, ok := t.hooks.(PDFSearchHooks)
	if !ok {
		return fmt.Errorf("PDF preview search is not supported by this frontend")
	}
	if len(args) == 1 && args[0] == "clear" {
		return hooks.SearchPDF(PDFSearchRequest{})
	}
	if len(args) < 2 {
		return fmt.Errorf("pdfsearch requires query, chunk, or clear")
	}
	token, err := strconv.Atoi(args[1])
	if err != nil || token < 1 || token > 1<<30 {
		return fmt.Errorf("invalid PDF search token")
	}
	request := PDFSearchRequest{Token: token}
	switch {
	case len(args) == 4 && args[0] == "query":
		if args[2] == "" || args[3] == "" || len(args[3]) > limits.PreviewSearchBytes || !utf8.ValidString(args[3]) {
			return fmt.Errorf("invalid PDF search query")
		}
		for _, r := range args[3] {
			if r == 0 {
				return fmt.Errorf("invalid PDF search text")
			}
		}
		request.Path, request.Text = t.resolveImagePath(args[2]), args[3]
	case len(args) == 3 && args[0] == "chunk":
		offset, err := strconv.Atoi(args[2])
		if err != nil || offset < 0 || offset > pdfmeta.MaxBytes {
			return fmt.Errorf("invalid PDF search offset")
		}
		request.Offset = offset
	default:
		return fmt.Errorf("invalid PDF search command")
	}
	return hooks.SearchPDF(request)
}
