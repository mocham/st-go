package preview

import (
	"fmt"
	"html"
	"path/filepath"
	"strings"

	"github.com/mocham/GoLS/latexpdf"
	"st-go/tui/writing"
)

// Annotation is presentation-only. The real document contains only its UUID
// comment until the editor commits the chosen source text.
type Annotation struct {
	ID, Document, Text, Reasoning, Status, Error string
	ShowReasoning                                bool
}

func AnnotatedHTML(source *Snapshot, annotations []Annotation) (string, int, error) {
	text := source.Source
	active := make([]Annotation, 0, len(annotations))
	for _, a := range annotations {
		if !writing.ValidUUID(a.ID) {
			return "", 0, fmt.Errorf("invalid proposal ID")
		}
		marker := writing.Marker(a.Document, a.ID)
		if strings.Count(text, marker) != 1 {
			continue
		}
		id := strings.ReplaceAll(a.ID, "-", "")
		body := a.Text
		if strings.TrimSpace(body) == "" {
			body = "Awaiting paragraph prediction."
		}
		text = strings.Replace(text, marker, "\n\nGOVIMAIBEGIN"+id+"\n\n"+body+"\n\nGOVIMAIEND"+id+"\n\n", 1)
		active = append(active, a)
	}
	if source.markdown {
		_, text = cursorMarker(text, true)
		text = latexpdf.MarkdownToLaTeX(text)
	}
	renderer := latexpdf.NewRenderer()
	result := renderer.RenderHTML(text, "")
	body := string(result.Body)
	for _, a := range active {
		id := strings.ReplaceAll(a.ID, "-", "")
		start, end := "<p>GOVIMAIBEGIN"+id+"</p>", "<p>GOVIMAIEND"+id+"</p>"
		if !strings.Contains(body, start) || !strings.Contains(body, end) {
			return "", 0, fmt.Errorf("proposal boundary could not be rendered")
		}
		header := `<section class="govim-ai" id="ai-` + a.ID + `"><h3>Pending insertion · ` + html.EscapeString(a.Status) + `</h3>`
		if a.ShowReasoning && a.Reasoning != "" {
			header += `<div class="ai-reasoning"><strong>Reasoning</strong><pre>` + html.EscapeString(a.Reasoning) + `</pre></div>`
		}
		if a.Error != "" {
			header += `<p>` + html.EscapeString(a.Error) + `</p>`
		}
		header += `<p class="ai-controls">:AIEdit · :AIRefine · :AIIns · :AIReasoning · :AIDismiss</p>`
		body = strings.Replace(body, start, header, 1)
		body = strings.Replace(body, end, "</section>", 1)
	}
	return body, result.MathErrors, nil
}

const AnnotationStyle = `.govim-ai{border:1px solid #b5c4d0;border-left:3px solid #9dafbf;background:#edf0f2;padding:10px 14px;margin:12px 0}.govim-ai h3{font-size:0.9em;color:#52687a}.ai-reasoning{font-size:0.85em;color:#66717b}.ai-reasoning pre{white-space:pre-wrap;font-family:inherit}.ai-controls{font:12px monospace;color:#66717b}`

func RenderAnnotations(source *Snapshot, annotations []Annotation) (latexpdf.PDFResult, int, error) {
	body, errors, err := AnnotatedHTML(source, annotations)
	if err != nil {
		return latexpdf.PDFResult{}, errors, err
	}
	result, err := latexpdf.NewRenderer().RenderAnnotatedHTMLPDF(latexpdf.Document{Source: source.Source, Title: filepath.Base(source.Root)}, body, latexpdf.PDFOptions{PageWidth: PageWidthMM, PageHeight: 297, Compact: true, FontScale: FontScale})
	return result, errors, err
}
