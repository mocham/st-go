package outline

import (
	"strings"
	"testing"
)

func TestTeXOutlineHierarchyAndLocations(t *testing.T) {
	source := "世界 before\n\\chapter{One}\n\\section*[Short {title}]{Long title}\n\\subsection{Two \\textbf{words}}\n\\subsubsection{Three}\n\\subsubsubsection{Four}\n\\label{sec:four}\n\\section % comment\n{Multiline\n title \\label{sec:inside}}\n"
	entries := TeX([]rune(source))
	want := []struct {
		title string
		level int
	}{{"One", 0}, {"Short title", 1}, {"Two words", 2}, {"Three", 3}, {"Four", 4}, {"label: sec:four", 4}, {"Multiline title", 1}, {"label: sec:inside", 4}}
	if len(entries) != len(want) {
		t.Fatalf("outline = %+v", entries)
	}
	for i, entry := range entries {
		if entry.Title != want[i].title || entry.Level != want[i].level {
			t.Errorf("entry %d = %+v", i, entry)
		}
		prefix := string([]rune(source)[:entry.Offset])
		if entry.Line != 1+strings.Count(prefix, "\n") || []rune(source)[entry.Offset] != '\\' {
			t.Errorf("wrong source location: %+v", entry)
		}
	}
}

func TestTeXOutlineIgnoresNonSource(t *testing.T) {
	source := `% \section{comment}
\verb|\section{inline}| \% not a comment
\begin{verbatim}
\section{verbatim}
\end{verbatim}
\begin{lstlisting}
\label{listing}
\end{lstlisting}
\newcommand{\fake}[1]{\section{macro #1}}
\def\fakeagain#1{\label{macro}}
\\section{escaped command}
\section{Actual \& title}
\label{real}
`
	entries := TeX([]rune(source))
	if len(entries) != 2 || entries[0].Title != "Actual & title" || entries[1].Title != "label: real" {
		t.Fatalf("non-source created entries: %+v", entries)
	}
}

func TestOutlineBoundsAndControlSanitizing(t *testing.T) {
	entries := TeX([]rune(strings.Repeat("\\section{title}\n", MaxEntries+10)))
	if len(entries) != MaxEntries {
		t.Fatalf("unbounded entries: %d", len(entries))
	}
	entries = TeX([]rune("\\section{" + strings.Repeat("界", 500) + "\x1b}\n"))
	if len(entries) != 1 || len([]rune(entries[0].Title)) > MaxTitleRunes || strings.Contains(entries[0].Title, "\x1b") {
		t.Fatalf("unbounded/unsafe title: %+v", entries)
	}
	for _, text := range []string{"\\section{", "\\section[broken", "\\verb", "\\begin{verbatim}\n\\section{x}", "\\section{%"} {
		TeX([]rune(text))
	}
}

func TestMarkdownOutlineSkipsFencedCode(t *testing.T) {
	source := "# One\n```tex\n## not a heading\n```\n### 世界 ###\n"
	entries := Markdown([]rune(source))
	if len(entries) != 2 || entries[0].Title != "One" || entries[1].Title != "世界" || entries[1].Level != 2 || entries[1].Line != 5 {
		t.Fatalf("outline = %+v", entries)
	}
}
