package latexpdf

import (
	"fmt"
	"strings"
	"testing"
	"unicode/utf8"

	"golang.org/x/net/html"
)

func TestExpandMathMacros(t *testing.T) {
	source := `
\newcommand{\Sym}{\operatorname{Sym}}
\newcommand{\pair}[2]{\langle #1,#2\rangle}
\newcommand{\Z}{\mathbb{Z}}
`
	macros := extractMathMacros(source)
	got := expandMathMacros(`\Sym_{\Z}(M)=\pair{x}{y}`, macros)
	want := `{\operatorname{Sym}}_{{\mathbb{Z}}}(M)={\langle {x},{y}\rangle}`
	if got != want {
		t.Fatalf("expanded macros mismatch\n got: %s\nwant: %s", got, want)
	}
}

func TestRenderMathWithDocumentMacros(t *testing.T) {
	source := `
\newcommand{\Sym}{\operatorname{Sym}}
\newcommand{\Z}{\mathbb{Z}}
\begin{document}
$\Sym_{\Z}(M)$
\end{document}`
	result := newLatexRenderer().render(source, "test-document")
	if result.MathErrors != 0 {
		t.Fatalf("unexpected MathJax errors: %d", result.MathErrors)
	}
	html := string(result.Body)
	if !strings.Contains(html, "<svg") || strings.Contains(html, "merror") {
		t.Fatalf("expected successful SVG math, got %s", html)
	}
}

func TestScopedTextColorsRenderInHTMLAndPDFStyles(t *testing.T) {
	source := `\documentclass{article}
\definecolor{brand}{RGB}{18,171,52}
\begin{document}
{\color{brand}Brand $x$ {\color{blue}blue} brand} plain
\textcolor[HTML]{804020}{direct}
\end{document}`
	result := newLatexRenderer().render(source, "scoped-text-colors")
	body := string(result.Body)
	for _, wanted := range []string{
		`<span style="color:#12ab34">Brand `,
		`<span style="color:#0000ff">blue</span>`,
		`<span style="color:#804020">direct</span>`,
	} {
		if !strings.Contains(body, wanted) {
			t.Errorf("colored HTML is missing %q: %s", wanted, body)
		}
	}
	for _, leaked := range []string{`definecolor`, `textcolor`, `{brand}`} {
		if strings.Contains(body, leaked) {
			t.Errorf("color command leaked %q into HTML: %s", leaked, body)
		}
	}

	root, err := html.Parse(strings.NewReader("<html><body>" + body + "</body></html>"))
	if err != nil {
		t.Fatal(err)
	}
	paragraph := findHTMLNode(root, "p")
	if paragraph == nil {
		t.Fatalf("colored output has no paragraph: %s", body)
	}
	layout := &pdfDocumentLayout{renderer: newPDFRenderer()}
	runs := layout.collectInlineChildren(paragraph, bodyPDFStyle())
	foundBrandText, foundBlue, foundPlain, foundDirect, foundFormula := false, false, false, false, false
	for _, run := range runs {
		switch {
		case strings.Contains(run.text, "Brand"):
			foundBrandText = run.style.red == 18 && run.style.green == 171 && run.style.blue == 52
		case strings.Contains(run.text, "blue"):
			foundBlue = run.style.red == 0 && run.style.green == 0 && run.style.blue == 255
		case strings.Contains(run.text, "plain"):
			foundPlain = run.style.red == 23 && run.style.green == 32 && run.style.blue == 51
		case strings.Contains(run.text, "direct"):
			foundDirect = run.style.red == 128 && run.style.green == 64 && run.style.blue == 32
		case run.formula != nil:
			foundFormula = run.style.red == 18 && run.style.green == 171 && run.style.blue == 52
			for _, path := range run.formula.paths {
				if path.Fill != "" && !path.fillPaint.current {
					t.Errorf("uncolored formula path unexpectedly has fixed paint %+v", path.fillPaint)
				}
			}
		}
	}
	if !foundBrandText || !foundBlue || !foundPlain || !foundDirect || !foundFormula {
		t.Fatalf("PDF color styles were not scoped correctly: %+v", runs)
	}
}

func TestHeadingColorsDoNotPolluteSectionMetadata(t *testing.T) {
	source := `\begin{document}
\section{\textcolor{red}{Red} and {\color{blue}Blue}}
\end{document}`
	result := newLatexRenderer().render(source, "colored-heading-metadata")
	if len(result.Sections) != 1 || result.Sections[0].Title != "Red and Blue" {
		t.Fatalf("colored heading metadata = %#v, want Red and Blue", result.Sections)
	}
	body := string(result.Body)
	for _, wanted := range []string{`style="color:#ff0000">Red`, `style="color:#0000ff">Blue`} {
		if !strings.Contains(body, wanted) {
			t.Errorf("colored heading HTML is missing %q: %s", wanted, body)
		}
	}
}

func TestCommonGroupNotationRendersWithoutParentPreamble(t *testing.T) {
	source := `\section{Fragment}
$\ur_x\omega^a$, $\SL_2(\F_5)$, $\PGL_2$, $\GSp_4$,
$\Spin_4$, $\U_2$, and $\Sym^3V\otimes\det^b$.`
	result := newLatexRenderer().render(source, "standalone-group-fragment")
	if result.MathErrors != 0 {
		t.Fatalf("standalone notation produced %d math errors: %#v", result.MathErrors, result.MathErrorExpressions)
	}
	if got := strings.Count(string(result.Body), `class="math-inline"`); got != 7 {
		t.Fatalf("rendered inline formulas = %d, want 7: %s", got, result.Body)
	}
}

