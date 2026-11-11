package preview

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"html"
	"path/filepath"
	"strings"

	"github.com/mocham/GoLS/latexpdf"
	"st-go/limits"
)

const HTMLStyle = `body{font:18px/1.55 Georgia,serif;color:#242424;margin:20px}h1,h2,h3,h4{line-height:1.25}pre,code{font-family:monospace}pre{padding:12px;background:#eee;white-space:pre-wrap}table{border-collapse:collapse;max-width:100%}td,th{padding:4px 8px;border:1px solid #aaa}.math-inline{display:inline-block}.math-display{display:block;text-align:center;overflow-x:auto;margin:1em 0}.math-inline svg,.math-display svg{vertical-align:middle}blockquote{border-left:3px solid #aaa;padding-left:1em}a{color:#246}`

func WebSupported(path string) bool {
	if Supported(path) {
		return true
	}
	switch strings.ToLower(filepath.Ext(path)) {
	case ".html", ".htm", ".css", ".json":
		return true
	}
	return false
}

func LoadWeb(path string, read Reader) (*Snapshot, error) {
	if Supported(path) {
		return Load(path, read)
	}
	path = cleanPath(path)
	text, err := read(path)
	if err != nil {
		return nil, err
	}
	if len(text) > limits.FileBytes {
		return nil, fmt.Errorf("web preview source exceeds resource limit")
	}
	return &Snapshot{Source: text, Root: path, Hash: sha256.Sum256([]byte(path + "\x00" + text)), files: map[string]bool{path: true}}, nil
}

// RenderHTML bypasses PDF pagination, native PDF decoding and pixel uploads.
// The existing renderer already produces semantic HTML with inline SVG math.
func RenderHTML(source *Snapshot) (body string, mathErrors int, err error) {
	switch strings.ToLower(filepath.Ext(source.Root)) {
	case ".html", ".htm":
		return source.Source, 0, nil
	case ".css", ".json":
		text := source.Source
		if strings.EqualFold(filepath.Ext(source.Root), ".json") {
			var pretty bytes.Buffer
			if json.Indent(&pretty, []byte(text), "", "  ") == nil {
				text = pretty.String()
			}
		}
		return "<pre>" + html.EscapeString(text) + "</pre>", 0, nil
	}
	text := source.Source
	if source.markdown {
		_, text = cursorMarker(text, true)
		text = latexpdf.MarkdownToLaTeX(text)
	}
	result := latexpdf.NewRenderer().RenderHTML(text, "")
	return string(result.Body), result.MathErrors, nil
}
