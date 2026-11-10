package latexpdf

import (
	"strings"
	"testing"
)

func TestFlowchartRendersSVGHTMLAndMathJaxLabels(t *testing.T) {
	source := `
\newcommand{\WildGamma}{\Gamma_L}
\begin{document}
\begin{flowchart}[direction=down]
group generic "Characteristic-zero argument" {
  node V "Representation crystalline over $L$" [accent]
  node R "Robba $\varphi$-module\\with literal $\WildGamma$-descent"
  V -> R
}
node K "Kummer restriction" [success]
R -.-> K "wild-log extension"
\end{flowchart}
\end{document}`
	result := newLatexRenderer().render(source, "flowchart-math-test")
	if result.MathErrors != 0 {
		t.Fatalf("flowchart labels produced %d MathJax errors: %s", result.MathErrors, result.Body)
	}
	html := string(result.Body)
	for _, fragment := range []string{
		`class="flowchart"`, `<svg`, `class="flowchart-group-box"`,
		`class="flowchart-node accent"`, `class="flowchart-node success"`,
		`class="flowchart-edge dashed"`, `class="math-inline"`,
	} {
		if !strings.Contains(html, fragment) {
			t.Errorf("rendered flowchart is missing %q: %s", fragment, html)
		}
	}
}

func TestFlowchartLabelsRemainEscaped(t *testing.T) {
	source := `\begin{document}\begin{flowchart}
node safe "<script>alert(1)</script>"
\end{flowchart}\end{document}`
	result := newLatexRenderer().render(source, "flowchart-escape-test")
	if strings.Contains(string(result.Body), "<script>") {
		t.Fatalf("flowchart emitted an unescaped label: %s", result.Body)
	}
	if !strings.Contains(string(result.Body), "&lt;script&gt;") {
		t.Fatalf("escaped flowchart label is missing: %s", result.Body)
	}
}

func TestFlowchartParseErrorsAreVisibleAndEscaped(t *testing.T) {
	source := `\begin{document}\begin{flowchart}
node broken "<b>unterminated
\end{flowchart}\end{document}`
	result := newLatexRenderer().render(source, "flowchart-error-test")
	html := string(result.Body)
	if !strings.Contains(html, "Flowchart error:") || !strings.Contains(html, "unterminated quoted label") {
		t.Fatalf("flowchart parse error was not shown: %s", html)
	}
	if strings.Contains(html, "<b>") {
		t.Fatalf("flowchart error source was not escaped: %s", html)
	}
}

func TestFlowchartDirectionalLayout(t *testing.T) {
	for _, test := range []struct {
		direction string
		ordered   func(*flowchartNode, *flowchartNode) bool
	}{
		{direction: "right", ordered: func(a, b *flowchartNode) bool { return a.X < b.X }},
		{direction: "left", ordered: func(a, b *flowchartNode) bool { return a.X > b.X }},
		{direction: "down", ordered: func(a, b *flowchartNode) bool { return a.Y < b.Y }},
		{direction: "up", ordered: func(a, b *flowchartNode) bool { return a.Y > b.Y }},
	} {
		chart, err := parseFlowchart("node A \"A\"\nnode B \"B\"\nA -> B", "direction="+test.direction)
		if err != nil {
			t.Fatalf("%s chart parse failed: %v", test.direction, err)
		}
		layOutFlowchart(chart)
		if !test.ordered(chart.nodeByID["A"], chart.nodeByID["B"]) {
			t.Errorf("%s layout put A at (%d,%d) and B at (%d,%d)", test.direction,
				chart.nodeByID["A"].X, chart.nodeByID["A"].Y,
				chart.nodeByID["B"].X, chart.nodeByID["B"].Y)
		}
	}
}