func TestCommonDimNotationRendersWithoutParentPreamble(t *testing.T) {
	source := `\section{Fragment}
$\j$, $\bF_p$, $\bFp$, $\bdF$, $\Chtop(\cX_{\lsup LG})$,
$\Remain$, $\Der$, $\Res$, $\Sim$, $\Ga$, $\rmU_n$, $\bQ$, $\bZ$,
$\red$, $\Gal$, $\spec$, $\bG$, $\Det$, and $\cO$.`
	result := newLatexRenderer().render(source, "standalone-dim-fragment")
	if result.MathErrors != 0 {
		t.Fatalf("standalone dim notation produced %d math errors: %#v", result.MathErrors, result.MathErrorExpressions)
	}
	if got := strings.Count(string(result.Body), `class="math-inline"`); got != 19 {
		t.Fatalf("rendered inline formulas = %d, want 19: %s", got, result.Body)
	}
}

func TestTabularTextAndFormulaeRenderAsVectorMath(t *testing.T) {
	source := `\documentclass{article}
\newcommand{\PacketTable}{%
\begin{tabular}{ccc}
\toprule
Weight & Algebraic $r$ & Representation\\
\midrule
$\mathbf1$ & $0$ & $\SL_2(\F_5)$\\
$\Sym^2V$ & $2$ & text with $\ur_x\omega^a$\\
\bottomrule
\end{tabular}}
\begin{document}
\PacketTable
\end{document}`
	result := newLatexRenderer().render(source, "vector-tabular-math")
	body := string(result.Body)
	if result.MathErrors != 0 {
		t.Fatalf("table produced %d math errors: %#v\n%s", result.MathErrors, result.MathErrorExpressions, body)
	}
	if strings.Contains(body, `class="tex-table"`) || !strings.Contains(body, `class="math-display"`) || !strings.Contains(body, "<svg") {
		t.Fatalf("table was not rendered as vector display math: %s", body)
	}
	if strings.Contains(body, `{ccc}\text{Weight}`) || !strings.Contains(body, `\begin{array}{lll}`) {
		t.Fatalf("table column preamble leaked into rendered cells: %s", body)
	}
	for _, raw := range []string{`>\\SL`, `>\\Sym`, `>\\ur`} {
		if strings.Contains(body, raw) {
			t.Errorf("raw table formula %q leaked into HTML: %s", raw, body)
		}
	}
}

func TestStandaloneGeneratedTableDefinitionsRemainPreviewable(t *testing.T) {
	source := `\newcommand{\FirstTable}{\begin{tabular}{cc}Name & $x$\\A & $1$\end{tabular}}
\newcommand{\SecondTable}{\begin{tabular}{cc}Name & $y$\\B & $2$\end{tabular}}`
	result := newLatexRenderer().render(source, "standalone-table-definitions")
	if result.MathErrors != 0 {
		t.Fatalf("standalone tables produced %d math errors: %#v", result.MathErrors, result.MathErrorExpressions)
	}
	body := string(result.Body)
	if got := strings.Count(body, `class="math-display"`); got != 2 {
		t.Fatalf("standalone rendered tables = %d, want 2: %s", got, body)
	}
}

func TestSupplementTableMacrosForStandaloneFragment(t *testing.T) {
	definitions := `\newcommand{\GeneratedTable}{\begin{tabular}{cc}Name & $x$\\A & $1$\end{tabular}}`
	fragment := `\section{Fragment}\begin{center}\GeneratedTable\end{center}`
	source := SupplementTableMacros(fragment, definitions)
	if !strings.Contains(source, `\begin{document}`) || !strings.Contains(source, definitions) {
		t.Fatalf("supplemented fragment is missing declarations or document wrapper: %s", source)
	}
	result := newLatexRenderer().render(source, "supplemented-table-fragment")
	body := string(result.Body)
	if result.MathErrors != 0 || strings.Count(body, `class="math-display"`) != 1 || strings.Contains(body, "GeneratedTable") {
		t.Fatalf("supplemented table did not render exactly once: errors=%d body=%s", result.MathErrors, body)
	}
	if unchanged := SupplementTableMacros(`No generated table here.`, definitions); unchanged != `No generated table here.` {
		t.Fatalf("unused definitions changed the source: %s", unchanged)
	}
}

func TestOptionalSectionTitleRendersLongHeading(t *testing.T) {
	source := `\documentclass{article}\begin{document}
	\section[Short title]{Long title}
Body.
\subsection*[Short detail]{Long detail}
\end{document}`
	result := newLatexRenderer().render(source, "optional-section-title")
	body := string(result.Body)
	for _, wanted := range []string{`<h2 id="section-1">`, `Long `, `title`, `<h3 id="section-2">Long detail</h3>`} {
		if !strings.Contains(body, wanted) {
			t.Errorf("optional heading output is missing %q: %s", wanted, body)
		}
	}
	for _, leaked := range []string{`section[`, `Short title`, `Short detail`} {
		if strings.Contains(body, leaked) {
			t.Errorf("optional heading syntax leaked %q: %s", leaked, body)
		}
	}
	if len(result.Sections) != 2 || result.Sections[0].Title != "Short title" || result.Sections[0].Number != "1" || result.Sections[1].Title != "Short detail" || result.Sections[1].Number != "" {
		t.Fatalf("optional heading metadata = %#v", result.Sections)
	}
}

