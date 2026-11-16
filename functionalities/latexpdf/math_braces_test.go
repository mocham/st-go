package latexpdf

import (
	"bytes"
	"strings"
	"testing"

	"golang.org/x/net/html"
)

func TestBraceLabelsAndNesting(t *testing.T) {
	renderer := NewRenderer()
	for _, test := range []struct {
		source      string
		under, over int
		labelGlyphs []string
	}{
		{`\underbrace{\underbrace{a+b}_{m}+c}_{n}`, 4, 0, []string{"1D45A", "1D45B"}},
		{`\overbrace{a+\underbrace{b+c}_{m}}^{n}`, 2, 2, []string{"1D45A", "1D45B"}},
		{`\underbrace{a+b}_{\underbrace{x+y}_{n}}`, 4, 0, []string{"1D465", "1D466", "1D45B"}},
		{`\underbrace x_n`, 2, 0, []string{"1D465", "1D45B"}},
		{`\overbrace\alpha^\beta`, 0, 2, []string{"1D6FC", "1D6FD"}},
		{`\underbrace{a+b}_\text{label}`, 2, 0, []string{"6C", "61", "62", "65"}},
		{`\underbrace{a+b}_\frac{1}{2}`, 2, 0, []string{"31", "32"}},
		{`\underbrace{a+b}_\sqrt[3]{n}`, 2, 0, []string{"33", "1D45B"}},
		{`\underbrace{a+b}`, 1, 0, nil},
	} {
		t.Run(test.source, func(t *testing.T) {
			for _, delimiters := range [][2]string{{"$", "$"}, {`\[`, `\]`}} {
				result := renderer.RenderHTML(delimiters[0]+test.source+delimiters[1], "")
				body := string(result.Body)
				if result.MathErrors != 0 || strings.Contains(body, "unknown-command") {
					t.Fatalf("brace diagnostics: %v\n%s", result.MathErrorExpressions, body)
				}
				if strings.Count(body, `data-mml-node="munder"`) != test.under || strings.Count(body, `data-mml-node="mover"`) != test.over {
					t.Fatalf("lost a brace or moved a label to a side script: %s", body)
				}
				for _, glyph := range test.labelGlyphs {
					if !strings.Contains(body, `data-c="`+glyph+`"`) {
						t.Errorf("missing label glyph %s", glyph)
					}
				}
			}
		})
	}
}

func TestBraceScriptOrderAndArgumentForms(t *testing.T) {
	renderer := NewRenderer()
	for _, pair := range [][2]string{
		{`\underbrace{x}^{a}_{b}`, `\underbrace{x}_{b}^{a}`},
		{`\overbrace{x}_{b}^{a}`, `\overbrace{x}^{a}_{b}`},
		{`\underbrace x_n`, `\underbrace{x}_{n}`},
		{`\underbrace{x}_\text{label}`, `\underbrace{x}_{\text{label}}`},
		{`\underbrace{x}_\frac{1}{2}`, `\underbrace{x}_{\frac{1}{2}}`},
	} {
		var first string
		for _, source := range pair {
			result := renderer.RenderHTML(`\[`+source+`\]`, "")
			if result.MathErrors != 0 {
				t.Fatalf("%s: %v", source, result.MathErrorExpressions)
			}
			root, err := html.Parse(strings.NewReader(string(result.Body)))
			if err != nil {
				t.Fatal(err)
			}
			var svg bytes.Buffer
			if err := html.Render(&svg, findHTMLNode(root, "svg")); err != nil {
				t.Fatal(err)
			}
			if first == "" {
				first = svg.String()
			} else if first != svg.String() {
				t.Errorf("equivalent brace syntax changed layout: %s vs %s", pair[0], pair[1])
			}
		}
	}
}

func TestBraceRewritePreservesUnrelatedAndIncompleteCommands(t *testing.T) {
	for _, source := range []string{`\underbraceCustom{x}_n`, `\\underbrace{x}_n`, `\underbrace`, `\underbrace{`, `\underbrace\`, `x\`} {
		if got := rewriteBraceScripts(source); got != source {
			t.Errorf("rewrite changed incomplete or unrelated input: %q -> %q", source, got)
		}
	}
}
