package latexpdf

import (
	"bytes"
	"compress/zlib"
	"fmt"
	"io"
	"math"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"golang.org/x/net/html"
)

func TestPDFCustomPageSize(t *testing.T) {
	document := Document{
		Source: `\begin{document}Custom page.\end{document}`,
		Title:  "Custom page",
	}
	options := PDFOptions{PageWidth: 120, PageHeight: 90}
	data, err := NewRenderer().RenderPDFWithOptions(document, options)
	if err != nil {
		t.Fatal(err)
	}
	match := regexp.MustCompile(`/MediaBox\s*\[0 0 ([0-9.]+) ([0-9.]+)\]`).FindSubmatch(data)
	if len(match) != 3 {
		t.Fatalf("custom PDF has no readable MediaBox: %q", data[:min(len(data), 200)])
	}
	width, _ := strconv.ParseFloat(string(match[1]), 64)
	height, _ := strconv.ParseFloat(string(match[2]), 64)
	if math.Abs(width-120*72/25.4) > 0.02 || math.Abs(height-90*72/25.4) > 0.02 {
		t.Fatalf("MediaBox = %.2fx%.2fpt, want 120x90mm", width, height)
	}

	defaultWidth, defaultHeight, err := pdfPageSize(PDFOptions{})
	if err != nil || defaultWidth != defaultPDFPageWidth || defaultHeight != defaultPDFPageHeight {
		t.Fatalf("zero-value page size = %.2fx%.2f, %v", defaultWidth, defaultHeight, err)
	}
	if PDFCacheKeyWithOptions(document, options) == PDFCacheKey(document) {
		t.Fatal("custom page dimensions are missing from the PDF cache key")
	}
	if _, _, err := pdfPageSize(PDFOptions{PageWidth: -1}); err == nil {
		t.Fatal("negative page width was accepted")
	}
}

func TestPDFFontsLoadLazilyAndRemainEmbedded(t *testing.T) {
	tests := []struct {
		name     string
		body     string
		wantMono bool
		wantCJK  bool
	}{
		{name: "serif only", body: "Plain serif text."},
		{name: "monospace", body: `\texttt{monospace text}`, wantMono: true},
		{name: "CJK", body: "漢字。", wantCJK: true},
		{name: "monospace CJK uses fallback only", body: `\texttt{漢字。}`, wantCJK: true},
	}
	fontFilePattern := regexp.MustCompile(`/FontFile2\s+\d+\s+0\s+R`)
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			data, err := NewRenderer().RenderPDF(Document{
				Source: `\documentclass{article}\begin{document}` + test.body + `\end{document}`,
				Title:  "Embedded fonts",
			})
			if err != nil {
				t.Fatal(err)
			}
			lower := bytes.ToLower(data)
			if got := bytes.Contains(lower, []byte(strings.ToLower(pdfMonoFamily))); got != test.wantMono {
				t.Errorf("mono font present = %t, want %t", got, test.wantMono)
			}
			if got := bytes.Contains(lower, []byte(strings.ToLower(pdfCJKFamily))); got != test.wantCJK {
				t.Errorf("CJK font present = %t, want %t", got, test.wantCJK)
			}
			wantEmbedded := 4
			if test.wantMono {
				wantEmbedded++
			}
			if test.wantCJK {
				wantEmbedded++
			}
			if got := len(fontFilePattern.FindAll(data, -1)); got != wantEmbedded {
				t.Errorf("embedded TrueType font programs = %d, want %d", got, wantEmbedded)
			}
		})
	}
}

func TestSVGPathSmoothQuadraticBecomesVectorSegments(t *testing.T) {
	segments, err := parseSVGPath("M0 0Q10 10 20 0T40 0Z", identityAffine())
	if err != nil {
		t.Fatal(err)
	}
	if len(segments) != 4 || segments[0].Cmd != 'M' || segments[1].Cmd != 'Q' || segments[2].Cmd != 'Q' || segments[3].Cmd != 'Z' {
		t.Fatalf("segments = %#v", segments)
	}
	want := [6]float64{30, -10, 40, 0}
	if segments[2].Arg != want {
		t.Fatalf("reflected quadratic segment = %v, want %v", segments[2].Arg, want)
	}
}