func TestRevisionConditionalsOptionalMacrosAndEditorialMarkers(t *testing.T) {
	source := `\def\RevisionVThree{1}
\documentclass{article}
\ifdefined\RevisionDiff
  \newcommand{\OLD}[1]{old-diff:#1}
  \newcommand{\NEW}[2][cyan]{new-diff:#2}
\else
  \ifdefined\RevisionVThree
    \newcommand{\OLD}[1]{}
    \newcommand{\NEW}[2][cyan]{{\color{#1}#2}}
  \else
    \newcommand{\OLD}[1]{#1}
    \newcommand{\NEW}[2][red]{{\color{#1}#2}}
  \fi
\fi
\NewExpandableDocumentCommand{\AUTHORREMARK}{o +m}{}
\begin{document}
\OLD{deleted text}
\NEW{current text}
\NEW[blue]{explicit text}
\AUTHORREMARK[remark:test]{hidden text and $\notARealMathCommand$.}
$\NEW{x+y}$
\end{document}`

	result := newLatexRenderer().render(source, "revision-conditionals")
	body := string(result.Body)
	for _, wanted := range []string{"current text", "explicit text", "math-inline"} {
		if !strings.Contains(body, wanted) {
			t.Errorf("rendered body is missing %q: %s", wanted, body)
		}
	}
	for _, leaked := range []string{"deleted text", "hidden text", "AUTHORREMARK"} {
		if strings.Contains(body, leaked) {
			t.Errorf("rendered body leaked %q: %s", leaked, body)
		}
	}
	if result.MathErrors != 0 {
		t.Fatalf("revision rendering produced %d math errors: %#v", result.MathErrors, result.MathErrorExpressions)
	}
}

func TestLatexDiffWrappersPreserveNestedMathematics(t *testing.T) {
	source := `\documentclass{article}
\providecommand{\DIFadd}[1]{\texorpdfstring{{\color{blue}#1}}{#1}}
\providecommand{\DIFdel}[1]{\texorpdfstring{{\color{red}#1}}{}}
\providecommand{\DIFaddbegin}{}
\providecommand{\DIFaddend}{}
\begin{document}
\DIFdel{Old $x^2$.}
\DIFaddbegin\DIFadd{New $y^2$ and
\[\begin{aligned}a&=b\\c&=d\end{aligned}\]}
\DIFaddend
\end{document}`
	result := newLatexRenderer().render(source, "latexdiff-nested-math")
	body := string(result.Body)
	if result.MathErrors != 0 {
		t.Fatalf("latexdiff rendering produced %d math errors: %#v\n%s", result.MathErrors, result.MathErrorExpressions, body)
	}
	for _, wanted := range []string{"Old", "New", `class="math-inline"`, `class="math-display"`} {
		if !strings.Contains(body, wanted) {
			t.Errorf("latexdiff rendering is missing %q: %s", wanted, body)
		}
	}
	if strings.Contains(body, "DIFadd") || strings.Contains(body, "DIFdel") {
		t.Fatalf("latexdiff command leaked into rendered output: %s", body)
	}
}

func TestPreambleAndBodyDeclarationsDoNotRenderAsProse(t *testing.T) {
	source := `\documentclass[11pt]{article}
\usepackage[margin=1in]{geometry}
\newcommand{\cA}{\mathcal A}
\newtheorem{theorem}{Theorem}[section]
\hypersetup{hidelinks}
\begin{document}
\renewcommand{\cA}{\mathcal B}
\setlength{\parindent}{0pt}
\theoremstyle{plain}
Visible text with $\cA$.
\end{document}`
	result := newLatexRenderer().render(source, "hidden-preamble-declarations")
	body := string(result.Body)
	if result.MathErrors != 0 || !strings.Contains(body, `class="math-inline"`) {
		t.Fatalf("body macro was not retained for mathematics: %s", body)
	}
	for _, leaked := range []string{"renewcommand", "parindent", "theoremstyle", "plainVisible", "0pt"} {
		if strings.Contains(body, leaked) {
			t.Errorf("non-rendering declaration %q leaked into HTML: %s", leaked, body)
		}
	}
	if strings.Count(body, "<p>") != 1 || !strings.Contains(body, "Visible text with") {
		t.Fatalf("declarations created empty or malformed paragraphs: %s", body)
	}
}

func TestFragmentPreambleCommandsAreConsumed(t *testing.T) {
	source := `\documentclass{article}
\usepackage{amsmath}
\title{Setup-only title}
\def\temporary#1{discard #1}
\section{Rendered section}
Rendered body.`
	result := newLatexRenderer().render(source, "fragment-preamble")
	body := string(result.Body)
	for _, leaked := range []string{"article", "amsmath", "Setup-only title", "discard"} {
		if strings.Contains(body, leaked) {
			t.Errorf("fragment preamble text %q leaked into HTML: %s", leaked, body)
		}
	}
	if !strings.Contains(body, "Rendered section") || !strings.Contains(body, "Rendered body.") {
		t.Fatalf("fragment body was removed with its preamble: %s", body)
	}
}

func TestLatexRendererEscapesText(t *testing.T) {
	source := `\begin{document}<script>alert(1)</script> $x$\end{document}`
	result := newLatexRenderer().render(source, "escape-test")
	if strings.Contains(string(result.Body), "<script>") {
		t.Fatal("renderer emitted unescaped source HTML")
	}
}

func TestNormalizeAdjacentSimpleScripts(t *testing.T) {
	got := normalizeMathForEngine(`M^{\otimes n}=M\otimes_RM,\quad \bigwedge^nu`)
	want := `M^{\otimes n}=M\otimes_{R}M,\quad \bigwedge^{n}u`
	if got != want {
		t.Fatalf("normalized expression mismatch\n got: %s\nwant: %s", got, want)
	}
}

func TestNormalizePrimeAccentsAndCommandScriptArguments(t *testing.T) {
	formulas := []string{
		`\Phi'^{-1}+M''^{[s,r]}+Q'^\bullet`,
		`G_K^\mathrm{ab}+t_\mathrm{id}`,
		`\widetilde{\overline{g_0}^{f_L/f_K}}`,
		"x\\\n  \\sum_{i=0}^n i",
	}
	for index, formula := range formulas {
		source := `\begin{document}$` + formula + `$\end{document}`
		result := newLatexRenderer().render(source, fmt.Sprintf("script-normalization-%d", index))
		if result.MathErrors != 0 {
			t.Errorf("formula %q produced %d MathJax errors: %#v", formula, result.MathErrors, result.MathErrorExpressions)
		}
	}
}

