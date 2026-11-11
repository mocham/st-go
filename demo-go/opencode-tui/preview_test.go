package main

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"st-go/limits"
	"st-go/platform_neutral/api"
)

func TestRichPreviewRendersMarkdownLatexMacros(t *testing.T) {
	source := "# Result\n\n\\newcommand{\\RR}{\\mathbb{R}}\n\nA map $f:\\RR\\to\\RR$.\n\n$$\n\\int_0^1 x^2\\,dx=\\frac13\n$$\n"
	pdf, mathErrors, err := renderMarkdown(source, t.TempDir())
	if err != nil || mathErrors != 0 || !bytes.HasPrefix(pdf, []byte("%PDF-")) {
		t.Fatalf("rendered %d bytes, math errors %d, err %v", len(pdf), mathErrors, err)
	}
}

func TestQuoteDSL(t *testing.T) {
	if got := quoteDSL("a'b.pdf"); got != `'a'"'"'b.pdf'` {
		t.Fatalf("quote = %q", got)
	}
}

func TestRichPreviewRefusesUnusableMainPane(t *testing.T) {
	view := &richPreview{enabled: true, width: 80}
	if view.canStart(20, 24) || !view.canStart(100, 24) || view.canStart(100, 4) {
		t.Fatal("rich preview did not enforce minimum main-pane geometry")
	}
}

func TestWebResponsePreviewStreamsRestoresAndClearsHTML(t *testing.T) {
	var output bytes.Buffer
	view, err := newRichPreview(&output, 58, true, true)
	if err != nil {
		t.Fatal(err)
	}
	defer view.close()
	if err := view.start(time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := view.setReady(&regionLayout{}); err != nil {
		t.Fatal(err)
	}
	settle := func() {
		t.Helper()
		deadline := time.Now().Add(5 * time.Second)
		for time.Now().Before(deadline) {
			status, err := view.tick(time.Now())
			if err != nil || status != "" {
				t.Fatalf("preview status=%q, %v", status, err)
			}
			if !view.busy && view.pending == nil && view.published == view.generation {
				return
			}
			time.Sleep(time.Millisecond)
		}
		t.Fatal("web rendering did not finish")
	}
	view.request("msg_web", "# Result\n\n\\newcommand{\\RR}{\\mathbb{R}}\n\nA map $f:\\RR\\to\\RR$.\n")
	settle()
	var document api.HTMLDocument
	if json.Unmarshal(view.html, &document) != nil || !strings.Contains(document.HTML, "Result") || !strings.Contains(document.HTML, "<svg") || document.CSS == "" {
		t.Fatal("invalid semantic HTML/math payload")
	}
	if !strings.Contains(output.String(), "\x1bPregion secondary html "+base64.StdEncoding.EncodeToString(view.html)+"\x1b\\") || strings.Contains(output.String(), "region secondary open ") || view.directory != "" {
		t.Fatal("web response entered the PDF path")
	}
	// New stream chunks render while hidden, then restore through the same
	// HTML command when the rich pane is toggled back on.
	if err := view.toggle(time.Now()); err != nil {
		t.Fatal(err)
	}
	output.Reset()
	view.request("msg_web", "# Updated streaming response\n\n**More** text.")
	settle()
	if output.Len() != 0 {
		t.Fatal("hidden response pane emitted visible content")
	}
	view.toggle(time.Now())
	view.setReady(&regionLayout{})
	if !strings.Contains(output.String(), "region secondary html "+base64.StdEncoding.EncodeToString(view.html)) {
		t.Fatal("restored pane lost the latest HTML")
	}
	output.Reset()
	if err := view.clear(); err != nil || len(view.html) != 0 || view.published != 0 || !strings.Contains(output.String(), "region secondary clear") {
		t.Fatal("session switch retained stale HTML", err)
	}
}

func TestLargeWebResponseUsesChunkedDCS(t *testing.T) {
	data, _, err := renderMarkdownHTML(strings.Repeat("plain text ", limits.ControlBytes/10))
	if err != nil || len(data) < limits.ControlBytes {
		t.Fatal("large HTML response could not render", err)
	}
	var output bytes.Buffer
	view := &richPreview{out: &output, enabled: true, web: true, html: data, published: 1}
	if err := view.openPublished(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), "region secondary html begin ") || !strings.HasSuffix(output.String(), "\x1bPregion secondary html commit\x1b\\") {
		t.Fatal("large response did not use atomic chunked transfer")
	}
}

func TestEndJumpWaitsForPublicationAndDoesNotLeakToAnotherPrompt(t *testing.T) {
	var output bytes.Buffer
	view := &richPreview{out: &output, enabled: true, visible: true, pdfPath: "/preview.pdf"}
	view.request("first", "Response")
	if err := view.jumpToEnd(); err != nil || output.Len() != 0 {
		t.Fatal("jump was sent before a document existed", err)
	}
	view.published = view.generation
	if err := view.setReady(&regionLayout{}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), "region secondary open ") || !strings.HasSuffix(output.String(), "\x1bPregion secondary position 1000000\x1b\\") || view.endPending {
		t.Fatal("publication lost deferred jump")
	}
	view.clear()
	view.request("second", "Another response")
	view.jumpToEnd()
	view.clear()
	output.Reset()
	view.request("third", "Selected a different prompt")
	view.published = view.generation
	view.openPublished()
	if strings.Contains(output.String(), "position 1000000") {
		t.Fatal("old prompt's end jump affected a new selection")
	}
}

func TestFileMediaPreviewAndResponseSwitchKeepIndependentResources(t *testing.T) {
	for _, web := range []bool{false, true} {
		name := "pdf"
		if web {
			name = "web"
		}
		t.Run(name, func(t *testing.T) {
			var output bytes.Buffer
			view, err := newRichPreview(&output, 58, true, web)
			if err != nil {
				t.Fatal(err)
			}
			defer view.close()
			view.visible, view.ready = true, true
			settle := func() {
				t.Helper()
				deadline := time.Now().Add(5 * time.Second)
				for time.Now().Before(deadline) {
					status, err := view.tick(time.Now())
					if err != nil || status != "" {
						t.Fatalf("render: %s %v", status, err)
					}
					if !view.busy && view.pending == nil {
						return
					}
					time.Sleep(time.Millisecond)
				}
				t.Fatal("file render timed out")
			}
			pdf, _, err := renderMarkdown("# Attached document\n", t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			if err := view.requestMedia("file:one", &fileMedia{data: pdf, extension: ".pdf"}); err != nil {
				t.Fatal(err)
			}
			settle()
			file := view.mediaPath
			data, err := os.ReadFile(file)
			if err != nil || !bytes.Equal(data, pdf) || !strings.Contains(output.String(), "region secondary open "+quoteDSL(file)) {
				t.Fatal("downloaded PDF was not published")
			}
			view.toggle(time.Now())
			output.Reset()
			view.toggle(time.Now())
			view.setReady(&regionLayout{})
			if !strings.Contains(output.String(), quoteDSL(file)) {
				t.Fatal("F2 restore lost file preview")
			}
			view.clear()
			view.request("prompt:one", "Back to the response with $x^2$.")
			settle()
			if view.mediaPath != "" {
				t.Fatal("return to response retained file media")
			}
			data, err = os.ReadFile(file)
			if err != nil || !bytes.Equal(data, pdf) {
				t.Fatal("response replacement overwrote a retained file resource")
			}
			if web && !strings.Contains(string(view.html), "Back to the response") {
				t.Fatal("HTML response not restored after PDF")
			}
		})
	}
}
