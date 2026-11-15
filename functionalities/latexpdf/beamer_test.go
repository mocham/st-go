package latexpdf

import (
	"bytes"
	"math"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"golang.org/x/net/html"
)

// Exercises the structures used in triangular.tex without depending on a
// sibling checkout, installed TeX, or a PDF viewer.
const beamerTestSource = `\documentclass[aspectratio=169,11pt]{beamer}
\newcommand{\M}{\mathfrak M}
\setbeamertemplate{footline}{\scriptsize Custom footer}
\begin{document}
\begin{frame}<1->[fragile]{Tame descent}{Two charts}
\small
Choose $p>2$ and $\M=\M_1\oplus\M_\sigma$.
\begin{columns}[T,onlytextwidth]
\begin{column}{.47\textwidth}
\begin{block}{Height}
\scriptsize
LEFTMARK
\begin{itemize}[<+->]
\item<1-> $v\M\subseteq\operatorname{im}\Phi\subseteq\M$;
\item<2-> $\det\Phi\in vA[[u]]^\times$.
\end{itemize}
\end{block}
\end{column}
\begin{column}{.49\linewidth}
\begin{alertblock}{Cocycle}
\scriptsize
RIGHTMARK
\begin{align*}
\sigma(S_{1\sigma})S_{\sigma1}&=I,\\
\sigma(S_{\sigma1})S_{1\sigma}&=I.
\end{align*}
\end{alertblock}
\end{column}
\end{columns}
\end{frame}
\frame[plain]{\frametitle[Short title]{The overlap}\framesubtitle{Polynomial}
\footnotesize\only<2->{Visible handout text.}\pause
\[\boxed{cd=1,\qquad z_d=-d^2z_c.}\]
}
\begin{frame}{Final slide}NORMALMARK\end{frame}
\end{document}`

