package latexpdf

import (
	"fmt"
	"strings"
)

// SourceViewportOptions positions an approximate preview at a structural
// block, or at a one-based source Line when nonzero, then applies Step blocks.
// MaxBytes bounds the selected body, retaining whole equations/environments.
type SourceViewportOptions struct {
	Block, Line, Step int
	MaxBytes          int
}

type SourceViewport struct {
	Prepared, Body     string
	Block, CursorLine  int
	StartLine, EndLine int
}

// PrepareSourceViewport selects nearby content instead of paginating the full
// document. Lines are approximate after conditional/comment preprocessing.
// A short preceding block supplies context; following blocks fill the window.
func PrepareSourceViewport(source string, options SourceViewportOptions) (SourceViewport, error) {
	prepared := rewriteEditorialCommands(preprocessKnownConditionals(stripLatexComments(normalizeLatexSourceEncoding(source))))
	body := documentBody(prepared)
	bodyStart := 0
	if begin := strings.Index(prepared, `\begin{document}`); begin >= 0 {
		bodyStart = begin + len(`\begin{document}`)
	}
	line := 1 + strings.Count(prepared[:bodyStart], "\n")
	type block struct{ start, end, line, endLine int }
	var blocks []block
	for pos := 0; pos < len(body); {
		content := skipUnicodeSpace(body, pos)
		if content == len(body) {
			break
		}
		end := content + latexRenderBlockEnd(body[content:])
		if end <= content {
			end = content + 1
		}
		endLine := line + strings.Count(body[pos:end], "\n")
		blocks = append(blocks, block{pos, end, line + strings.Count(body[pos:content], "\n"), endLine})
		if len(blocks) > 65536 {
			return SourceViewport{}, fmt.Errorf("too many source blocks for a preview")
		}
		pos, line = end, endLine
	}
	if len(blocks) == 0 {
		return SourceViewport{Prepared: prepared, Body: body, CursorLine: line, StartLine: line, EndLine: line}, nil
	}
	index := max(0, min(options.Block, len(blocks)-1))
	if options.Line > 0 {
		index = 0
		for index+1 < len(blocks) && blocks[index].endLine < options.Line {
			index++
		}
	}
	index = int(max(int64(0), min(int64(index)+int64(options.Step), int64(len(blocks)-1))))
	maximum := options.MaxBytes
	if maximum <= 0 {
		maximum = 128 << 10
	}
	if blocks[index].end-blocks[index].start > maximum {
		return SourceViewport{}, fmt.Errorf("source block exceeds the %d-byte viewport limit", maximum)
	}
	start, finish := index, index
	if index > 0 && blocks[index-1].end-blocks[index-1].start < 256 && blocks[index].end-blocks[index-1].start <= maximum {
		start--
	}
	for finish+1 < len(blocks) {
		candidate := blocks[finish+1]
		if candidate.end-blocks[start].start > maximum ||
			blocks[finish].end-blocks[start].start >= 2048 ||
			blocks[finish].endLine-blocks[start].line >= 40 {
			break
		}
		finish++
	}
	return SourceViewport{
		Prepared: prepared, Body: body[blocks[start].start:blocks[finish].end],
		Block: index, CursorLine: blocks[index].line,
		StartLine: blocks[start].line, EndLine: blocks[finish].endLine,
	}, nil
}

// RenderPreparedPDFResultWithOptions renders a selected body against the full
// prepared document's macros, counters, references, and colors. Unlike a
// standalone source fragment it retains context declared outside the viewport.
func (renderer *Renderer) RenderPreparedPDFResultWithOptions(document Document, body string, options PDFOptions) (PDFResult, error) {
	key := PDFCacheKeyWithOptions(document, options) + "-viewport-" + sourceCacheKey(body)
	rendered := renderer.RenderPreparedHTML(document.Source, body, key)
	pdf, pages, err := renderer.pdf.render(document, rendered.Body, rendered.Sections, key, options)
	if err != nil {
		return PDFResult{}, err
	}
	sections := append([]Section(nil), rendered.Sections...)
	for i := range sections {
		sections[i].Page = pages[sections[i].ID]
	}
	return PDFResult{PDF: pdf, MathErrors: rendered.MathErrors,
		MathErrorExpressions: append([]string(nil), rendered.MathErrorExpressions...), Sections: sections}, nil
}
