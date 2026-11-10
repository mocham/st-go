package latexpdf

import (
	"strings"
	"testing"
)

func TestSupplementTraditionalBBLBibliography(t *testing.T) {
	source := `\begin{document}
See \cite[Theorem 2]{alpha}.
\printbibliography[title={Works}]
\end{document}`
	bbl := `\begin{thebibliography}{A+24}
\bibitem[A{\etalchar{+}}24]{alpha}
Ada Author. \newblock {\em A useful result}. 2024.
\end{thebibliography}`
	supplemented := SupplementBibliography(source, bbl)
	if supplemented == source || strings.Contains(supplemented, `\printbibliography`) {
		t.Fatalf("traditional BBL was not attached: %s", supplemented)
	}
	rendered := newLatexRenderer().render(supplemented, "traditional-bbl")
	body := string(rendered.Body)
	for _, wanted := range []string{`class="citation"`, `href="#bib-alpha"`, `class="bibliography"`, `Ada Author`, `A useful result`} {
		if !strings.Contains(body, wanted) {
			t.Errorf("traditional BBL output is missing %q: %s", wanted, body)
		}
	}
	if rendered.MathErrors != 0 || strings.Contains(body, `class="unknown-command"`) || strings.Contains(body, `class="unresolved"`) {
		t.Fatalf("traditional BBL retained diagnostics: errors=%d body=%s", rendered.MathErrors, body)
	}
}

func TestSupplementBibLaTeXDataBBL(t *testing.T) {
	source := `\DeclareFieldFormat{labelalpha}{\thefield{entrykey}}
\begin{document}
See \cite{paper}.
\printbibliography
\end{document}`
	bbl := `\datalist[entry]{anyt/global}
\entry{paper}{article}{}{}
  \name{author}{1}{}{% 
    {{hash=DJ}{family={Doe},given={Jane}}}%
  }
  \field{labelalpha}{Doe24}
  \field{title}{Vector bibliographies}
  \field{journaltitle}{Journal of Native Rendering}
  \field{volume}{7}
  \field{pages}{1\bibrangedash 9}
  \field{year}{2024}
  \verb{eprint}
  \verb 2401.00001
  \endverb
\endentry
\enddatalist`
	supplemented := SupplementBibliography(source, bbl)
	for _, wanted := range []string{`\begin{thebibliography}`, `\bibitem[paper]{paper}`, `Jane Doe`, `pp.~1--9`, `arXiv:2401.00001`} {
		if !strings.Contains(supplemented, wanted) {
			t.Errorf("converted biblatex BBL is missing %q: %s", wanted, supplemented)
		}
	}
	rendered := newLatexRenderer().render(supplemented, "biblatex-data-bbl")
	body := string(rendered.Body)
	if rendered.MathErrors != 0 || strings.Contains(body, `class="unknown-command"`) || strings.Contains(body, `class="unresolved"`) {
		t.Fatalf("biblatex BBL retained diagnostics: errors=%d body=%s", rendered.MathErrors, body)
	}
	for _, wanted := range []string{`href="#bib-paper"`, `data-marker="[paper]"`, `Vector bibliographies`} {
		if !strings.Contains(body, wanted) {
			t.Errorf("biblatex BBL HTML is missing %q: %s", wanted, body)
		}
	}
}

func TestSupplementBibliographyKeepsUnsupportedFallbackWithoutEntries(t *testing.T) {
	source := `\begin{document}\printbibliography\end{document}`
	if got := SupplementBibliography(source, `\endinput`); got != source {
		t.Fatalf("unrecognized BBL changed source to %q", got)
	}
}
