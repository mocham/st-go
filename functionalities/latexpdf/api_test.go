package latexpdf_test

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/mocham/GoLS/latexpdf"
)

func TestStandalonePublicPipeline(t *testing.T) {
	renderer := latexpdf.NewRenderer()
	source := `\documentclass{article}
\newtheorem{theorem}{Theorem}[section]
\begin{document}
\tableofcontents
\section{Reusable renderer}
\begin{theorem}\label{thm:one}Vector formula $x^2+y^2$.\end{theorem}
See Theorem~\ref{thm:one}.
\end{document}`

	html := renderer.RenderHTML(source, "")
	if html.MathErrors != 0 || !strings.Contains(string(html.Body), `href="#ref-thm-one"`) {
		t.Fatalf("standalone HTML render failed: errors=%d body=%s", html.MathErrors, html.Body)
	}
	document := latexpdf.Document{
		Source: source, Title: "Reusable renderer", Author: "Package test",
		Modified: time.Unix(1_700_000_000, 0),
	}
	data, err := renderer.RenderPDF(document)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.HasPrefix(data, []byte("%PDF-")) || bytes.Contains(data, []byte("/Subtype /Image")) {
		t.Fatalf("standalone pipeline returned a non-vector PDF (%d bytes)", len(data))
	}
	changed := document
	changed.Source += " "
	if key := latexpdf.PDFCacheKey(document); len(key) != 64 || key == latexpdf.PDFCacheKey(changed) {
		t.Fatalf("invalid content cache key %q", key)
	}
	data[0] = 'X'
	cached, err := renderer.RenderPDF(document)
	if err != nil || !bytes.HasPrefix(cached, []byte("%PDF-")) {
		t.Fatal("caller mutation corrupted the renderer's in-memory PDF cache")
	}
}

func TestPDFResultReportsSectionPagesAndDiagnostics(t *testing.T) {
	renderer := latexpdf.NewRenderer()
	source := `\documentclass{article}
\begin{document}
\section{First section}
First page text with $x+y$.
\section{Second section}
Second page text.
\end{document}`
	document := latexpdf.Document{Source: source, Title: "PDF result"}
	result, err := renderer.RenderPDFResultWithOptions(document, latexpdf.PDFOptions{
		PageWidth: 120, PageHeight: 90,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.HasPrefix(result.PDF, []byte("%PDF-")) || result.MathErrors != 0 {
		t.Fatalf("PDF result = %d bytes, %d math errors", len(result.PDF), result.MathErrors)
	}
	if len(result.Sections) != 2 || result.Sections[0].Title != "First section" || result.Sections[1].Title != "Second section" {
		t.Fatalf("PDF sections = %#v", result.Sections)
	}
	if result.Sections[0].Page < 1 || result.Sections[1].Page < result.Sections[0].Page {
		t.Fatalf("PDF section pages = %#v", result.Sections)
	}
	if !bytes.Contains(result.PDF, []byte("/Outlines")) {
		t.Fatal("PDF result has no document outline")
	}

	result.PDF[0] = 'X'
	result.Sections[0].Title = "mutated"
	cached, err := renderer.RenderPDFResultWithOptions(document, latexpdf.PDFOptions{
		PageWidth: 120, PageHeight: 90,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.HasPrefix(cached.PDF, []byte("%PDF-")) || cached.Sections[0].Title != "First section" {
		t.Fatal("caller mutation corrupted the cached PDF result")
	}
}
