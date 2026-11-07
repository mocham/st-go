# `latexpdf`

`github.com/mocham/GoLS/latexpdf` is a self-contained Go module for rendering the LaTeX
document model used by GoLS. It requires no TeX installation, browser, runtime
font files, SVG converter, or subprocess. MathJax SVG outlines are written as
native PDF vector paths; positioned SVG text such as CJK inside mathematics
uses the embedded fallback font and remains selectable, as does prose. Scoped
`\color` and `\textcolor` declarations, including common `\definecolor` models,
are preserved in HTML and in native PDF text, formula fills, strokes, and CJK
formula text. `\strikethrough`, `\strikeout`, `\sout`, and Soul's `\st`
produce native HTML/PDF strike lines in prose and horizontal vector rules in
mathematics; `\underline` and `\uline` are native PDF decorations as well.

Standalone Xy-pic `\xymatrix` and `tikzcd` grids are laid out as native vector
diagrams, including labeled, equality, dashed, hooked, diagonal, and multi-cell
arrows, path offsets, and curved bypass arrows. Numbered equations containing a
`tikzcd` use the same renderer. Labelled A/B/C/D `\dynkin` diagrams are also
native vectors. The native TikZ subset covers positioned and relative nodes,
named styles, boxes, lines and arrowheads, dashed paths,
rectangles, circles, Bézier curves, grids, circular arcs with centered text
decorations, scoped shifts, numeric plots, and simple `\foreach` lists and
integer ranges. TikZ
commands outside that subset are counted in the SVG accessibility metadata and
the complete original source is retained in `data-tex` rather than silently
being represented as supported.

Common package-level math constructs are normalized before MathJax rendering,
including declared math alphabets, `\mathlarger`, graphicx scaling/rotation
wrappers, `\llbracket`, `\rrbracket`, `\dag`, math-mode footnotes, and legacy
`eqnarray` displays. TeX
primitive definitions remain deliberately unsupported and are not mistaken for
their standard command names. Unrecognized commands remain diagnostics but are
shown explicitly as red `\Command` literals in prose and vector mathematics;
they do not become opaque MathJax error boxes.

```go
renderer := latexpdf.NewRenderer()
pdf, err := renderer.RenderPDF(latexpdf.Document{
    Source:   expandedSource,
    Title:    "Lecture notes",
    Author:   "A. Author",
    Modified: modTime,
})
```

`RenderPDFWithOptions` optionally accepts a custom page size in millimetres.
Leaving either dimension zero preserves that dimension's A4 default:

```go
pdf, err := renderer.RenderPDFWithOptions(document, latexpdf.PDFOptions{
    PageWidth: 120, PageHeight: 90,
})
```

`RenderPDFResult` and `RenderPDFResultWithOptions` return the same PDF together
with MathJax diagnostics and section metadata mapped to final PDF page numbers.

The module also exposes `RenderHTML`, `RenderPreparedHTML`, `PreparePages`, and
`PDFCacheKey`. A renderer is safe to reuse across requests and keeps bounded-by-
use in-memory caches for parsed formulas and rendered documents. Persistent
storage is deliberately not part of this module; applications can use the
stable `PDFCacheKey` with their own cache backend.

Callers must expand `\input` and `\include` themselves because file/database
resolution is application-specific. All fonts needed by PDF output, including
the Apache-2.0 CJK fallback font, are embedded in this module. Optional mono and
CJK faces are registered and embedded on first use; PDF rendering never falls
back to viewer or system fonts. UTF-8 is used directly; legacy `applemac`,
Windows-1252, and Latin-1 sources declared through `inputenc` are decoded before
parsing. TeX text accents and common ligatures are emitted as Unicode text.

Run its independent test suite with:

```sh
cd latexpdf
go test ./...
```