func TestPDFInlineMathKeepsTextAndFormulaSpacing(t *testing.T) {
	pieces := splitPDFText("text before ")
	if len(pieces) == 0 || pieces[len(pieces)-1] != " " {
		t.Fatalf("trailing source space was lost at an inline boundary: %#v", pieces)
	}
	style := pdfTextStyle{size: 12}
	formula := &pdfFormula{viewWidth: 442, viewHeight: 442, widthEx: 1}
	item := (&pdfDocumentLayout{}).measureInline(pdfInline{formula: formula, style: style})
	fontMM := style.size * 25.4 / 72
	rawFormulaWidth := fontMM * 0.5
	want := rawFormulaWidth + fontMM*0.16
	if math.Abs(item.width-want) > 0.0001 {
		t.Fatalf("inline formula width = %g, want %g including side bearings", item.width, want)
	}
	pieces = splitPDFText("text before\nformula after")
	wantPieces := []string{"text", " ", "before", " ", "formula", " ", "after"}
	if fmt.Sprint(pieces) != fmt.Sprint(wantPieces) {
		t.Fatalf("HTML-collapsible source newline forced a PDF break: %#v", pieces)
	}
	pieces = splitPDFText("Definition\u00a01.1")
	if len(pieces) != 1 || pieces[0] != "Definition\u00a01.1" {
		t.Fatalf("nonbreaking reference text became separate line items: %#v", pieces)
	}
	if !isCJKRune('，') {
		t.Fatal("full-width CJK punctuation did not select the embedded CJK font")
	}
	oversized := item
	oversized.formulaWidth = 200
	oversized.width = 201
	fitted := fitInlineFormula(oversized, 80)
	if fitted.width > 80 || fitted.formulaWidth >= oversized.formulaWidth {
		t.Fatalf("oversized inline formula was not fitted atomically: before=%+v after=%+v", oversized, fitted)
	}
}

func TestMathJaxSVGParsesAsFilledPDFPaths(t *testing.T) {
	rendered := newLatexRenderer().render(
		`\documentclass{article}\begin{document}\[\frac{1}{2}\longrightarrow\C[x]\]\end{document}`,
		"pdf-vector-formula-test",
	)
	root, err := html.Parse(strings.NewReader("<html><body>" + string(rendered.Body) + "</body></html>"))
	if err != nil {
		t.Fatal(err)
	}
	var wrapper *html.Node
	var findWrapper func(*html.Node)
	findWrapper = func(node *html.Node) {
		if wrapper != nil {
			return
		}
		if htmlClasses(node)["math-display"] {
			wrapper = node
			return
		}
		for child := node.FirstChild; child != nil; child = child.NextSibling {
			findWrapper(child)
		}
	}
	findWrapper(root)
	if wrapper == nil {
		t.Fatalf("rendered formula has no MathJax wrapper: %s", rendered.Body)
	}
	formula, err := newPDFRenderer().formulaFromWrapper(wrapper)
	if err != nil {
		t.Fatal(err)
	}
	if formula.viewWidth <= 0 || formula.viewHeight <= 0 || len(formula.paths) < 3 {
		t.Fatalf("formula did not produce usable PDF paths: %+v", formula)
	}
	segments := 0
	for _, path := range formula.paths {
		segments += len(path.Segments)
	}
	if segments < 20 {
		t.Fatalf("formula contains only %d vector segments", segments)
	}
}

