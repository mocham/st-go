package main

import (
	"bytes"
	"compress/zlib"
	"context"
	"fmt"
	"io"
	"math"
	"os"
	"regexp"
	"strings"
	"testing"
)

func TestGoLSPreviewFontIsNinetyPercentLarger(t *testing.T) {
	const previousScale = 1.7
	if math.Abs(previewFontScale/previousScale-1.9) > 1e-12 {
		t.Fatalf("font scale = %g, want 90%% above %g", previewFontScale, previousScale)
	}
	opened, err := openCorpus(writeTestCorpus(t))
	if err != nil {
		t.Fatal(err)
	}
	defer opened.close()
	previewer := newDocumentPreviewer(t.TempDir())
	streamPattern := regexp.MustCompile(`(?s)stream\r?\n(.*?)\r?\nendstream`)
	for _, path := range []string{"AA/lecture.tex", "AA/notes.md"} {
		t.Run(path, func(t *testing.T) {
			preview, err := previewer.render(context.Background(), opened, opened.documentPaths[path], 120, 160)
			if err != nil {
				t.Fatal(err)
			}
			pdf, err := os.ReadFile(preview.path)
			if err != nil {
				t.Fatal(err)
			}
			// Check actual PDF body-font operators, not just the configured
			// constant. This also catches stale PDFs rendered at the old scale.
			wanted := []byte(fmt.Sprintf(" %.2f Tf", 11.2*previousScale*1.9))
			for _, match := range streamPattern.FindAllSubmatch(pdf, -1) {
				reader, err := zlib.NewReader(bytes.NewReader(match[1]))
				if err != nil {
					continue
				}
				content, err := io.ReadAll(reader)
				reader.Close()
				if err == nil && bytes.Contains(content, wanted) {
					return
				}
			}
			t.Fatalf("PDF is missing the enlarged body font %q", wanted)
		})
	}
}

func TestSearchSelectionPreviewsBeforeEnteringContents(t *testing.T) {
	opened, err := openCorpus(writeTestCorpus(t))
	if err != nil {
		t.Fatal(err)
	}
	output := &testOutput{}
	browser := &Browser{fd: -1, out: output, corpora: []*corpus{opened}}
	if err := browser.setup(); err != nil {
		opened.close()
		t.Fatal(err)
	}
	defer browser.cleanup()
	browser.view = viewState{kind: viewSearch, corpusIndex: -1, query: "needle"}
	browser.idx = 1
	browser.buildEntries()
	browser.renderAll()
	if browser.view.kind != viewSearch || browser.pages < 1 || !strings.Contains(output.String(), "fit-height page 1") {
		t.Fatalf("search selection did not preview: view=%d pages=%d", browser.view.kind, browser.pages)
	}
	browser.enterSelected()
	if browser.view.kind != viewTOC || !browser.navigation.Visible || len(browser.previewer.cache) != 1 {
		t.Fatal("search result did not enter visible contents using its selected preview")
	}
}

func TestGoLSFloatingNavigationAndPinDoesNotRepaint(t *testing.T) {
	output := &testOutput{}
	browser := &Browser{fd: -1, out: output}
	if err := browser.setup(); err != nil {
		t.Fatal(err)
	}
	defer browser.cleanup()
	browser.terminalSize()
	if !browser.navigation.Floating() || browser.previewW != browser.cols {
		t.Fatalf("default navigation reserves preview space: %+v width=%d", browser.navigation, browser.previewW)
	}
	browser.handleCSI('Z')
	if !browser.navigation.Pinned || browser.previewW != browser.listX-2 || output.Len() != 0 {
		t.Fatalf("pinning did not preserve the current frame: %+v width=%d output=%q", browser.navigation, browser.previewW, output.String())
	}
	browser.navigation.Visible = false
	browser.terminalSize()
	if browser.previewW != browser.cols {
		t.Fatal("hidden pinned navigation reserved preview columns")
	}
	browser.handleCSI('Z')
	if browser.navigation.Pinned || output.Len() != 0 {
		t.Fatal("unpinning hidden navigation repainted the current frame")
	}
}