func TestDeclaredMathAlphabetsUseCompatibleVectorFonts(t *testing.T) {
	source := `
\DeclareMathAlphabet{\mathpzc}{OT1}{pzc}{m}{it}
\DeclareSymbolFontAlphabet{\mathbbl}{bbold}
\begin{document}$\mathpzc{w}+\mathbbl{\Delta}$\end{document}`
	macros := extractMathMacros(source)
	if got := expandMathMacros(`\mathpzc{w}+\mathbbl{\Delta}`, macros); !strings.Contains(got, `\mathcal`) || !strings.Contains(got, `\mathbb`) {
		t.Fatalf("declared math alphabets expanded to %q", got)
	}
	result := newLatexRenderer().render(source, "declared-math-alphabet")
	if result.MathErrors != 0 {
		t.Fatalf("declared math alphabets produced %d MathJax errors: %#v", result.MathErrors, result.MathErrorExpressions)
	}
}

func TestMathLayoutAndPackageCompatibilityCommands(t *testing.T) {
	formulas := []string{
		`\mathlarger{\Delta}+\mathsection`,
		`E^\dag+\Z\llbracket x\rrbracket`,
		`\scalebox{.8}{\rotatebox[origin=c]{90}{D}}`,
		`\mathpzc{w}`,
	}
	for index, formula := range formulas {
		result := newLatexRenderer().render(`\begin{document}$`+formula+`$\end{document}`, fmt.Sprintf("package-compatibility-%d", index))
		if result.MathErrors != 0 {
			t.Errorf("formula %q produced %d MathJax errors: %#v", formula, result.MathErrors, result.MathErrorExpressions)
		}
	}
}

func TestMathFootnoteRendersOutsideFormula(t *testing.T) {
	source := `\begin{document}\[a\le b.\footnote{The strict case $a<b$ is excluded.}\]\end{document}`
	result := newLatexRenderer().render(source, "math-footnote")
	body := string(result.Body)
	if result.MathErrors != 0 || !strings.Contains(body, `class="footnote"`) || !strings.Contains(body, "strict case") {
		t.Fatalf("math footnote render = errors %d, body %s", result.MathErrors, body)
	}
}

func TestPrimitiveDefinitionUsesUnknownLiteralFallback(t *testing.T) {
	source := `\def\L#1{{}^\mathrm{L}#1}\begin{document}$\L X$\end{document}`
	result := newLatexRenderer().render(source, "primitive-shadow")
	body := string(result.Body)
	if result.MathErrors != 1 || !strings.Contains(body, `data-unknown-commands="\L"`) || !strings.Contains(body, `fill="#ff0000"`) || strings.Contains(body, `data-mjx-error`) {
		t.Fatalf("primitive \\def was not shown as an unknown red literal: errors=%d body=%s", result.MathErrors, result.Body)
	}
}

func TestUnknownCommandsRenderAsRedLiterals(t *testing.T) {
	source := `\begin{document}
Prose \NoSuch{body}.
Inline $x+\NoSuch_1+\Other$.
\end{document}`
	result := newLatexRenderer().render(source, "red-unknown-command")
	body := string(result.Body)
	if result.MathErrors != 1 {
		t.Fatalf("unknown math command diagnostics = %d, want 1: %#v", result.MathErrors, result.MathErrorExpressions)
	}
	for _, wanted := range []string{
		`class="unknown-command" style="color:#ff0000">\NoSuch</span>body`,
		`data-unknown-commands="\NoSuch \Other"`,
		`fill="#ff0000"`,
	} {
		if !strings.Contains(body, wanted) {
			t.Errorf("unknown-command output is missing %q: %s", wanted, body)
		}
	}
	if strings.Contains(body, `data-mml-node="merror"`) || strings.Contains(body, `data-mjx-error`) || strings.Contains(body, `class="math-error"`) {
		t.Fatalf("unknown command retained an error box: %s", body)
	}
}

func TestRenderTensorPowerFormula(t *testing.T) {
	source := `\begin{document}
\[
M^{\otimes0}=R,\qquad M^{\otimes n}=\underbrace{M\otimes_R\cdots\otimes_RM}_{n\text{ 次}}.
\]
\end{document}`
	result := newLatexRenderer().render(source, "tensor-power-test")
	if result.MathErrors != 0 {
		t.Fatalf("valid tensor-power formula produced %d MathJax errors: %s", result.MathErrors, result.Body)
	}
}

func TestSolutionConditionals(t *testing.T) {
	quiz := `
\newif\ifshowsolutions
\ifdefined\withsolutions
  \showsolutionstrue
\fi
\begin{document}
question
\ifshowsolutions answer\fi
\end{document}`
	without := preprocessKnownConditionals(quiz)
	if strings.Contains(without, "answer") {
		t.Fatal("solution was shown without the withsolutions definition")
	}
	with := preprocessKnownConditionals(`\def\withsolutions{1}` + quiz)
	if !strings.Contains(with, "answer") {
		t.Fatal("solution was hidden despite the withsolutions definition")
	}
	malformed := `\ifdefined\unfinished text`
	if got := preprocessKnownConditionals(malformed); got != malformed {
		t.Fatalf("malformed conditional changed from %q to %q", malformed, got)
	}
	plain := `before\iffalse hidden \iftrue nested\fi\else visible\fi after`
	if got := preprocessKnownConditionals(plain); got != "before visible after" {
		t.Fatalf("plain true/false conditional = %q", got)
	}
}

