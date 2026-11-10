package preview

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/mocham/GoLS/latexpdf"
	"st-go/limits"
)

const (
	PageWidthMM         = 140.0
	FontScale           = 0.6848 // 7% larger than the shared 0.64 scale.
	DefaultWidthPercent = 60
)

// FontScaleForPane matches govim's default fit-width preview text on the same
// terminal. Pane-sized PDFs need a different PDF font scale than govim's fixed
// 140 mm page; equal raw scales would not produce equal on-screen text sizes.
func FontScaleForPane(pageWidthMM float64, paneColumns, terminalColumns int) float64 {
	if pageWidthMM <= 0 || paneColumns <= 0 || terminalColumns <= 0 {
		return FontScale
	}
	referenceColumns := terminalColumns * DefaultWidthPercent / 100
	return FontScale * pageWidthMM / PageWidthMM * float64(max(1, referenceColumns)) / float64(paneColumns)
}

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
	rendered, err := latexpdf.NewRenderer().RenderPDFResultWithOptions(latexpdf.Document{Source: text, Title: title, Author: latexpdf.PlainText(author)}, latexpdf.PDFOptions{PageWidth: PageWidthMM, PageHeight: 297, Compact: true, FontScale: FontScale})
	if err == nil && len(rendered.PDF) > limits.FileBytes {
		err = fmt.Errorf("preview PDF exceeds resource limit")
	}
	return rendered.PDF, rendered.MathErrors, err
}
