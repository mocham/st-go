package main

import (
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"

	"st-go/platform_neutral/api"
)

func TestWebPreviewPublishesHTMLInsteadOfPDF(t *testing.T) {
	e, out, _ := newPreviewTestEditor(t, "page.html", "<h1>Initial; 'quoted' 世界</h1>")
	e.preview.options.web = true
	e.preview.render = renderPreview
	settlePreview(t, e)
	text := out.String()
	prefix := "\x1bPregion secondary html "
	_, encoded, ok := strings.Cut(text, prefix)
	if !ok || strings.Contains(text, "region secondary open ") || e.preview.directory != "" {
		t.Fatal("web preview created a PDF or omitted HTML", text)
	}
	encoded, _, _ = strings.Cut(encoded, "\x1b\\")
	data, err := base64.StdEncoding.DecodeString(encoded)
	var document api.HTMLDocument
	if err != nil || json.Unmarshal(data, &document) != nil || document.HTML != "<h1>Initial; 'quoted' 世界</h1>" || !strings.Contains(document.CSS, e.preview.options.background) {
		t.Fatalf("bad HTML payload %q, %v", data, err)
	}
	out.Reset()
	e.replace(4, 11, "Updated", 0)
	e.execute("w")
	settlePreview(t, e)
	if !strings.Contains(out.String(), prefix) || strings.Contains(out.String(), "region secondary open ") {
		t.Fatal("saving web preview did not publish fresh HTML")
	}
}