func TestEqnarrayRendersAsNumberedVectorMath(t *testing.T) {
	source := `\newcommand{\gr}{\operatorname{gr}}
\begin{document}
\begin{eqnarray}
\label{eq:graded}\gr^0 V&=&Z\\
\gr^1 V&=&W
\end{eqnarray}
See \eqref{eq:graded}.
\end{document}`
	rendered := newLatexRenderer().render(source, "eqnarray-vector-math")
	body := string(rendered.Body)
	if rendered.MathErrors != 0 || strings.Contains(body, `class="unknown-command"`) || strings.Contains(body, `data-mml-node="merror"`) {
		t.Fatalf("eqnarray render = errors %d, body %s", rendered.MathErrors, body)
	}
	if strings.Contains(body, `data-c="2C"`) {
		t.Fatalf("row-ending source whitespace became a visible comma: %s", body)
	}
	for _, wanted := range []string{`class="math-display"`, `\gr^0 V`, `href="#ref-eq-graded"`, `class="equation-numbers"`} {
		if !strings.Contains(body, wanted) {
			t.Errorf("eqnarray output is missing %q: %s", wanted, body)
		}
	}
}

func TestMathJaxCompatibilityRewrites(t *testing.T) {
	preamble := `
\newcommand{\Z}{\mathbb Z}
\newcommand{\OO}{\mathcal O}
\newcommand{\m}{\mathfrak m}
\newcommand{\fitmath}[1]{\resizebox{\linewidth}{!}{\ensuremath{\displaystyle #1}}}
\newcommand{\circled}[1]{\tikz\node{#1};}`
	formulas := []string{
		`\varprojlim_J X_n\cong\varinjlim_I Y_i`,
		`a\equiv b\pmod{3}`,
		`a\equiv b\pmod\m`,
		`\widehat\Z`,
		`\widehat\OO`,
		`x_1,\dots,x_n`,
		`D^\bullet`,
		`\bar y^p`,
		`\fitmath{x+y}`,
		`\circled{1}`,
		`\qed`,
		`x=y.\qedhere`,
		`\begin{aligned}\label{eq:test}x&=y\end{aligned}`,
		`\bbpi:\Gamma\to\operatorname{Aut}(D)`,
		`\begin{aligned}M:=\{x\mid f(x)\in\begin{cases}A,&n\le l\\B,&n>l\end{cases}\}\end{aligned}`,
		`\begin{aligned}A&=\begin{pmatrix}1&0\\0&1\end{pmatrix}\\B&=1\end{aligned}`,
		`\begin{array}{cc}A&\multicolumn{1}{c}{B}\\1&2\end{array}`,
		`\begin{array}{cc}A&B\\\cline{1-2}1&2\end{array}`,
		`\begin{aligned}a&=\left\{\begin{aligned}b&=1\\c&=2\end{aligned}\right.\end{aligned}`,
		`\xymatrix{A\ar@{=}[rr]\ar[d]^f&&B\\C\ar[r]&D}`,
		`\texorpdfstring{{\protect\color{red}\scriptsize m}}{}+\texorpdfstring{{\protect\color{blue}\sf n}}{{n}}`,
		`\begin{CD}&P_M @>{a}>> Z_N\\&&@AA{d}A\\&&P_N\end{CD}`,
		`\begin{CD}0@>>>\Z@>{\times6}>>\Z@>>>\Z/6\Z@>>>0\\@.@V{\times2}VV@V{\operatorname{id}}VV@V{f}VV@.\\0@>>>\Z@>{\times3}>>\Z@>>>\Z/3\Z@>>>0\end{CD}`,
	}
	for index, formula := range formulas {
		source := preamble + `\begin{document}\[` + formula + `\]\end{document}`
		result := newLatexRenderer().render(source, fmt.Sprintf("compatibility-test-%d", index))
		if result.MathErrors != 0 {
			t.Errorf("formula %q produced %d MathJax errors: %s", formula, result.MathErrors, result.Body)
		}
	}
}

func TestNormalizeCDLeadingAlignmentAndMacroDelimiter(t *testing.T) {
	source := `\begin{CD}&P @>{a}>> Z\\&&@AA{d}A\\&&N\end{CD}`
	want := `\begin{CD}P @>{a}>> Z\\@.@AA{d}A\\@.N\end{CD}`
	if got := normalizeCDLeadingTabs(source); got != want {
		t.Fatalf("normalized CD mismatch\n got: %s\nwant: %s", got, want)
	}
	macros := map[string]mathMacro{"Z": {body: `\mathbb Z`}}
	if got := expandMathMacros(`\Z@>>>0`, macros); got != `{\mathbb Z}@>>>0` {
		t.Fatalf("CD-delimited custom macro expansion = %q", got)
	}
}

func TestMathDollarInsideTextArgument(t *testing.T) {
	source := `\begin{document}$\left\{\text{$\mathbb C$ 上的函数}\right\}$\end{document}`
	result := newLatexRenderer().render(source, "nested-dollar-test")
	if result.MathErrors != 0 {
		t.Fatalf("nested text math produced %d MathJax errors: %s", result.MathErrors, result.Body)
	}
}

func TestInlineMatrixAcrossLinesStaysInsideMath(t *testing.T) {
	source := `\begin{document}
\mbox{$M=
\begin{bmatrix}
1&0\\0&1
\end{bmatrix}
$} if and only if $M=I$.
\end{document}`
	result := newLatexRenderer().render(source, "inline-matrix-lines")
	if result.MathErrors != 0 {
		t.Fatalf("inline matrix produced %d math errors: %#v\n%s", result.MathErrors, result.MathErrorExpressions, result.Body)
	}
	if got := strings.Count(string(result.Body), `class="math-inline"`); got != 2 {
		t.Fatalf("inline formula count = %d, want 2: %s", got, result.Body)
	}
}

