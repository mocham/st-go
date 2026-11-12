package preview

import (
	"strings"
	"testing"

	"st-go/tui/writing"
)

func TestProposalAnnotationsMarkdownAndLatex(t *testing.T) {
	for _, ext := range []string{"md", "tex"} {
		t.Run(ext, func(t *testing.T) {
			path := "/project/paper." + ext
			id := writing.UUID()
			marker := writing.Marker(path, id)
			text := "Before the proposal.\n\n" + marker + "\n\nAfter the proposal.\n"
			if ext == "tex" {
				text = "\\documentclass{article}\n\\begin{document}\n" + text + "\\end{document}"
			}
			source, err := Load(path, func(string) (string, error) { return text, nil })
			if err != nil {
				t.Fatal(err)
			}
			annotations := []Annotation{{ID: id, Document: path, Text: "Proposed paragraph with $x^2$.", Status: "result", Reasoning: "exposed <thought>", ShowReasoning: true}}
			body, _, err := AnnotatedHTML(source, annotations)
			if err != nil {
				t.Fatal(err)
			}
			for _, want := range []string{`id="ai-` + id + `"`, `class="govim-ai"`, "Before the proposal", "After the proposal", "Proposed paragraph", "exposed &lt;thought&gt;"} {
				if !strings.Contains(body, want) {
					t.Fatalf("missing %q in %s", want, body)
				}
			}
			if strings.Contains(body, "GOVIMAIBEGIN") {
				t.Fatal("sentinel leaked")
			}
			result, _, err := RenderAnnotations(source, annotations)
			if err != nil {
				t.Fatal(err)
			}
			anchor, ok := result.Anchors["ai-"+id]
			if !ok || anchor.Page < 1 || anchor.Position < 0 || anchor.Position > 1000000 {
				t.Fatalf("invalid anchor: %+v %v", anchor, ok)
			}
			if !strings.HasPrefix(string(result.PDF), "%PDF-") {
				t.Fatal("missing PDF")
			}
			if source.Source != text {
				t.Fatal("annotation mutated immutable snapshot")
			}
		})
	}
}
