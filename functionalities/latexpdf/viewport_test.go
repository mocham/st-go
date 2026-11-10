package latexpdf

import (
	"bytes"
	"fmt"
	"strings"
	"testing"
)

func TestSourceViewportPreservesContextAndMoves(t *testing.T) {
	var source strings.Builder
	source.WriteString("\\documentclass{article}\n\\newcommand{\\outside}{z^2}\n\\begin{document}\n")
	for i := 0; i < 80; i++ {
		fmt.Fprintf(&source, "Paragraph %d with $\\outside$ and enough prose to preview.\n\n", i)
	}
	source.WriteString("\\end{document}")
	first, err := PrepareSourceViewport(source.String(), SourceViewportOptions{MaxBytes: 4096})
	if err != nil {
		t.Fatal(err)
	}
	last, err := PrepareSourceViewport(source.String(), SourceViewportOptions{Line: 120, MaxBytes: 4096})
	if err != nil || first.Body == last.Body || last.Block <= first.Block || len(last.Body) > 4096 {
		t.Fatalf("source seek failed: %#v, %v", last, err)
	}
	if strings.Contains(last.Body, `\newcommand`) || !strings.Contains(last.Prepared, `\newcommand`) {
		t.Fatal("viewport did not separate local content from global declarations")
	}
	rendered, err := NewRenderer().RenderPreparedPDFResultWithOptions(Document{Source: last.Prepared, Title: "Viewport"}, last.Body, PDFOptions{PageWidth: 140, PageHeight: 1000})
	if err != nil || rendered.MathErrors != 0 || !bytes.HasPrefix(rendered.PDF, []byte("%PDF-")) {
		t.Fatalf("prepared viewport lost its macros: %v, errors=%d", err, rendered.MathErrors)
	}
	previous, err := PrepareSourceViewport(source.String(), SourceViewportOptions{Block: last.Block, Step: -1})
	if err != nil || previous.Block != last.Block-1 {
		t.Fatal("wheel step did not move one source block")
	}
}

func TestSourceViewportKeepsWholeMathAndBounds(t *testing.T) {
	source := "\\begin{document}\nBefore.\n\n\\[\na+b=c\n\\]\n\nAfter.\n\\end{document}"
	view, err := PrepareSourceViewport(source, SourceViewportOptions{Line: 5, MaxBytes: 128})
	if err != nil || !strings.Contains(view.Body, "\\[\na+b=c\n\\]") {
		t.Fatalf("split display mathematics: %q, %v", view.Body, err)
	}
	if _, err := PrepareSourceViewport(strings.Repeat("x", 1000), SourceViewportOptions{MaxBytes: 128}); err == nil {
		t.Fatal("oversized structural block bypassed the viewport budget")
	}
	end, err := PrepareSourceViewport(source, SourceViewportOptions{Step: 1000000})
	if err != nil || !strings.Contains(end.Body, "After.") {
		t.Fatal("scroll past EOF did not clamp")
	}
}
