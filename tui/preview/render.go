package preview

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/mocham/GoLS/latexpdf"
	"st-go/limits"
)

// Render uses the existing selectable-text/vector-math PDF pipeline in-process.
// One renderer per saved revision releases its document/formula caches afterward.
func Render(source *Snapshot) (pdf []byte, mathErrors int, err error) {
	defer func() {
		if problem := recover(); problem != nil {
			pdf = nil
			err = fmt.Errorf("preview render: %v", problem)
		}
	}()
	text := source.Source
	if source.markdown {
		_, text = cursorMarker(text, true)
		text = latexpdf.MarkdownToLaTeX(text)
	}
	title := strings.TrimSuffix(filepath.Base(source.Root), filepath.Ext(source.Root))
	if value, ok := latexpdf.FindCommandArgument(text, "title"); ok {
		title = latexpdf.PlainText(value)
	}
	author, _ := latexpdf.FindCommandArgument(text, "author")
	rendered, err := latexpdf.NewRenderer().RenderPDFResultWithOptions(latexpdf.Document{Source: text, Title: title, Author: latexpdf.PlainText(author)}, latexpdf.PDFOptions{PageWidth: 140, PageHeight: 297, Compact: true, FontScale: 0.8})
	if err == nil && len(rendered.PDF) > limits.FileBytes {
		err = fmt.Errorf("preview PDF exceeds resource limit")
	}
	return rendered.PDF, rendered.MathErrors, err
}