func TestMathJaxColorsRemainFixedVectorPaints(t *testing.T) {
	source := `\documentclass{article}
\definecolor{brand}{HTML}{12AB34}
\begin{document}{\color{brand}Brand}\[\color{brand}x+\textcolor{red}{\boxed{y}}+\textcolor{blue}{\text{初}}\]\end{document}`
	rendered := newLatexRenderer().render(source, "pdf-colored-formula-test")
	if rendered.MathErrors != 0 {
		t.Fatalf("colored formula produced %d MathJax errors: %#v", rendered.MathErrors, rendered.MathErrorExpressions)
	}
	body := string(rendered.Body)
	for _, wanted := range []string{`fill="#12ab34"`, `fill="#ff0000"`, `fill="#0000ff"`} {
		if !strings.Contains(body, wanted) {
			t.Errorf("colored MathJax SVG is missing %q: %s", wanted, body)
		}
	}
	root, err := html.Parse(strings.NewReader("<html><body>" + body + "</body></html>"))
	if err != nil {
		t.Fatal(err)
	}
	formula, err := newPDFRenderer().formulaFromWrapper(findMathWrapper(root, "math-display"))
	if err != nil {
		t.Fatal(err)
	}
	foundBrand, foundRed, foundRedStroke, foundBlueText := false, false, false, false
	for _, path := range formula.paths {
		if path.strokePaint.enabled && !path.strokePaint.current && path.strokePaint.color == (renderColor{255, 0, 0}) {
			foundRedStroke = true
		}
		if path.fillPaint.enabled && !path.fillPaint.current {
			switch path.fillPaint.color {
			case (renderColor{18, 171, 52}):
				foundBrand = true
			case (renderColor{255, 0, 0}):
				foundRed = true
			}
		}
	}
	for _, text := range formula.texts {
		if text.text == "初" && text.paint.enabled && !text.paint.current && text.paint.color == (renderColor{0, 0, 255}) {
			foundBlueText = true
		}
	}
	if !foundBrand || !foundRed || !foundRedStroke || !foundBlueText {
		t.Fatalf("fixed PDF formula paints missing: brand=%t red=%t red stroke=%t blue text=%t formula=%+v", foundBrand, foundRed, foundRedStroke, foundBlueText, formula)
	}
	pdf, err := NewRenderer().RenderPDF(Document{Source: source, Title: "Colored vectors"})
	if err != nil {
		t.Fatal(err)
	}
	content := inflatedPDFStreams(pdf)
	for _, operator := range [][]byte{
		[]byte("0.071 0.671 0.204 rg"),
		[]byte("1.000 0.000 0.000 rg"),
		[]byte("1.000 0.000 0.000 RG"),
		[]byte("0.000 0.000 1.000 rg"),
	} {
		if !bytes.Contains(content, operator) {
			t.Errorf("native PDF content is missing color operator %q", operator)
		}
	}
	if bytes.Contains(pdf, []byte("/Subtype /Image")) {
		t.Fatal("colored formula unexpectedly used an image fallback")
	}
}

func TestUnknownMathCommandUsesRedVectorLiteral(t *testing.T) {
	source := `\begin{document}\[A\xrightarrow{\Missing}B\]\end{document}`
	rendered := newLatexRenderer().render(source, "pdf-red-unknown-command")
	body := string(rendered.Body)
	if rendered.MathErrors != 1 || strings.Contains(body, `data-mml-node="merror"`) || !strings.Contains(body, `data-unknown-commands="\Missing"`) {
		t.Fatalf("unknown command fallback = errors %d, body %s", rendered.MathErrors, body)
	}
	root, err := html.Parse(strings.NewReader("<html><body>" + body + "</body></html>"))
	if err != nil {
		t.Fatal(err)
	}
	formula, err := newPDFRenderer().formulaFromWrapper(findMathWrapper(root, "math-display"))
	if err != nil {
		t.Fatal(err)
	}
	foundRed := false
	for _, path := range formula.paths {
		if path.fillPaint.enabled && !path.fillPaint.current && path.fillPaint.color == (renderColor{255, 0, 0}) {
			foundRed = true
			break
		}
	}
	if !foundRed {
		t.Fatalf("unknown command literal has no fixed red vector paint: %+v", formula)
	}
	pdf, err := NewRenderer().RenderPDF(Document{Source: source, Title: "Unknown command"})
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(inflatedPDFStreams(pdf), []byte("1.000 0.000 0.000 rg")) {
		t.Fatal("unknown command PDF is missing the red fill operator")
	}
	if bytes.Contains(pdf, []byte("/Subtype /Image")) {
		t.Fatal("unknown command PDF used an image fallback")
	}
}

