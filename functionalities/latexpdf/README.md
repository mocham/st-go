# `latexpdf`

`github.com/mocham/GoLS/latexpdf` renders a supported LaTeX/Markdown subset to
HTML/SVG and selectable-text PDFs. It needs no TeX installation, browser,
subprocess, or system fonts. Math uses vector paths; prose and CJK text remain
selectable. Fonts are embedded and optional faces load on first use.

## Supported features

- Headings, prose, inline/display math, colors, underline/strike, and Unicode.
- Xy-pic/tikzcd grids and arrows, labelled Dynkin diagrams, and a subset of TikZ
  nodes, paths, shapes, plots, and loops.
- Traditional and biblatex BBL data through `SupplementBibliography`.
- UTF-8 plus declared applemac, Windows-1252, and Latin-1 input encodings.

This is not a full TeX engine. Unsupported commands remain visible diagnostics;
unsupported drawing source is retained in generated metadata. Callers resolve
`\input`/`\include` because filesystem/archive lookup is application-specific.

## API

```go
renderer := latexpdf.NewRenderer()
pdf, err := renderer.RenderPDF(latexpdf.Document{
    Source: expandedSource,
    Title:  "Lecture notes",
})
```

`RenderPDFWithOptions` accepts page dimensions in millimetres and a `FontScale`.
`RenderPDFResult` variants also return diagnostics and section/page mappings.
`RenderHTML` / `RenderPreparedHTML` provide browser output.

Renderers can be reused and cache formulas/documents in memory. Persistent
storage belongs to the caller; `PDFCacheKey` supports external caches.
See [embedded font notices](pdf_assets/README.md).

```sh
GOWORK=off go -C functionalities/latexpdf test ./...
```
