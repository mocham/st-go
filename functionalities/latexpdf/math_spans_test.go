package latexpdf

import (
	"bytes"
	"strings"
	"testing"

	"golang.org/x/net/html"
)

func checkNestedMathHTML(t *testing.T, result Result, expressions []string, display bool) {
	t.Helper()
	if result.MathErrors != 0 || strings.Contains(string(result.Body), "unknown-command") {
		t.Fatalf("nested text/math diagnostics: %v\n%s", result.MathErrorExpressions, result.Body)
	}
	root, err := html.Parse(strings.NewReader(string(result.Body)))
	if err != nil {
		t.Fatal(err)
	}
	var formulas []*html.Node
	var visit func(*html.Node)
	visit = func(node *html.Node) {
		classes := htmlClasses(node)
		if classes["math-inline"] || classes["math-display"] {
			formulas = append(formulas, node)
			return
		}
		for child := node.FirstChild; child != nil; child = child.NextSibling {
			visit(child)
		}
	}
	visit(root)
	if len(formulas) != len(expressions) {
		t.Fatalf("got %d formulas, want %d: %s", len(formulas), len(expressions), result.Body)
	}
	for index, formula := range formulas {
		if got := htmlAttribute(formula, "data-tex"); got != expressions[index] {
			t.Errorf("formula %d was truncated or merged: got %q, want %q", index, got, expressions[index])
		}
		if htmlClasses(formula)["math-display"] != display || findHTMLNode(formula, "svg") == nil {
			t.Errorf("formula %d lost its math mode or SVG", index)
		}
	}
}

func TestNestedTextMathInlineDelimiters(t *testing.T) {
	renderer := NewRenderer()
	expressions := []string{
		`\text{when \(y>0\)}`,
		`\text{outer $x+\text{inner \(y\)}$ end}`,
		`\text{when \begin{math}y>0\end{math}}`,
		`[0,1]\quad\{x\}\quad\text{cost \$5}`,
		"\\text{when \\(y>0\\)}\n+\\begin{pmatrix}\n1&0\\\\0&1\n\\end{pmatrix}",
	}
	for _, expression := range expressions {
		t.Run(expression, func(t *testing.T) {
			var baseline string
			for _, delimiters := range [][2]string{{"$", "$"}, {`\(`, `\)`}, {`\begin{math}`, `\end{math}`}} {
				source := "Before " + delimiters[0] + expression + delimiters[1] + " after."
				result := renderer.RenderHTML(source, "")
				checkNestedMathHTML(t, result, []string{expression}, false)
				body := string(result.Body)
				if strings.Count(body, "<p>") != 1 || !strings.HasPrefix(body, "<p>Before ") || !strings.HasSuffix(body, " after.</p>") {
					t.Fatalf("inline math split the surrounding paragraph: %s", body)
				}
				if baseline == "" {
					baseline = body
				} else if body != baseline {
					t.Errorf("%s...%s does not match dollar-delimited rendering", delimiters[0], delimiters[1])
				}
			}
		})
	}
}

func TestStandaloneTextWithNestedMath(t *testing.T) {
	renderer := NewRenderer()
	for _, command := range []string{"text", "mbox"} {
		source := `START \` + command + `{OUTERMARK \text{INNERMARK \(x\)} \begin{math}y+\text{when \(z>0\)}\end{math}} END`
		result := renderer.RenderHTML(source, "")
		checkNestedMathHTML(t, result, []string{"x", `y+\text{when \(z>0\)}`}, false)
		if strings.Count(string(result.Body), "<p>") != 1 || !strings.Contains(string(result.Body), "START OUTERMARK INNERMARK ") || !strings.HasSuffix(string(result.Body), " END</p>") {
			t.Fatalf("standalone \\%s lost prose or paragraph structure: %s", command, result.Body)
		}
		pdf, err := renderer.RenderPDFResultWithOptions(Document{Source: source}, PDFOptions{Compact: true})
		if err != nil || pdf.MathErrors != 0 {
			t.Fatalf("nested text/math PDF: %v, %v", err, pdf.MathErrorExpressions)
		}
		content := inflatedPDFStreams(pdf.PDF)
		for _, marker := range []string{"START", "OUTERMARK", "INNERMARK", "END"} {
			if bytes.Count(content, tablePDFText(marker)) != 1 {
				t.Errorf("PDF lost or duplicated selectable text %q", marker)
			}
		}
		if !bytes.Contains(content, []byte(" m\n")) {
			t.Fatal("nested math produced no PDF vector paths")
		}
	}
}

func TestNestedMathPreparedPagination(t *testing.T) {
	renderer := NewRenderer()
	expression := "\\text{when \\(y>0\\)}\n+\\begin{pmatrix}\n1&0\\\\0&1\n\\end{pmatrix}"
	for _, delimiters := range [][2]string{{`\(`, `\)`}, {`\begin{math}`, `\end{math}`}, {`\[`, `\]`}, {"$$", "$$"}} {
		display := delimiters[0] == `\[` || delimiters[0] == "$$"
		fragment := delimiters[0] + expression + delimiters[1]
		if !display {
			fragment += " same paragraph."
		}
		source := fragment + "\n\n" + `\section*{Following}Tail.`
		prepared, pages := PreparePages(source, len(fragment)+2)
		if len(pages) < 2 {
			t.Fatal("test did not exercise HTML pagination")
		}
		var combined strings.Builder
		for _, page := range pages {
			if page.Oversized {
				t.Fatal("valid nested formula was split into oversized source fragments")
			}
			combined.WriteString(string(renderer.RenderPreparedHTML(prepared, page.Source, "").Body))
		}
		result := renderer.RenderHTML(source, "")
		checkNestedMathHTML(t, result, []string{expression}, display)
		if combined.String() != string(result.Body) {
			t.Fatalf("pagination changed nested math or following section: %s", combined.String())
		}
	}
}

func TestMathEnvironmentAtParagraphStartAndIncompleteMath(t *testing.T) {
	renderer := NewRenderer()
	result := renderer.RenderHTML(`\begin{math}[0,1]\end{math} continues. \(z\)`, "")
	checkNestedMathHTML(t, result, []string{"[0,1]", "z"}, false)
	if strings.Count(string(result.Body), "<p>") != 1 {
		t.Fatal("a leading math environment became a separate block")
	}
	for _, source := range []string{`\(\text{inner \(x\)} TailAfter`, `\begin{math}\text{inner $x$} TailAfter`, `\text{unfinished TailAfter`} {
		result := renderer.RenderHTML(source, "")
		if !strings.Contains(string(result.Body), "TailAfter") {
			t.Fatalf("incomplete editing input lost its trailing content: %s", result.Body)
		}
	}
}
