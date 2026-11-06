package latexpdf

import (
	"bytes"
	"strings"
	"testing"

	"golang.org/x/net/html"
)

func TestXYMatrixRendersNativeDiagramGeometry(t *testing.T) {
	source := `\begin{document}\[
\xymatrix{
A\ar@{=}[rr]&&B\ar@{-->}[d]^{\cong}\\
C\ar[d]^f\ar@{^{(}->}[rr]_{\cong}^{\substack{x\mapsto x^2}}&&D\\
E\ar[r]&F
}\]\end{document}`
	rendered := newLatexRenderer().render(source, "native-xymatrix-test")
	if rendered.MathErrors != 0 {
		t.Fatalf("xymatrix produced %d MathJax errors: %#v", rendered.MathErrors, rendered.MathErrorExpressions)
	}
	body := string(rendered.Body)
	for _, wanted := range []string{`data-diagram="xymatrix"`, `stroke-dasharray=`, `\substack{x\mapsto x^2}`} {
		if !strings.Contains(body, wanted) {
			t.Errorf("native xymatrix output is missing %q: %s", wanted, body)
		}
	}
	if strings.Contains(body, `\begin{array}`) || strings.Contains(body, `diagram-source`) {
		t.Fatalf("xymatrix fell back to the legacy array/source rendering: %s", body)
	}

	root, err := html.Parse(strings.NewReader("<html><body>" + body + "</body></html>"))
	if err != nil {
		t.Fatal(err)
	}
	formula, err := newPDFRenderer().formulaFromWrapper(findMathWrapper(root, "math-display"))
	if err != nil {
		t.Fatal(err)
	}
	foundDashed, foundDoubleRule := false, 0
	for _, path := range formula.paths {
		if len(path.dash) > 0 {
			foundDashed = true
		}
		if path.Stroke != "" && len(path.Segments) == 2 {
			foundDoubleRule++
		}
	}
	if !foundDashed || foundDoubleRule < 2 {
		t.Fatalf("PDF diagram paths lost dashed/equality geometry: dashed=%t stroked-lines=%d", foundDashed, foundDoubleRule)
	}

	pdf, err := NewRenderer().RenderPDF(Document{Source: source, Title: "xymatrix"})
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(pdf, []byte("/Subtype /Image")) {
		t.Fatal("xymatrix PDF used an image fallback")
	}
}

func TestTikZCDSharesNativeDiagramRenderer(t *testing.T) {
	source := `\begin{document}\begin{tikzcd}
{[G/G]}\ar[d,"f",swap]\ar[r,"\simeq"] & X\ar[d,dashed]\\
Y\ar[r,equals] & Z
\end{tikzcd}\end{document}`
	rendered := newLatexRenderer().render(source, "native-tikzcd-test")
	if rendered.MathErrors != 0 {
		t.Fatalf("tikzcd produced %d MathJax errors: %#v", rendered.MathErrors, rendered.MathErrorExpressions)
	}
	body := string(rendered.Body)
	for _, wanted := range []string{`data-diagram="tikzcd"`, `stroke-dasharray=`, `&#34;f&#34;`} {
		if !strings.Contains(body, wanted) {
			t.Errorf("native tikzcd output is missing %q: %s", wanted, body)
		}
	}
}

func TestTikZPictureRendersNodesPathsCurvesAndCJK(t *testing.T) {
	source := `\begin{document}\begin{tikzpicture}[node distance=10mm,>=Stealth]
\node[draw,minimum width=12mm] (a) {$A$};
\node[draw,right=of a,minimum width=12mm] (b) {$B$};
\draw[->,dashed] (a)--node[above]{$f$}(b);
\draw[thick] (0,-1) .. controls (1,-1.3) and (2,-0.7) .. (3,-1);
\fill (1.5,-1) circle (2pt);
\node[below=3pt] at (1.5,-1) {中文标签};
\end{tikzpicture}\end{document}`
	rendered := newLatexRenderer().render(source, "native-tikz-test")
	if rendered.MathErrors != 0 {
		t.Fatalf("TikZ produced %d MathJax errors: %#v", rendered.MathErrors, rendered.MathErrorExpressions)
	}
	body := string(rendered.Body)
	for _, wanted := range []string{`data-diagram="tikz"`, `stroke-dasharray=`, ` C `, `中文标签`} {
		if !strings.Contains(body, wanted) {
			t.Errorf("native TikZ output is missing %q: %s", wanted, body)
		}
	}
	if strings.Contains(body, "TikZ diagram source") {
		t.Fatalf("supported TikZ picture was emitted as source: %s", body)
	}
	pdf, err := NewRenderer().RenderPDF(Document{Source: source, Title: "TikZ"})
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(pdf, []byte("/Subtype /Image")) || !bytes.Contains(bytes.ToLower(pdf), []byte(strings.ToLower(pdfCJKFamily))) {
		t.Fatal("TikZ PDF lost native vector/CJK output")
	}
}