func TestBeamerFramesColumnsAndPreparedNavigation(t *testing.T) {
	renderer := NewRenderer()
	result := renderer.RenderHTML(beamerTestSource, "")
	body := string(result.Body)
	if result.MathErrors != 0 || strings.Contains(body, `class="unknown-command"`) {
		t.Fatalf("Beamer diagnostics: %v, body=%s", result.MathErrorExpressions, body)
	}
	for _, expected := range []string{`width:47%`, `width:49%`, `data-tex-size="0.7"`, `beamer-columns`, `beamer-block`, `Two charts`, `Visible handout text.`, `Polynomial`} {
		if !strings.Contains(body, expected) {
			t.Errorf("missing %q", expected)
		}
	}
	root, err := html.Parse(strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	text := htmlText(root)
	for _, leaked := range []string{`.47`, `\textwidth`, `<1->`, `[plain]`, `Custom footer`, `Short title`} {
		if strings.Contains(text, leaked) {
			t.Errorf("layout source leaked as text: %q", leaked)
		}
	}
	if len(result.Sections) != 3 || result.Sections[0].Title != "Tame descent" || result.Sections[1].Title != "The overlap" {
		t.Fatalf("frame navigation: %+v", result.Sections)
	}
	prepared, pages := PreparePages(beamerTestSource, 0)
	if len(pages) != 1 {
		t.Fatalf("short frames should share an HTML page, got %d pages", len(pages))
	}
	if strings.Contains(body, "break-before") || strings.Contains(body, "aspect-ratio") || !strings.Contains(body, "border:1px solid") || !strings.Contains(body, "margin:1.25em 0") {
		t.Fatal("HTML frames are not continuous padded boxes")
	}
	navigation := DocumentSections(pages)
	if len(navigation) != 3 {
		t.Fatalf("HTML navigation: %+v", navigation)
	}
	sectionIndex := 0
	for index, page := range pages {
		fragment := renderer.RenderPreparedHTML(prepared, page.Source, "")
		if page.Oversized || len(fragment.Sections) == 0 || strings.Count(string(fragment.Body), `class="beamer-frame"`) != len(fragment.Sections) {
			t.Fatalf("page %d lost its frame", index+1)
		}
		for _, section := range fragment.Sections {
			if section.ID != navigation[sectionIndex].ID || section.ID != result.Sections[sectionIndex].ID || navigation[sectionIndex].Page != index+1 {
				t.Fatalf("page %d has inconsistent frame destinations: %+v / %+v", index+1, fragment.Sections, navigation[sectionIndex])
			}
			sectionIndex++
		}
	}
}

func TestBeamerPDFContinuousBoxesColumnsAndFontSizes(t *testing.T) {
	result, err := NewRenderer().RenderPDFResult(Document{Source: beamerTestSource, Title: "Generatedtitlemustnotappear"})
	if err != nil {
		t.Fatal(err)
	}
	if result.MathErrors != 0 {
		t.Fatalf("math errors: %v", result.MathErrorExpressions)
	}
	if count := len(regexp.MustCompile(`/Type\s*/Page\b`).FindAll(result.PDF, -1)); count != 1 {
		t.Fatalf("short frames should share a PDF page, got %d pages", count)
	}
	for _, section := range result.Sections {
		if section.Page != 1 {
			t.Errorf("frame %s is on page %d", section.Title, section.Page)
		}
	}
	box := regexp.MustCompile(`/MediaBox\s*\[0 0 ([0-9.]+) ([0-9.]+)\]`).FindSubmatch(result.PDF)
	if len(box) != 3 {
		t.Fatal("missing slide MediaBox")
	}
	w, _ := strconv.ParseFloat(string(box[1]), 64)
	h, _ := strconv.ParseFloat(string(box[2]), 64)
	if math.Abs(w-210*72/25.4) > .02 || math.Abs(h-297*72/25.4) > .02 {
		t.Fatalf("expected normal A4 pages, got %gx%gpt", w, h)
	}
	content := inflatedPDFStreams(result.PDF)
	var previousBottom float64
	frameBorders := 0
	for _, match := range regexp.MustCompile(`([0-9.]+) ([0-9.]+) ([0-9.]+) (-[0-9.]+) re S`).FindAllSubmatch(content, -1) {
		width, _ := strconv.ParseFloat(string(match[3]), 64)
		if math.Abs(width-(210-pdfMarginLeft-pdfMarginRight)*72/25.4) > .02 {
			continue
		}
		top, _ := strconv.ParseFloat(string(match[2]), 64)
		height, _ := strconv.ParseFloat(string(match[4]), 64)
		if frameBorders > 0 && previousBottom-top < 5.9*72/25.4 {
			t.Fatal("missing gap between frame borders")
		}
		previousBottom = top + height
		frameBorders++
	}
	if frameBorders != 3 {
		t.Fatalf("got %d full-width frame borders, want 3", frameBorders)
	}
	position := func(text string) (float64, float64, float64) {
		t.Helper()
		end := bytes.Index(content, tablePDFText(text))
		if end < 0 {
			t.Fatalf("lost selectable text %q", text)
		}
		start := bytes.LastIndex(content[:end], []byte("BT "))
		match := regexp.MustCompile(`BT ([0-9.]+) ([0-9.]+) Td`).FindSubmatch(content[start:end])
		if len(match) != 3 {
			t.Fatalf("missing position for %q", text)
		}
		x, _ := strconv.ParseFloat(string(match[1]), 64)
		y, _ := strconv.ParseFloat(string(match[2]), 64)
		fonts := regexp.MustCompile(`/F\w+ ([0-9.]+) Tf`).FindAllSubmatch(content[:end], -1)
		if len(fonts) == 0 {
			t.Fatal("missing text font")
		}
		size, _ := strconv.ParseFloat(string(fonts[len(fonts)-1][1]), 64)
		return x, y, size
	}
	x1, y1, size1 := position("LEFTMARK")
	x2, y2, size2 := position("RIGHTMARK")
	if x2 <= x1+100 || math.Abs(y1-y2) > .02 {
		t.Fatalf("columns are not side by side: (%g,%g), (%g,%g)", x1, y1, x2, y2)
	}
	if math.Abs(size1-11.2*.7) > .01 || size1 != size2 {
		t.Fatalf("scriptsize fonts = %g, %g", size1, size2)
	}
	_, _, normal := position("NORMALMARK")
	if math.Abs(normal-11.2) > .01 {
		t.Fatalf("font size leaked between frames: %g", normal)
	}
	if bytes.Contains(content, tablePDFText("Generatedtitlemustnotappear")) {
		t.Fatal("generated title consumed a slide")
	}
}

func TestBeamerDenseFrameFitsPageAndOverrides(t *testing.T) {
	source := `\documentclass{beamer}\begin{document}\begin{frame}{Dense}` + strings.Repeat("A paragraph that must stay on this slide.\n\n", 30) + `ENDMARK\end{frame}\begin{frame}{Next}NEXTMARK\end{frame}\end{document}`
	renderer := NewRenderer()
	for _, options := range []PDFOptions{{}, {PageWidth: 180, PageHeight: 110, Compact: true}} {
		result, err := renderer.RenderPDFResultWithOptions(Document{Source: source}, options)
		if err != nil {
			t.Fatal(err)
		}
		if count := len(regexp.MustCompile(`/Type\s*/Page\b`).FindAll(result.PDF, -1)); count != 2 {
			t.Fatalf("dense frame split into %d pages", count)
		}
		content := inflatedPDFStreams(result.PDF)
		for _, marker := range []string{"ENDMARK", "NEXTMARK"} {
			if bytes.Count(content, tablePDFText(marker)) != 1 {
				t.Fatalf("lost or duplicated %s", marker)
			}
		}
		if result.Sections[0].Page != 1 || result.Sections[1].Page != 2 {
			t.Fatalf("wrong fitted-frame destinations: %+v", result.Sections)
		}
		if bytes.Contains(result.PDF, []byte("/CropBox")) != options.Compact {
			t.Fatal("continuous boxes did not honor compact preview cropping")
		}
	}
}

func TestTextSizeScopeAndAdjacentStarredHeadings(t *testing.T) {
	source := `\begin{document}Before\section*{Unnumbered}After.
\small SMALLMARK {\scriptsize SCRIPTMARK} SMALLAGAIN

STILLSMALL
\[x+y\]
\normalsize NORMALMARK
\begin{itemize}\scriptsize\item SMALLITEM\end{itemize}
\section{Numbered}Tail\end{document}`
	renderer := NewRenderer()
	result := renderer.RenderHTML(source, "")
	if len(result.Sections) != 2 || result.Sections[0].Number != "" || result.Sections[1].Number != "1" {
		t.Fatalf("starred headings: %+v", result.Sections)
	}
	if strings.Contains(string(result.Body), `unknown-command`) {
		t.Fatalf("text declaration diagnostic: %s", result.Body)
	}
	if got := PlainText(`\section*{Unnumbered}`); got != "Unnumbered" {
		t.Fatalf("starred heading plaintext = %q", got)
	}
	root, err := html.Parse(strings.NewReader(string(result.Body)))
	if err != nil {
		t.Fatal(err)
	}
	var paragraph *html.Node
	var visit func(*html.Node)
	visit = func(node *html.Node) {
		if node.Data == "p" && strings.Contains(htmlText(node), "STILLSMALL") {
			paragraph = node
		}
		for child := node.FirstChild; child != nil; child = child.NextSibling {
			visit(child)
		}
	}
	visit(root)
	if paragraph == nil || htmlAttribute(paragraph, "data-tex-size") != "0.9" {
		t.Fatal("small declaration did not persist across paragraphs")
	}
	// A declaration in a group must not compound the inherited size, nor leak.
	layout := &pdfDocumentLayout{textSize: .9}
	inlineRoot, _ := html.Parse(strings.NewReader(`<p><span data-tex-size="0.7">small</span> normal</p>`))
	runs := layout.collectInlineChildren(findHTMLNode(inlineRoot, "p"), bodyPDFStyle())
	if len(runs) != 2 || math.Abs(runs[0].style.size*.9-11.2*.7) > .001 || runs[1].style.size != 11.2 {
		t.Fatalf("nested sizes compounded or leaked: %+v", runs)
	}
}

func TestBeamerTitlePageAndIncompleteInput(t *testing.T) {
	renderer := NewRenderer()
	source := `\documentclass{beamer}\title[Short]{Long title}\author{An Author}\begin{document}\begin{frame}\titlepage\end{frame}\end{document}`
	result := renderer.RenderHTML(source, "")
	if !strings.Contains(string(result.Body), "Long title") || !strings.Contains(string(result.Body), "An Author") || strings.Contains(string(result.Body), "unknown-command") {
		t.Fatalf("title page: %s", result.Body)
	}
	// Incomplete source is common while editing; trailing control sequences must
	// not send the frame/preamble scanners back to the start of the document.
	renderer.RenderHTML(`\documentclass{article}\`, "")
	renderer.RenderHTML(`\documentclass{beamer}\begin{document}\begin{frame}{Title}Body\`, "")
	_, _, body, _ := beamerFrameHeading("Body\\", false)
	if body != "Body\\" {
		t.Fatalf("incomplete frame body changed: %q", body)
	}
}

func TestBeamerSectionDeclarationsAndColumnCommands(t *testing.T) {
	source := `\documentclass{beamer}\begin{document}\section*{Topic}
\begin{frame}{Columns}\begin{columns}[T]\column{.4\textwidth}Left\column[t]{.5\textwidth}Right\end{columns}\end{frame}
\begin{frame}{End}Done\end{frame}\end{document}`
	renderer := NewRenderer()
	result, err := renderer.RenderPDFResult(Document{Source: source})
	if err != nil {
		t.Fatal(err)
	}
	if count := len(regexp.MustCompile(`/Type\s*/Page\b`).FindAll(result.PDF, -1)); count != 1 {
		t.Fatalf("section declaration created an extra slide: %d", count)
	}
	if len(result.Sections) != 3 || result.Sections[0].Page != 1 || result.Sections[1].Page != 1 || result.Sections[2].Page != 1 {
		t.Fatalf("section/frame navigation: %+v", result.Sections)
	}
	_, pages := PreparePages(source, 0)
	if len(pages) != 1 {
		t.Fatalf("section declaration created an empty HTML page: %d", len(pages))
	}
	body := string(renderer.RenderHTML(source, "").Body)
	if strings.Contains(body, "unknown-command") || strings.Count(body, `class="beamer-column"`) != 2 {
		t.Fatalf("column commands: %s", body)
	}
}

func TestTextWidthRulesHavePDFGeometry(t *testing.T) {
	result := NewRenderer().RenderHTML(`\rule{.5\textwidth}{.8pt}`, "")
	root, err := html.Parse(strings.NewReader(string(result.Body)))
	if err != nil {
		t.Fatal(err)
	}
	layout := &pdfDocumentLayout{pageWidth: 160, pageHeight: 90, marginLeft: 7, marginRight: 7, fontScale: 1, textSize: 1}
	runs := layout.collectInlineChildren(findHTMLNode(root, "p"), bodyPDFStyle())
	if len(runs) != 1 || runs[0].formula == nil {
		t.Fatalf("rule has no PDF geometry: %+v", runs)
	}
	formula := runs[0].formula
	if math.Abs(formula.viewWidth-73) > .001 || math.Abs(formula.viewHeight-.8*25.4/72) > .001 {
		t.Fatalf("wrong textwidth/point dimensions: %+v", formula)
	}
}