func TestStrikeAndUnderlineDecorationsRemainNative(t *testing.T) {
	source := `\documentclass{article}\begin{document}
\strikethrough{removed $x$} plain
\sout{\textcolor{red}{gone}} \strikeout{old} \st{short}
\STRIKETHROUGH{upper} \uline{under}.
\[\textcolor{blue}{\strikethrough{x+1}}\]
\end{document}`
	rendered := newLatexRenderer().render(source, "native-text-decorations")
	if rendered.MathErrors != 0 {
		t.Fatalf("decorations produced %d MathJax errors: %#v", rendered.MathErrors, rendered.MathErrorExpressions)
	}
	body := string(rendered.Body)
	if got := strings.Count(body, "<del>"); got != 5 {
		t.Fatalf("prose strike elements = %d, want 5: %s", got, body)
	}
	if !strings.Contains(body, "<u>under</u>") || !strings.Contains(body, `data-mml-node="menclose"`) || strings.Contains(body, `data-mml-node="merror"`) {
		t.Fatalf("native decoration HTML is incomplete: %s", body)
	}

	root, err := html.Parse(strings.NewReader("<html><body>" + body + "</body></html>"))
	if err != nil {
		t.Fatal(err)
	}
	paragraph := findHTMLNode(root, "p")
	if paragraph == nil {
		t.Fatalf("decorated output has no paragraph: %s", body)
	}
	layout := &pdfDocumentLayout{renderer: newPDFRenderer()}
	runs := layout.collectInlineChildren(paragraph, bodyPDFStyle())
	foundRemoved, foundFormula, foundPlain, foundRed, foundUnderline := false, false, false, false, false
	for _, run := range runs {
		switch {
		case strings.Contains(run.text, "removed"):
			foundRemoved = run.style.strike
		case run.formula != nil:
			foundFormula = run.style.strike
		case strings.Contains(run.text, "plain"):
			foundPlain = !run.style.strike
		case strings.Contains(run.text, "gone"):
			foundRed = run.style.strike && run.style.red == 255 && run.style.green == 0 && run.style.blue == 0
		case strings.Contains(run.text, "under"):
			foundUnderline = run.style.underline && !run.style.strike
		}
	}
	if !foundRemoved || !foundFormula || !foundPlain || !foundRed || !foundUnderline {
		t.Fatalf("PDF decoration styles were not scoped correctly: %+v", runs)
	}

	formula, err := newPDFRenderer().formulaFromWrapper(findMathWrapper(root, "math-display"))
	if err != nil {
		t.Fatal(err)
	}
	foundBlueStrike := false
	for _, path := range formula.paths {
		if len(path.Segments) == 2 && path.strokeWidth > 0 && path.strokePaint.enabled &&
			!path.strokePaint.current && path.strokePaint.color == (renderColor{0, 0, 255}) {
			foundBlueStrike = true
		}
	}
	if !foundBlueStrike {
		t.Fatalf("math strike did not become a blue vector rule: %+v", formula.paths)
	}

	pdf, err := NewRenderer().RenderPDF(Document{Source: source, Title: "Native decorations"})
	if err != nil {
		t.Fatal(err)
	}
	content := inflatedPDFStreams(pdf)
	for _, operator := range [][]byte{
		[]byte("1.000 0.000 0.000 RG"),
		[]byte("0.000 0.000 1.000 RG"),
	} {
		if !bytes.Contains(content, operator) {
			t.Errorf("native PDF content is missing decoration color %q", operator)
		}
	}
	if bytes.Contains(pdf, []byte("/Subtype /Image")) {
		t.Fatal("text decorations unexpectedly used an image fallback")
	}
}

func TestDefinedMathSTMacroIsNotTreatedAsStrike(t *testing.T) {
	rendered := newLatexRenderer().render(
		`\newcommand{\st}{\mathrm{st}}\begin{document}$\st(V)$\end{document}`,
		"defined-st-math-macro",
	)
	body := string(rendered.Body)
	if rendered.MathErrors != 0 || strings.Contains(body, `data-mml-node="menclose"`) {
		t.Fatalf("defined \\st macro was mistaken for Soul strike: errors=%d body=%s", rendered.MathErrors, body)
	}
}

func inflatedPDFStreams(pdf []byte) []byte {
	streamPattern := regexp.MustCompile(`(?s)stream\r?\n(.*?)\r?\nendstream`)
	var output bytes.Buffer
	for _, match := range streamPattern.FindAllSubmatch(pdf, -1) {
		reader, err := zlib.NewReader(bytes.NewReader(match[1]))
		if err != nil {
			continue
		}
		_, _ = io.Copy(&output, reader)
		_ = reader.Close()
	}
	return output.Bytes()
}