func TestRenderedSectionsReceiveStableAnchors(t *testing.T) {
	source := `\begin{document}
\section{First section}
Text.
\subsection{Second section}
More text.
\attentionwindow{10--20 minutes}{Window title}
\end{document}`
	result := newLatexRenderer().render(source, "section-anchor-test")
	if len(result.Sections) != 3 {
		t.Fatalf("got %d sections, want 3: %#v", len(result.Sections), result.Sections)
	}
	for index, section := range result.Sections {
		wantID := fmt.Sprintf("section-%d", index+1)
		if section.ID != wantID || !strings.Contains(string(result.Body), `id="`+wantID+`"`) {
			t.Fatalf("missing stable section anchor %q: %#v", wantID, result)
		}
	}
}

func TestTexorpdfstringUsesVisualAndPlainTextBranches(t *testing.T) {
	source := `\begin{document}\section{\texorpdfstring{Pseudo-$\delta$}{Pseudo-delta} rings}\end{document}`
	result := newLatexRenderer().render(source, "texorpdfstring-test")
	if len(result.Sections) != 1 || result.Sections[0].Title != "Pseudo-delta rings" {
		t.Fatalf("plain section title = %#v", result.Sections)
	}
	body := string(result.Body)
	if !strings.Contains(body, `Pseudo-<span class="math-inline"`) || strings.Contains(body, "Pseudo-delta") {
		t.Fatalf("visual section title used the wrong texorpdfstring branch: %s", body)
	}
}

func TestTheoremNumberingAndReferencesUseDocumentIndex(t *testing.T) {
	source := `\documentclass{article}
\usepackage{amsthm}
\newtheorem{theorem}{Theorem}[section]
\newtheorem{proposition}[theorem]{Proposition}
\theoremstyle{definition}
\newtheorem{definition}[theorem]{Definition}
\numberwithin{equation}{section}
\begin{document}
\section{First}
\begin{definition}[Graph pseudo-$\delta$ ring]\label{def:graph}
Definition body.
\end{definition}
\begin{equation}\label{eq:first}
x=y.
\end{equation}
\begin{align}
a&=b,\\
c&=d.\label{eq:second}
\end{align}
\begin{proposition}\label{prop:first}
See Definition~\ref{def:graph}, equations \eqref{eq:first} and
\eqref{eq:second}, and \ref{missing}.
\end{proposition}
\section{Second}
\begin{theorem}\label{thm:second}
Second-section theorem.
\end{theorem}
\end{document}`
	result := newLatexRenderer().render(source, "theorem-reference-index-test")
	body := string(result.Body)
	for _, required := range []string{
		`id="ref-def-graph"><h3>Definition 1.1 <span class="statement-option">(Graph pseudo-`,
		`id="ref-eq-first"`, `<span class="equation-number">(1.1)</span>`,
		`id="ref-eq-second"`, `<span class="equation-numbers"><span>(1.2)</span><span>(1.3)</span></span>`,
		`id="ref-prop-first"><h3>Proposition 1.2</h3>`,
		`href="#ref-def-graph">1.1</a>`, `href="#ref-eq-first">(1.1)</a>`,
		`href="#ref-eq-second">(1.3)</a>`,
		`class="reference unresolved"`,
		`id="ref-thm-second"><h3>Theorem 2.1</h3>`,
	} {
		if !strings.Contains(body, required) {
			t.Errorf("rendered document is missing %q: %s", required, body)
		}
	}

	prepared, pages := prepareLatexPages(source, 220)
	if len(pages) < 2 {
		t.Fatalf("reference test did not paginate: %#v", pages)
	}
	var paged strings.Builder
	renderer := newLatexRenderer()
	for pageNumber, page := range pages {
		if page.Oversized {
			continue
		}
		pageResult := renderer.renderPrepared(prepared, page.Source, fmt.Sprintf("theorem-reference-page-%d", pageNumber+1))
		paged.WriteString(string(pageResult.Body))
	}
	for _, required := range []string{
		`id="ref-prop-first"><h3>Proposition 1.2</h3>`,
		`href="#ref-def-graph">1.1</a>`,
		`href="#ref-eq-second">(1.3)</a>`,
		`id="ref-thm-second"><h3>Theorem 2.1</h3>`,
	} {
		if !strings.Contains(paged.String(), required) {
			t.Errorf("paginated rendering is missing %q: %s", required, paged.String())
		}
	}
}

func TestSharedCountersSectionItemReferencesAndManualTags(t *testing.T) {
	source := `\documentclass{article}
\numberwithin{equation}{section}
\begin{document}
\newtheorem{theorem}[equation]{Theorem}
\section{First}\label{sec:first}
\begin{equation}\tag{A}\label{eq:manual}x=y\end{equation}
\begin{theorem}\label{thm:shared}Shared counter.\end{theorem}
\subsection{Details}\label{subsec:details}
\begin{enumerate}
\item\label{item:first} First item.
\item Second item.
\end{enumerate}
See Section~\ref{sec:first}, Subsection~\ref{subsec:details},
Theorem~\ref{thm:shared}, equation~\eqref{eq:manual}, and item~\ref{item:first}.
\section{Second}
\begin{equation}\label{eq:second}u=v\end{equation}
\end{document}`
	result := newLatexRenderer().render(source, "shared-counter-reference-test")
	body := string(result.Body)
	for _, required := range []string{
		`<h2 id="section-1"><span class="section-number">1</span> First</h2>`,
		`<h3 id="section-2"><span class="section-number">1.1</span> Details</h3>`,
		`<h2 id="section-3"><span class="section-number">2</span> Second</h2>`,
		`id="ref-eq-manual"`, `<span class="equation-number">(A)</span>`,
		`id="ref-thm-shared"><h3>Theorem 1.1</h3>`,
		`id="ref-item-first"`,
		`href="#section-1">1</a>`, `href="#section-2">1.1</a>`,
		`href="#ref-thm-shared">1.1</a>`, `href="#ref-eq-manual">(A)</a>`,
		`href="#ref-item-first">1</a>`,
		`id="ref-eq-second"`, `<span class="equation-number">(2.1)</span>`,
	} {
		if !strings.Contains(body, required) {
			t.Errorf("shared-counter rendering is missing %q: %s", required, body)
		}
	}
	if strings.Contains(body, `data-tex="\tag`) || strings.Contains(body, `data-tex="x=y\tag`) {
		t.Fatalf("manual equation tag leaked into MathJax input: %s", body)
	}
}

