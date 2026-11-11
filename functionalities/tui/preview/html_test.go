package preview

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestHTMLPreviewUsesSemanticRendererWithoutPDF(t *testing.T) {
	path := filepath.Join(t.TempDir(), "note.md")
	source, err := LoadWeb(path, func(string) (string, error) { return "# A heading\n\nSome **bold** text and $x^2$.\n", nil })
	if err != nil {
		t.Fatal(err)
	}
	body, mathErrors, err := RenderHTML(source)
	if err != nil || mathErrors != 0 || !strings.Contains(body, "A heading") || !strings.Contains(body, "<strong>") || !strings.Contains(body, "<svg") || strings.Contains(body, "%PDF-") {
		t.Fatalf("semantic HTML: errors=%d, err=%v, body=%s", mathErrors, err, body)
	}
}

func TestHTMLAndJSONSourcePreview(t *testing.T) {
	for _, test := range []struct{ name, text, want string }{
		{"page.html", `<h1 style="color:red">Hello</h1>`, `<h1 style="color:red">Hello</h1>`},
		{"data.json", `{"tag":"<script>"}`, "&lt;script&gt;"},
		{"style.css", "body { color: red; }", "<pre>body { color: red; }</pre>"},
	} {
		t.Run(test.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), test.name)
			source, err := LoadWeb(path, func(string) (string, error) { return test.text, nil })
			if err != nil {
				t.Fatal(err)
			}
			body, _, err := RenderHTML(source)
			if err != nil || !WebSupported(path) || !strings.Contains(body, test.want) || !source.HasFile(path) {
				t.Fatalf("HTML %q, %v", body, err)
			}
		})
	}
}