func TestMathJaxCJKTextIsPositionedInVectorPDF(t *testing.T) {
	rendered := newLatexRenderer().render(
		`\documentclass{article}\begin{document}\[\begin{array}{c|c}\text{初对象}&\text{终对象}\\M\text{ 平坦}&M^+\text{ 内射}\end{array}\]\end{document}`,
		"pdf-cjk-formula-test",
	)
	if rendered.MathErrors != 0 {
		t.Fatalf("CJK formula produced %d MathJax errors: %#v", rendered.MathErrors, rendered.MathErrorExpressions)
	}
	root, err := html.Parse(strings.NewReader("<html><body>" + string(rendered.Body) + "</body></html>"))
	if err != nil {
		t.Fatal(err)
	}
	wrapper := findMathWrapper(root, "math-display")
	if wrapper == nil {
		t.Fatalf("rendered CJK formula has no MathJax wrapper: %s", rendered.Body)
	}
	formula, err := newPDFRenderer().formulaFromWrapper(wrapper)
	if err != nil {
		t.Fatal(err)
	}
	var text strings.Builder
	for _, run := range formula.texts {
		text.WriteString(run.text)
		if run.fontSize <= 0 || run.x < formula.viewX || run.y < formula.viewY {
			t.Fatalf("invalid positioned formula text: %+v in %+v", run, formula)
		}
	}
	for _, wanted := range []string{"初对象", "终对象", "平坦", "内射"} {
		if !strings.Contains(text.String(), wanted) {
			t.Errorf("formula text %q is missing %q", text.String(), wanted)
		}
	}
	if len(formula.paths) == 0 {
		t.Fatal("mixed CJK formula lost its mathematical vector paths")
	}
	foundTableRule := false
	for _, path := range formula.paths {
		if path.Stroke != "" && path.strokeWidth > 0 && len(path.Segments) == 2 && path.Segments[0].Cmd == 'M' && path.Segments[1].Cmd == 'L' {
			foundTableRule = true
		}
	}
	if !foundTableRule {
		t.Fatal("CJK array lost its stroked column rule")
	}

	pureRendered := newLatexRenderer().render(
		`\begin{document}\[\text{初对象}\]\end{document}`,
		"pdf-pure-cjk-formula-test",
	)
	pureRoot, err := html.Parse(strings.NewReader("<html><body>" + string(pureRendered.Body) + "</body></html>"))
	if err != nil {
		t.Fatal(err)
	}
	pureFormula, err := newPDFRenderer().formulaFromWrapper(findMathWrapper(pureRoot, "math-display"))
	if err != nil {
		t.Fatal(err)
	}
	if len(pureFormula.paths) != 0 || len(pureFormula.texts) != 3 {
		t.Fatalf("pure CJK formula = %d paths, %d text runs; want 0 paths and 3 runs", len(pureFormula.paths), len(pureFormula.texts))
	}

	pdf, err := NewRenderer().RenderPDF(Document{Source: `\begin{document}\[\text{初对象}\]\end{document}`})
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(bytes.ToLower(pdf), []byte(strings.ToLower(pdfCJKFamily))) || bytes.Contains(pdf, []byte("/Subtype /Image")) {
		t.Fatalf("CJK math PDF did not embed vector/selectable CJK output (%d bytes)", len(pdf))
	}
}

func findMathWrapper(root *html.Node, className string) *html.Node {
	if root == nil {
		return nil
	}
	if htmlClasses(root)[className] {
		return root
	}
	for child := root.FirstChild; child != nil; child = child.NextSibling {
		if found := findMathWrapper(child, className); found != nil {
			return found
		}
	}
	return nil
}

func TestBoxedMathJaxRectangleRemainsAnOutline(t *testing.T) {
	rendered := newLatexRenderer().render(
		`\documentclass{article}\begin{document}\[\boxed{x=1}\]\end{document}`,
		"pdf-boxed-formula-test",
	)
	root, err := html.Parse(strings.NewReader("<html><body>" + string(rendered.Body) + "</body></html>"))
	if err != nil {
		t.Fatal(err)
	}
	var wrapper *html.Node
	var findWrapper func(*html.Node)
	findWrapper = func(node *html.Node) {
		if wrapper != nil {
			return
		}
		if htmlClasses(node)["math-display"] {
			wrapper = node
			return
		}
		for child := node.FirstChild; child != nil; child = child.NextSibling {
			findWrapper(child)
		}
	}
	findWrapper(root)
	if wrapper == nil {
		t.Fatalf("boxed formula has no MathJax wrapper: %s", rendered.Body)
	}
	formula, err := newPDFRenderer().formulaFromWrapper(wrapper)
	if err != nil {
		t.Fatal(err)
	}
	foundOutline := false
	for _, path := range formula.paths {
		if len(path.Segments) == 5 && path.Segments[len(path.Segments)-1].Cmd == 'Z' && path.Stroke != "" {
			foundOutline = true
			if path.Fill != "" || path.strokeWidth <= 0 {
				t.Fatalf("boxed rectangle fill=%q stroke=%q width=%g", path.Fill, path.Stroke, path.strokeWidth)
			}
		}
	}
	if !foundOutline {
		t.Fatalf("boxed formula has no stroked rectangle: %+v", formula.paths)
	}
}
