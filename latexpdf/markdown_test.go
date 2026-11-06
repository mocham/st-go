package latexpdf

import (
	"strings"
	"testing"
)

func TestMarkdownUsesLatexMathPipeline(t *testing.T) {
	markdown := `---
title: ignored front matter
---
# Vector notes

\newcommand{\RR}{\mathbb{R}}

A **bold** map $f:\RR\to\RR$ and [reference](https://example.test).

$$
\int_0^1 x^2\,dx=\frac13
$$

- first item
- second item

` + "```go\nfmt.Println(`x`)\n```"
	source := MarkdownToLaTeX(markdown)
	for _, wanted := range []string{`\section*{Vector notes}`, `\textbf{bold}`, `\newcommand{\RR}`, `\begin{itemize}`, `\begin{verbatim}`} {
		if !strings.Contains(source, wanted) {
			t.Errorf("converted Markdown is missing %q:\n%s", wanted, source)
		}
	}
	result := newLatexRenderer().render(source, "markdown-vector-math")
	body := string(result.Body)
	if result.MathErrors != 0 {
		t.Fatalf("Markdown produced %d math errors: %#v", result.MathErrors, result.MathErrorExpressions)
	}
	if strings.Count(body, `class="math-inline"`) != 1 || strings.Count(body, `class="math-display"`) != 1 {
		t.Fatalf("Markdown math did not use inline and display SVG wrappers: %s", body)
	}
	for _, wanted := range []string{"<strong>bold</strong>", "<ul>", "<pre", "https://example.test"} {
		if !strings.Contains(body, wanted) {
			t.Errorf("rendered Markdown is missing %q: %s", wanted, body)
		}
	}
	if len(result.Sections) != 1 || result.Sections[0].Title != "Vector notes" {
		t.Fatalf("Markdown sections = %#v", result.Sections)
	}
}

func TestMarkdownEscapesTextWithoutEscapingMath(t *testing.T) {
	source := MarkdownToLaTeX("# 100% notes\n\nPrice \\$5, code `a_b`, and \\(x_1\\).")
	if !strings.Contains(source, `\section*{100\% notes}`) || !strings.Contains(source, `\$5`) || !strings.Contains(source, `\texttt{a\_b}`) || !strings.Contains(source, `\(x_1\)`) {
		t.Fatalf("Markdown escaping is incorrect: %s", source)
	}
	result := newLatexRenderer().render(source, "markdown-escaping")
	if result.MathErrors != 0 || strings.Count(string(result.Body), `class="math-inline"`) != 1 {
		t.Fatalf("escaped Markdown render = errors %d, body %s", result.MathErrors, result.Body)
	}
	if !strings.Contains(string(result.Body), `<code>a_b</code>`) {
		t.Fatalf("Markdown code span retained TeX escaping: %s", result.Body)
	}
}