func TestAppendixCustomEnumerateAndAliasLabels(t *testing.T) {
	source := `\documentclass{article}
\newtheorem{theorem}{Theorem}[section]
\numberwithin{equation}{section}
\begin{document}
\appendix
\section{Supplement}\label{sec:supplement}
\subsection{Details}\label{subsec:supplement}
\begin{theorem}\label{thm:first}\label{thm:alias}Statement.\end{theorem}
\begin{equation}\label{eq:appendix}x=y\end{equation}
\begin{enumerate}[label=(F\arabic*)]
\item\label{item:F1} First condition.
\item Second condition.
\end{enumerate}
See \ref{sec:supplement}, \ref{subsec:supplement}, \ref{thm:first},
\ref{thm:alias}, \eqref{eq:appendix}, and \ref{item:F1}.
\[
u=v.\tag*{dagger}
\]
\end{document}`
	result := newLatexRenderer().render(source, "appendix-enumerate-alias-test")
	body := string(result.Body)
	for _, required := range []string{
		`<h2 id="section-1"><span class="section-number">A</span> Supplement</h2>`,
		`<h3 id="section-2"><span class="section-number">A.1</span> Details</h3>`,
		`id="ref-thm-first"><h3>Theorem A.1</h3>`,
		`id="ref-eq-appendix"`, `<span class="equation-number">(A.1)</span>`,
		`<ol class="custom-enumerate">`, `data-marker="(F1)"`, `data-marker="(F2)"`,
		`id="ref-item-F1"`, `href="#ref-item-F1">(F1)</a>`,
		`href="#ref-thm-first">A.1</a>`,
		`<span class="equation-number">dagger</span>`,
	} {
		if !strings.Contains(body, required) {
			t.Errorf("appendix/list rendering is missing %q: %s", required, body)
		}
	}
	if strings.Count(body, `href="#ref-thm-first">A.1</a>`) != 2 {
		t.Errorf("theorem alias did not resolve to the primary anchor: %s", body)
	}
	if strings.Contains(body, `>appendix<`) || strings.Contains(body, `\tag`) {
		t.Errorf("non-rendering appendix/tag syntax leaked into output: %s", body)
	}
}

func TestCustomTheCounterFormatting(t *testing.T) {
	source := `\documentclass{article}
\newtheorem{intro}{Theorem}
\renewcommand{\theintro}{\Alph{intro}}
\newtheorem{introcorollary}[intro]{Corollary}
\begin{document}
\begin{intro}\label{intro:a}First.\end{intro}
\begin{introcorollary}\label{intro:b}Second.\end{introcorollary}
See \ref{intro:a} and \ref{intro:b}.
\end{document}`
	result := newLatexRenderer().render(source, "custom-counter-format-test")
	body := string(result.Body)
	for _, required := range []string{
		`id="ref-intro-a"><h3>Theorem A</h3>`,
		`id="ref-intro-b"><h3>Corollary B</h3>`,
		`href="#ref-intro-a">A</a>`, `href="#ref-intro-b">B</a>`,
	} {
		if !strings.Contains(body, required) {
			t.Errorf("custom counter rendering is missing %q: %s", required, body)
		}
	}
}

func TestCitationsAndBibliographyResolveToRenderedEntries(t *testing.T) {
	source := `\documentclass{article}
\begin{document}
Numeric \cite{alpha}; named \cite[\S~2]{beta}; together \cite{alpha,beta};
missing \cite{missing}.
\begin{thebibliography}{99}
\bibitem{alpha} A.~Author, \emph{R\'esum\'e title}.
\bibitem[B$_2$]{beta} B.~Author, \emph{Z\"one title}.
\end{thebibliography}
\end{document}`
	result := newLatexRenderer().render(source, "citation-bibliography-test")
	body := string(result.Body)
	for _, required := range []string{
		`<span class="citation">[<a class="citation-link" href="#bib-alpha">1</a>]</span>`,
		`<a class="citation-link" href="#bib-beta">B<span class="math-inline"`,
		`§&nbsp;2`,
		`href="#bib-alpha">1</a>, <a class="citation-link" href="#bib-beta">`,
		`<span class="citation unresolved">[<span class="citation-key unresolved" title="Unresolved citation: missing">?</span>]</span>`,
		`<section class="bibliography"><h2>References</h2><ol class="bibliography-list">`,
		`<li id="bib-alpha" data-marker="[1]">`,
		`<li id="bib-beta"`, `<span class="bibliography-marker">[B<span class="math-inline"`,
		`<em>Résumé title</em>`, `<em>Zöne title</em>`,
	} {
		if !strings.Contains(body, required) {
			t.Errorf("citation rendering is missing %q: %s", required, body)
		}
	}
	if result.MathErrors != 0 {
		t.Fatalf("bibliography label produced %d MathJax errors: %s", result.MathErrors, body)
	}
	if strings.Contains(body, `\bibitem`) || strings.Contains(body, `thebibliography`) {
		t.Fatalf("raw bibliography syntax leaked into output: %s", body)
	}
}