func TestTikZScopesPlotsStylesAndMixedLabels(t *testing.T) {
	source := `\begin{document}\begin{tikzpicture}[
box/.style={draw,rounded corners,minimum width=20mm,minimum height=8mm}]
\node[box] (a) {光滑点};
\node[box,right=of a] (b) {$\dim T=1$};
\draw[->,bend right=20] (a) to node[below]{若维数 $\ge2$，构造\\$\alpha\notin A$} (b);
\begin{scope}[xshift=5cm]
\draw[thick,domain=-1:1,samples=25] plot ({\x*\x},{\x*\x*\x});
\end{scope}
\end{tikzpicture}\end{document}`
	rendered := newLatexRenderer().render(source, "native-tikz-scope-test")
	if rendered.MathErrors != 0 {
		t.Fatalf("scoped TikZ produced %d MathJax errors: %#v", rendered.MathErrors, rendered.MathErrorExpressions)
	}
	body := string(rendered.Body)
	for _, wanted := range []string{`data-diagram="tikz"`, `data-unsupported="0"`, `若`, `data-c="1D6FC"`} {
		if !strings.Contains(body, wanted) {
			t.Errorf("scoped/style TikZ output is missing %q: %s", wanted, body)
		}
	}
	if converted := tikZNodeMath(`若维数 $\ge2$，构造\\$\alpha\notin A$`); strings.Contains(converted, "$") || !strings.Contains(converted, `\alpha`) {
		t.Fatalf("mixed TikZ label converted to %q", converted)
	}
}

func TestXYMatrixSpacingOptions(t *testing.T) {
	body, spacing, ok := standaloneXYMatrix(`{\xymatrix@C=10pt@R=2pt{A\ar[r]&B\\C&D}}`)
	if !ok || spacing != "@C=10pt@R=2pt" || !strings.Contains(body, `A\ar[r]`) {
		t.Fatalf("standalone xymatrix parse = body %q spacing %q ok %t", body, spacing, ok)
	}
	diagram, err := parseXYMatrix(body)
	if err != nil {
		t.Fatal(err)
	}
	diagram.applyXYSpacing(spacing)
	if diagram.columnGap <= diagram.rowGap || diagram.rowGap <= 0 {
		t.Fatalf("xymatrix spacing = column %.1f row %.1f", diagram.columnGap, diagram.rowGap)
	}
}

func TestXYMatrixConsumesLabelsAndPathModifiers(t *testing.T) {
	source := `\begin{document}\begin{equation}\label{modified-xy}
\xymatrix@C=18pt{
A\ar^(.4){f}[rr]\ar@/_2ex/[rrr]&&B&C\\
D\ar@<.5ex>[r]\ar@<-.5ex>[r]&E&&F
}
\end{equation}\end{document}`
	rendered := newLatexRenderer().render(source, "modified-xymatrix")
	body := string(rendered.Body)
	if rendered.MathErrors != 0 {
		t.Fatalf("modified xymatrix produced %d MathJax errors: %#v", rendered.MathErrors, rendered.MathErrorExpressions)
	}
	for _, wanted := range []string{`data-diagram="xymatrix"`, ` C `, `class="numbered-equation"`} {
		if !strings.Contains(body, wanted) {
			t.Errorf("modified xymatrix output is missing %q: %s", wanted, body)
		}
	}
}

func TestTikZCDNestedInEquationUsesNativeRenderer(t *testing.T) {
	source := `\begin{document}\begin{equation}\label{native-cd}
\begin{tikzcd}
A\ar[d,"f",swap]\ar[r,"\simeq"] & B\ar[d]\\
C\ar[r] & D
\end{tikzcd}
\end{equation}\end{document}`
	rendered := newLatexRenderer().render(source, "numbered-native-tikzcd")
	body := string(rendered.Body)
	if rendered.MathErrors != 0 || !strings.Contains(body, `data-diagram="tikzcd"`) || strings.Contains(body, `Unknown environment`) {
		t.Fatalf("nested tikzcd render = errors %d, body %s", rendered.MathErrors, body)
	}
}

func TestTikZGridForeachRangeAndDecoratedArc(t *testing.T) {
	source := `\begin{document}\begin{tikzpicture}[x=8mm,y=8mm]
\draw[step=1] (0,0) grid (4,-4);
\foreach \i in {1,...,4}{
  \node[left] at (0,-\i+0.5) {$\i$};
}
\draw[decoration={text along path,reverse path,text align={align=center},text={Spec{$R_m$}}},decorate] (1.3,0) arc (0:180:1.3);
\end{tikzpicture}\end{document}`
	rendered := newLatexRenderer().render(source, "tikz-grid-range-arc")
	body := string(rendered.Body)
	if rendered.MathErrors != 0 {
		t.Fatalf("TikZ grid/range/arc produced %d MathJax errors: %#v", rendered.MathErrors, rendered.MathErrorExpressions)
	}
	for _, wanted := range []string{`data-diagram="tikz"`, `data-unsupported="0"`, `Spec`, `R_m`} {
		if !strings.Contains(body, wanted) {
			t.Errorf("TikZ grid/range/arc output is missing %q: %s", wanted, body)
		}
	}
	if strings.Contains(body, `unsupported TikZ command`) {
		t.Fatalf("supported TikZ constructs retained an unsupported diagnostic: %s", body)
	}
}
