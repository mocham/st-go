package latexpdf

import (
	"bytes"
	"strings"
	"testing"

	"golang.org/x/net/html"
)

const colorBoxTestSource = `\documentclass{amsart}
\definecolor{ink}{HTML}{14213D}
\definecolor{paper}{HTML}{F6F3EC}
\newcommand{\slideheader}[2]{\begin{tcolorbox}[colback=ink,colframe=ink,boxrule=0pt,arc=0pt,left=7mm,right=7mm,top=3.5mm,bottom=3.5mm]
{\color{white}\bfseries\LARGE 问题 #1}\hfill{\color{white!82}\bfseries\Large #2}\end{tcolorbox}}
\newenvironment{promptbox}{\begin{tcolorbox}[colback=paper,colframe=blue,boxrule=1.1pt,arc=1mm,left=8mm,right=8mm,top=7mm,bottom=7mm]\large}{\end{tcolorbox}}
\newcommand{\tagline}[1]{\vfill\begin{tcolorbox}[colback=gray!15,boxrule=0pt]\color{ink!76}\bfseries 关键词：#1\end{tcolorbox}}
\begin{document}
\slideheader{1}{张量积}
\begin{promptbox}PROMPTMARK $R\otimes_R M\cong M$.
\begin{enumerate}\item First assertion.\item Second assertion.\end{enumerate}
\[\Phi:R\otimes_R M\longrightarrow M.\]
\end{promptbox}
\tagline{TAGMARK}
\end{document}`

func TestColorBoxMacroAndEnvironmentWrappers(t *testing.T) {
	renderer := NewRenderer()
	result := renderer.RenderHTML(colorBoxTestSource, "")
	body := string(result.Body)
	if result.MathErrors != 0 || strings.Contains(body, "unknown-command") {
		t.Fatalf("box diagnostics: %v; body=%s", result.MathErrorExpressions, body)
	}
	if count := strings.Count(body, `class="tcolorbox"`); count != 3 {
		t.Fatalf("got %d boxes, want header, prompt, and tagline", count)
	}
	for _, expected := range []string{"background-color:#14213d", "background-color:#f6f3ec", "border-width:1.1pt", "padding-left:calc(8mm + 1mm)", `data-tex-size="1.2"`, "PROMPTMARK", "TAGMARK"} {
		if !strings.Contains(body, expected) {
			t.Errorf("missing %q", expected)
		}
	}
	prepared, pages := PreparePages(colorBoxTestSource, 0)
	if len(pages) != 1 || pages[0].Oversized {
		t.Fatalf("unexpected box pagination: %+v", pages)
	}
	fragment := renderer.RenderPreparedHTML(prepared, pages[0].Source, "")
	if string(fragment.Body) != body {
		t.Fatal("prepared rendering changed box styles or destinations")
	}
	root, _ := html.Parse(strings.NewReader(body))
	for _, leaked := range []string{"colback", "boxrule", "promptbox", "slideheader", "#1"} {
		if strings.Contains(htmlText(root), leaked) {
			t.Errorf("box syntax leaked: %q", leaked)
		}
	}
	pdf, err := renderer.RenderPDFWithOptions(Document{Source: colorBoxTestSource}, PDFOptions{Compact: true})
	if err != nil {
		t.Fatal(err)
	}
	content := inflatedPDFStreams(pdf)
	for _, marker := range []string{"PROMPTMARK", "TAGMARK"} {
		if bytes.Count(content, tablePDFText(marker)) != 1 {
			t.Errorf("lost/duplicated selectable text %q", marker)
		}
	}
	// Opaque dark fill must precede its white header text.
	background := bytes.Index(content, []byte("0.078 0.129 0.239 rg"))
	headerToken := append(append([]byte{'('}, tablePDFText("问")...), []byte(") Tj")...)
	if bytes.Count(content, headerToken) != 1 {
		t.Fatal("lost or duplicated selectable CJK header text")
	}
	header := bytes.Index(content, headerToken)
	if background < 0 || background >= header {
		t.Fatal("dark header background was not painted behind its text")
	}
}

func TestColorBoxDefaultsTitlesAndNestedBoxes(t *testing.T) {
	source := `\documentclass{beamer}
\tcbset{colback=blue!5,boxsep=0pt}
\newtcolorbox{exercise}[2][red]{colframe=#1,title={Exercise #2},coltitle=white,fonttitle=\bfseries}
\begin{document}\begin{frame}{Nested}
\begin{exercise}[green]{A, B}Outer text.
\tcolorbox[colback=black,coltext=white,boxrule=0pt]INNERMARK $x$.\endtcolorbox
Outer again.\end{exercise}
\end{frame}\begin{frame}{After}AFTERMARK\end{frame}\end{document}`
	renderer := NewRenderer()
	result := renderer.RenderHTML(source, "")
	body := string(result.Body)
	if result.MathErrors != 0 || strings.Contains(body, "unknown-command") || strings.Count(body, `class="tcolorbox"`) != 2 {
		t.Fatalf("nested boxes: %s", body)
	}
	for _, expected := range []string{"Exercise", "A, B", "border-color:#00ff00", "background-color:#000000", "color:#ffffff"} {
		if !strings.Contains(body, expected) {
			t.Errorf("missing %q", expected)
		}
	}
	resultPDF, err := renderer.RenderPDFResult(Document{Source: source})
	if err != nil {
		t.Fatal(err)
	}
	if len(resultPDF.Sections) != 2 || resultPDF.Sections[0].Page != 1 || resultPDF.Sections[1].Page != 1 {
		t.Fatalf("nested boxes interrupted frame flow: %+v", resultPDF.Sections)
	}
	content := inflatedPDFStreams(resultPDF.PDF)
	for _, marker := range []string{"INNERMARK", "AFTERMARK"} {
		if bytes.Count(content, tablePDFText(marker)) != 1 {
			t.Errorf("nested boxes lost %s", marker)
		}
	}
}