func TestTableOfContentsUsesGlobalSectionIndexAndTocDepth(t *testing.T) {
	source := `\documentclass{article}
\setcounter{tocdepth}{1}
\begin{document}
\tableofcontents
\section{First $X$}\label{sec:first-toc}
\subsection{Hidden detail}
\appendix
\section{Supplement}
\end{document}`
	result := newLatexRenderer().render(source, "table-of-contents-test")
	body := string(result.Body)
	for _, required := range []string{
		`<nav class="document-toc" aria-label="Table of contents">`,
		`class="toc-entry level-1" data-target="section-1"`,
		`<span class="toc-number">1</span>`,
		`<span class="toc-title">First <span class="math-inline"`,
		`class="toc-entry level-1" data-target="section-3"`,
		`<span class="toc-number">A</span><span class="toc-title">Supplement</span>`,
	} {
		if !strings.Contains(body, required) {
			t.Errorf("table of contents is missing %q: %s", required, body)
		}
	}
	if strings.Contains(body, `data-target="section-2"`) || strings.Contains(body, `>tableofcontents<`) {
		t.Fatalf("tocdepth/raw table-of-contents handling is incorrect: %s", body)
	}
}

func TestLatexPaginationUsesStructuralBoundaries(t *testing.T) {
	body := `\section{First}
First paragraph.

\begin{theorem}
The theorem remains one render block.
\end{theorem}

\subsection{Second}
Second paragraph.`
	blocks := splitLatexRenderBlocks(body)
	if len(blocks) < 5 {
		t.Fatalf("got %d render blocks, want at least 5: %#v", len(blocks), blocks)
	}
	limit := len(blocks[0]) + len(blocks[1])
	for _, block := range blocks {
		if len(block) > limit {
			limit = len(block)
		}
	}
	pages := paginateLatexBody(body, limit)
	if len(pages) < 3 {
		t.Fatalf("got %d pages, want at least 3: %#v", len(pages), pages)
	}
	var reconstructed strings.Builder
	for index, page := range pages {
		if len(page.Source) > limit {
			t.Errorf("page %d has %d bytes, limit %d", index+1, len(page.Source), limit)
		}
		if page.Oversized {
			t.Errorf("page %d unexpectedly used oversized fallback", index+1)
		}
		reconstructed.WriteString(page.Source)
	}
	if reconstructed.String() != body {
		t.Fatal("pagination did not preserve the normalized body")
	}
	if strings.Contains(pages[0].Source, `\begin{theorem}`) || !strings.HasPrefix(strings.TrimSpace(pages[1].Source), `\begin{theorem}`) {
		t.Fatalf("theorem was not used as a page boundary: %#v", pages)
	}
}

func TestOversizedLatexBlockUsesBoundedUTF8SourcePages(t *testing.T) {
	body := `\begin{theorem}` + strings.Repeat("界", 40) + `\end{theorem}`
	const limit = 32
	pages := paginateLatexBody(body, limit)
	if len(pages) < 2 {
		t.Fatalf("got %d pages, want multiple source pages", len(pages))
	}
	var reconstructed strings.Builder
	for index, page := range pages {
		if !page.Oversized {
			t.Errorf("page %d should use oversized source fallback", index+1)
		}
		if len(page.Source) > limit {
			t.Errorf("page %d has %d bytes, limit %d", index+1, len(page.Source), limit)
		}
		if !utf8.ValidString(page.Source) {
			t.Errorf("page %d split a UTF-8 code point", index+1)
		}
		reconstructed.WriteString(page.Source)
	}
	if reconstructed.String() != body {
		t.Fatal("oversized pagination did not preserve source")
	}
}

func TestDocumentSectionExtractionMatchesEachRenderedPage(t *testing.T) {
	source := `\documentclass{article}\begin{document}
\section{First}
First paragraph.

\begin{proof}
\subsection{Nested second}
Proof text.
\end{proof}

\section{Third}
Final paragraph.
\end{document}`
	prepared, pages := prepareLatexPages(source, 100)
	tableOfContents := latexDocumentSections(pages)
	if len(tableOfContents) != 3 {
		t.Fatalf("document ToC = %#v, want three sections", tableOfContents)
	}
	renderer := newLatexRenderer()
	sectionIndex := 0
	for pageIndex, page := range pages {
		if page.Oversized {
			continue
		}
		rendered := renderer.renderPrepared(prepared, page.Source, fmt.Sprintf("toc-page-%d", pageIndex+1))
		extracted := extractLatexSections(page.Source)
		if len(rendered.Sections) != len(extracted) {
			t.Fatalf("page %d rendered sections = %#v, extracted = %#v", pageIndex+1, rendered.Sections, extracted)
		}
		for index := range extracted {
			if rendered.Sections[index].ID != tableOfContents[sectionIndex].ID || rendered.Sections[index].Title != extracted[index].Title {
				t.Fatalf("page %d section mismatch: rendered=%#v extracted=%#v", pageIndex+1, rendered.Sections[index], extracted[index])
			}
			if tableOfContents[sectionIndex].Page != pageIndex+1 {
				t.Fatalf("document ToC entry mismatch: %#v", tableOfContents[sectionIndex])
			}
			sectionIndex++
		}
	}
}

func TestFlowchartDocumentIntrinsicBypassesPagination(t *testing.T) {
	source := `% document-type: flowchart
\documentclass{article}
\begin{document}
\begin{flowchart}
node A "` + strings.Repeat("wide chart label ", 20) + `"
node B "target"
A -> B
\end{flowchart}
\end{document}`
	if documentType := latexDocumentType(source); documentType != "flowchart" {
		t.Fatalf("document type = %q, want flowchart", documentType)
	}
	prepared, pages := prepareLatexPages(source, 64)
	if len(pages) != 1 || pages[0].Oversized {
		t.Fatalf("flowchart pages = %#v, want one complete render page", pages)
	}
	if len(pages[0].Source) <= 64 || !strings.Contains(prepared, `\begin{flowchart}`) {
		t.Fatalf("flowchart intrinsic did not preserve the complete body: %#v", pages[0])
	}

	bodyDirective := `\documentclass{article}\begin{document}% document-type: flowchart
text\end{document}`
	if documentType := latexDocumentType(bodyDirective); documentType != "" {
		t.Fatalf("body comment selected document type %q", documentType)
	}
}
