# TeX preview engine benchmark and profile report

Date: 2026-11-06

This report evaluates the current `latexpdf` TeX preview pipeline against
`github.com/go-tex/engine v0.270.0` on production documents. It also profiles a
minimal `latexpdf` font-registration correction. The measured operation is
complete source-to-vector-PDF generation at the file browser's representative
preview-pane size.

## Conclusions

1. The unmodified `latexpdf` pipeline takes 9.604 s for the corpus. go-tex takes
   6.693 s and is 1.435x faster.
2. This difference is primarily an fpdf font-registration problem, not an
   engine or MathJax problem. `latexpdf` registers twelve UTF-8 font faces for
   every PDF, including the same 5.15 MiB CJK font four times. fpdf parses and
   subsets every registered face, including unused faces.
3. A minimal lazy-registration prototype takes 5.447 s, a 43.3% reduction from
   current `latexpdf` and 18.6% less time than go-tex. It reduces allocation from
   16.337 GB to 7.779 GB while preserving all 2,804 `latexpdf` pages.
4. Font embedding remains mandatory. The prototype embeds every used font and
   does not use PDF Base-14 or system fonts. go-tex also embeds its text font.
5. The engines are not output-equivalent: go-tex produces 1,004 pages instead
   of 2,804 and 158.1 MB instead of 57.3-59.2 MB. Timing does not establish
   equivalent fidelity.

On this corpus, speed is not a reason to replace `latexpdf` after correcting its
font setup. The recommended path is to retain `latexpdf`, apply lazy embedded
font registration, and then address cache retention and PDF stream compression.

Implementation status: after this evaluation, lazy embedded mono/CJK
registration was made the default in the local `latexpdf` module. The
configuration names and commit identifier below describe the benchmark inputs,
not the post-report working tree.

## Configurations under test

| Name | Implementation |
|---|---|
| Current | Local `latexpdf` at `bb9cf8e07ff5d68fbce54c57ee571dfb3fbeb97f` using fpdf v0.12.0 |
| Lazy fonts | Temporary copy of the same source with only mono/CJK registration changed |
| go-tex | `github.com/go-tex/engine v0.270.0`, PDF output through go-pdfkit v0.20.0 |

The lazy-font prototype makes only these behavioral changes:

- Keep the four real Liberation Serif faces embedded as before.
- Do not register mono until monospace text is encountered.
- Do not register the CJK fallback until a CJK rune is encountered.
- Register mono and CJK once under their real regular style, rather than
  registering identical bytes under regular, bold, italic, and bold-italic.
- Reset the requested style to regular for those two regular-only faces. The
  current four style registrations all point to the same bytes and do not
  provide real bold or italic glyphs.

The prototype was built and tested in a temporary workspace; production
repository files were not modified during engine evaluation.

## Workload

- Corpus: a locally supplied TeX corpus (`$CORPUS_ROOT` below)
- Entry documents: 55 `.tex` files whose comment-stripped source contains
  `\documentclass`
- Main-source bytes: 993,458
- Source after local include expansion: 1,169,502 bytes
- Documents changed by expansion: 11
- Page size: 102.39375 x 114.3 mm
- Margins: left/right 21 mm, top 20 mm, bottom 18 mm

The page size represents a 43 x 24 cell pane with 9 x 18 pixel cells at 96 dpi:
387 x 432 pixels, or 102.39375 x 114.3 mm. `latexpdf` receives those dimensions
through `PDFOptions`. The harness injects an equivalent `\geometry` command into
go-tex source because its public `Options` does not expose paper dimensions.

Both engines receive the same recursively expanded source. Expansion is needed
because `latexpdf.Document` requires expanded source and because go-tex's
multipass detector scans only the source supplied to it. Without expansion, 11
documents hide bibliography markers in included literature-review fragments and
incorrectly avoid go-tex's auxiliary path.

The current file-browser TeX path does not yet perform this include expansion.
That is a production correctness gap independent of the engine comparison.

## Benchmark method

### Harness behavior

Each corpus run is a fresh process. Within a run:

1. Discover and sort entry documents.
2. Construct one reusable `latexpdf.Renderer` for the corpus.
3. Read and recursively expand each document's local `\input`/`\include` tree.
4. For `latexpdf`, call `RenderHTML` and then `RenderPDFWithOptions`. The explicit
   HTML call uses the same key as the PDF pipeline, so the internal call is a
   cache hit; HTML is rendered once, not twice.
5. For go-tex, change to the entry document's directory and call
   `CompileToPDFDiag` with `Lenient: true` and `NoProgressLimitHeavy`.
6. Keep the PDF in memory, count pages, record output bytes and diagnostics, and
   sample `runtime.MemStats` around the timed per-document operation.

Corpus wall time includes source reads, include expansion, compilation, layout,
and in-memory PDF finalization. It excludes entry discovery, JSON report writes,
terminal PDF decoding, page rasterization, framebuffer painting, and filesystem
writes of generated PDFs.

### Host and run control

- CPU: AMD Ryzen 7 H 255, 8 cores / 16 threads
- OS: Linux 6.12.38+deb13-amd64, x86-64
- Go: 1.27.2
- `GOGC=100`; default `GOMAXPROCS=16`
- No CPU pinning; frequency boost enabled
- Three unprofiled fresh-process runs per configuration
- Rotated order: current, lazy, go-tex, go-tex, current, lazy, lazy, go-tex,
  current
- Per-document summary values are medians of the three corresponding runs

An earlier sequential lazy-font pilot measured 7.881, 8.633, and 7.686 s. Its
5.06% coefficient of variation was not reproduced. The rotated series below has
0.19% variation, agrees with the later profile run, and is used for all final
comparisons. The pilot is treated as host-load-contaminated rather than mixed
into the result.

## Benchmark results

### Raw corpus runs

| Configuration | Run 1 | Run 2 | Run 3 | Median | CV |
|---|---:|---:|---:|---:|---:|
| Current latexpdf | 9.581 s | 9.604 s | 9.604 s | 9.604 s | 0.11% |
| Lazy embedded fonts | 5.447 s | 5.433 s | 5.458 s | 5.447 s | 0.19% |
| go-tex | 6.693 s | 6.693 s | 6.663 s | 6.693 s | 0.21% |

### Aggregate metrics

| Metric | Current latexpdf | Lazy fonts | go-tex |
|---|---:|---:|---:|
| Corpus wall, median | 9.604 s | 5.447 s | 6.693 s |
| Change from current | baseline | -43.3% | -30.3% |
| Median document | 99.7 ms | 28.4 ms | 59.7 ms |
| p90 document | 370.4 ms | 274.8 ms | 290.2 ms |
| Maximum document | 1.084 s | 0.996 s | 0.949 s |
| Bytes allocated | 16.337 GB | 7.779 GB | 6.722 GB |
| Allocation count | 44.277 M | 24.740 M | 53.133 M |
| PDF output | 59.171 MB | 57.303 MB | 158.128 MB |
| Pages | 2,804 | 2,804 | 1,004 |
| Successful documents | 55/55 | 55/55 | 55/55 |

The lazy prototype is 1.763x faster than current `latexpdf` and 1.229x faster
than go-tex in corpus wall time. It is faster than go-tex on 51 of 55 individual
documents. The median per-document lazy/go-tex ratio is 0.665.

go-tex allocates 13.6% fewer bytes than lazy `latexpdf`, but performs 114.8% more
individual allocations. Its PDFs are 2.76x larger than the lazy output. The
larger files may increase downstream decoder and painting work, which this
source-to-PDF benchmark does not measure.

## Finalization and bibliography semantics

go-tex's `compile` function performs an auxiliary pass when expanded source
contains labels, bibliography items, page references, a bibliography request,
TOC/list requests, or an index. It then performs the first rendered pass and up
to two additional rendered passes until cross-reference page numbers stabilize.
Thus its finalized path costs two to four complete engine passes. Twenty-seven
documents in this corpus enter that path, and all required internal passes are
included in the timings.

go-tex does not execute an external BibTeX program. It prefers an existing
`jobname.bbl`; without one, its `\bibliography` implementation reads `.bib` data
and implements a limited plain-style bibliography internally. This corpus uses
inline or included `thebibliography` blocks. Its only `.bbl` is empty and
unreferenced, so no external BibTeX phase is required for these 55 entries.

`latexpdf` is a semantic LaTeX-subset renderer rather than a TeX Live pipeline.
It has no external auxiliary or BibTeX pass; one render is its finalized output.
The measurements therefore include each engine's own complete production API,
not a claim that either one is byte- or layout-equivalent to
`latex -> bibtex -> latex -> latex`.

## CPU profiles

Profiles were collected in separate runs with Go CPU profiling enabled. They
are diagnostic only and are not used for benchmark timing. Concurrent GC makes
total sampled CPU exceed wall time.

| Configuration | Profile wall | CPU samples |
|---|---:|---:|
| Current latexpdf | 10.551 s | 12.35 s |
| Lazy fonts | 5.450 s | 6.36 s |
| go-tex | 9.150 s | 14.91 s |

### Current latexpdf: cumulative CPU

| Call path | CPU | Share of samples |
|---|---:|---:|
| `RenderPDFWithOptions` / `pdfRenderer.render` | 8.49 s | 68.7% |
| `fpdf.Output` / `Close` / `enddoc` | 3.95-3.96 s | 32.0% |
| `fpdf.putfonts` | 2.65 s | 21.5% |
| `fpdf.AddUTF8FontFromBytes` | 2.54 s | 20.6% |
| `utf8FontFile.GenerateCutFont` | 2.38 s | 19.3% |
| `RenderHTML` | 2.11 s | 17.1% |
| `latexRenderContext.renderMath` | 1.91 s | 15.5% |

Font parsing at registration and font subsetting at PDF finalization are both
major costs. The rows are cumulative call paths and must not be added together.

### Lazy-font latexpdf: cumulative CPU

| Call path | CPU | Share of samples |
|---|---:|---:|
| `RenderPDFWithOptions` / `pdfRenderer.render` | 3.50-3.51 s | 55.0-55.2% |
| `RenderHTML` | 1.90 s | 29.9% |
| `latexRenderContext.renderMath` | 1.67 s | 26.3% |
| `fpdf.Output` / `Close` / `enddoc` | 1.46 s | 23.0% |
| `fpdf.putpages` | 1.24 s | 19.5% |
| fpdf page-stream compression | 1.13 s | 17.8% |
| MathJax `RenderWithOptions` | 0.95 s | 14.9% |
| `fpdf.AddUTF8FontFromBytes` | 0.29 s | 4.6% |
| `fpdf.putfonts` | 0.22 s | 3.5% |
| `utf8FontFile.GenerateCutFont` | 0.15 s | 2.4% |

After correcting registration, font work is no longer dominant. PDF page-stream
compression, HTML/math rendering, and layout are the next CPU targets.

### go-tex: cumulative CPU

| Call path | CPU | Share of samples |
|---|---:|---:|
| `CompileToPDFDiag` | 9.33 s | 62.6% |
| TeX `compile` | 5.63 s | 37.8% |
| engine main loop | 5.15 s | 34.5% |
| GC background marking | 4.37 s | 29.3% |
| `RenderPDF` | 3.69 s | 24.8% |
| PDF box/list drawing | 2.95 s | 19.8% |
| math SVG-to-PDF path drawing | 2.59 s | 17.4% |

go-tex spends meaningful CPU in both repeated TeX expansion/layout passes and
PDF math-path generation. Its high allocation count also produces substantial
concurrent GC work.

## Allocation profiles

`runtime.MemStats` totals in the benchmark table are authoritative for complete
allocation volume. The sampled profiles below identify sources of that volume.

### Largest flat allocation sites

| Configuration | Site | Flat allocation | Profile share |
|---|---|---:|---:|
| Current | fpdf `generateSCCSDictionaries` | 3.69 GB | 23.8% |
| Current | fpdf `unpackUint16Array` | 2.35 GB | 15.2% |
| Current | flate block generation | 1.31 GB | 8.5% |
| Current | fpdf `parseHMTXTable` | 0.65 GB | 4.2% flat, 19.4% cumulative |
| Lazy | flate block generation | 1.04 GB | 14.3% |
| Lazy | flate writer creation | 0.81 GB | 11.2% |
| Lazy | byte-buffer growth | 0.76 GB | 10.5% |
| Lazy | string-builder growth | 0.51 GB | 7.0% |
| Lazy | MathJax ordered-map cloning | 0.45 GB | 6.2% |
| Lazy | fpdf `generateSCCSDictionaries` | 0.22 GB | 3.1% |
| go-tex | string-builder writes | 1.28 GB | 20.3% |
| go-tex | byte-buffer growth | 1.03 GB | 16.3% |
| go-tex | OpenType contour construction | 0.44 GB | 6.9% |
| go-tex | pdfkit numeric serialization | 0.27 GB | 4.3% |
| go-tex | OpenType Type 2 curve construction | 0.27 GB | 4.3% |

The lazy change removes the multi-gigabyte fpdf font-table and SCCS hot spots
from the top of the profile. It does not eliminate allocations from compression,
MathJax structures, HTML parsing, or vector-path caching.

### Retained heap after forced GC

The allocation-profile run forces a GC and keeps the reusable renderer alive
before writing the profile.

| Configuration | Retained heap | Main retained state |
|---|---:|---|
| Current latexpdf | 397.2 MB | parsed formula paths, rendered HTML/math state, cached PDF bytes |
| Lazy fonts | 370.1 MB | same cache categories; font setup is mostly transient |
| go-tex | 4.0 MB | runtime and package initialization in this harness |

The latexpdf renderer deliberately caches formulas and complete PDFs across
documents. Approximately 200 MB of the retained profile is attributed directly
to parsed SVG path construction, and roughly one output-corpus worth of PDF data
is retained by the PDF cache. Lazy font registration solves transient work, not
cache growth. A bounded cache or explicit eviction policy is still needed for a
long-running browser process.

The go-tex comparison creates a fresh engine per document and does not retain
the generated output, so its low retained heap is expected. It is not evidence
that an application-level go-tex output cache would be free.

## Font-registration diagnosis

Current `latexpdf` registers these faces before laying out every document:

- Four distinct Liberation Serif faces: regular, bold, italic, bold-italic.
- Liberation Mono Regular under four style keys.
- Droid Sans Fallback Full under four style keys.

The CJK TTF is 5,398,328 bytes. The production corpus contains no CJK runes, but
the font is still parsed four times and emitted four times per PDF. fpdf's
`AddUTF8FontFromBytes` parses immediately; `putfonts` iterates all registered
fonts and calls `GenerateCutFont`, whether or not a face was selected.

### Isolated fpdf microbenchmark

The microbenchmark creates a one-page PDF containing representative Latin text,
uses `-benchtime=5x -count=3`, and reports the mean of three runs.

| Registered faces | Time/PDF | Allocated/PDF |
|---|---:|---:|
| Four serif faces | 8.85 ms | 20.26 MB |
| Four serif + one mono | 10.96 ms | 24.69 MB |
| Four serif + one mono + one CJK | 30.23 ms | 60.50 MB |
| Current twelve-face setup | 95.94 ms | 180.68 MB |

This reproduces the small-document latency floor and demonstrates that it comes
from registration, not document complexity. The full-corpus prototype removes
52.4% of allocated bytes and 44.1% of allocations relative to current
`latexpdf`.

The prototype still eagerly registers italic and bold-italic serif faces. Those
could also be loaded on first use, although regular and bold are normally needed
for body text and the generated title.

## Font-embedding verification

st-go does not search for system fonts when displaying a PDF, so all text fonts
must be embedded in the generated file. Lazy registration means "embed on first
use," not "delegate to the viewer."

A generated fixture was inspected with Poppler `pdffonts`. The latexpdf fixture
intentionally exercises serif, monospace, CJK, and vector math; the go-tex
fixture exercises ordinary text and math.

| Engine | PDF font entries | Embedded | Subset reported | ToUnicode |
|---|---|---|---|---|
| Lazy latexpdf | 4 serif + 1 mono + 1 CJK, CID TrueType | yes, all | no | yes, all |
| go-tex | `ZGEEPZ+STIXTwoMath-Regular`, CID Type 0C | yes | yes | yes |

No Base-14 or unembedded text-font entry was present. fpdf does call
`GenerateCutFont`, but its CID font names do not carry the standard six-letter
subset prefix, so `pdffonts` reports `sub=no`. Embedding itself is confirmed by
`emb=yes` and the PDF's `/FontFile2` streams. go-pdfkit uses a conventional
subset name and `/FontFile3` for its CFF font.

go-tex's PDF driver also rejects a render font that does not expose embeddable
bytes and shares one loaded subset per font program across pages. Its math is
drawn as vector paths. Both engines therefore satisfy the no-system-font
requirement in the tested path.

Production tests should assert that every text font has a `/FontFile2` or
`/FontFile3` descendant and should include Latin serif styles, mono, and CJK.

## Correctness and fidelity observations

- All 55 documents completed under all three configurations.
- Current and lazy `latexpdf` reported zero MathJax errors.
- Lazy registration preserved the current 2,804-page count exactly.
- go-tex reported no runaway expansion, open groups, page-cap hits, or runaway
  arguments.
- go-tex skipped `\DefineVerbatimEnvironment` once in each of 13 mechanism
  documents. It reported no other skipped command in this corpus.
- go-tex's 1,004 pages versus latexpdf's 2,804 pages show that layout and/or
  supported semantics differ substantially despite matching physical paper and
  margins.
- go-tex's in-process bibliography implementation is not a style-faithful
  substitute for external BibTeX or Biber.
- No visual, extracted-text, link-target, or page-by-page equivalence score was
  computed. A successful compile is not proof of a faithful preview.

The benchmark therefore answers a performance question for each engine's own
finalized output. It does not establish go-tex as a drop-in rendering-equivalent
replacement.

## Recommendations

1. Keep lazy embedded mono/CJK registration as the default and retain embedding,
   CJK, mono, MediaBox, and page-count regression coverage.
2. Consider lazy registration of serif italic and bold-italic faces.
3. Bound or evict `latexpdf` HTML, formula-path, and PDF caches; retained heap is
   now a larger production concern than font setup.
4. Profile fpdf page-stream compression next. After the font correction it is
   the largest clearly isolated PDF CPU/allocation cost.
5. Add recursive include expansion to the file-browser TeX path so production
   behavior matches `latexpdf.Document`'s contract and this benchmark workload.
6. Before reconsidering go-tex, run visual/text/link differential tests and an
   end-to-end st-go benchmark including PDF decode and framebuffer painting.

## Reproduction and artifacts

The evaluation harness and raw artifacts were temporary and are not included
in this repository. Paths below are placeholders for locally supplied inputs
and outputs. The harness source was `texbench/cmd/prodbench/main.go` relative to
its temporary workspace.

Representative unprofiled commands:

```sh
CORPUS_ROOT=/path/to/tex-corpus

GOGC=100 prodbench-pane \
  -engine latexpdf \
  -root "$CORPUS_ROOT" \
  -page-width 102.39375 -page-height 114.3 \
  -json tech-current-r1.json

GOGC=100 prodbench-fontopt \
  -engine latexpdf \
  -root "$CORPUS_ROOT" \
  -page-width 102.39375 -page-height 114.3 \
  -json tech-fontopt-r1.json

GOGC=100 prodbench-pane \
  -engine gotex \
  -root "$CORPUS_ROOT" \
  -page-width 102.39375 -page-height 114.3 \
  -json tech-gotex-r1.json
```

Profile collection adds `-cpuprofile FILE -allocprofile FILE`. Inspection uses:

```sh
go tool pprof -top -cum BINARY CPU.pprof
go tool pprof -top -alloc_space BINARY ALLOCS.pprof
go tool pprof -top -inuse_space BINARY ALLOCS.pprof
```

Raw JSON, logs, matching binaries, and profiles were retained in a separate
local results directory. Choose your own output directory when reproducing.

Binary SHA-256 values:

- Current/go-tex harness: `60afeffdb08c33dac29a3088b5d9830a05173dc457e3df877ea9b19689b9046a`
- Lazy-font harness: `08b82d47778a7d5b8500ce608501dedfda89c39d1efc48b1fe50635f3c7a7062`

## Appendix: per-document medians

`Pages L/G` is the latexpdf page count (identical for current and lazy) followed
by the go-tex page count. `Lazy/go-tex` below 1.00 means lazy `latexpdf` is faster.
Document identifiers are anonymized; source filenames and directory structure
are omitted.

| Document | Current (ms) | Lazy fonts (ms) | go-tex (ms) | Lazy/go-tex | Pages L/G |
|---|---:|---:|---:|---:|---:|
| `document-001` | 269.5 | 187.4 | 225.0 | 0.83 | 92/35 |
| `document-002` | 98.1 | 13.2 | 19.7 | 0.67 | 6/3 |
| `document-003` | 438.4 | 361.9 | 290.2 | 1.25 | 117/49 |
| `document-004` | 254.0 | 180.0 | 253.8 | 0.71 | 112/43 |
| `document-005` | 98.3 | 20.1 | 25.6 | 0.78 | 11/4 |
| `document-006` | 267.0 | 183.4 | 223.1 | 0.82 | 134/46 |
| `document-007` | 95.2 | 15.6 | 19.4 | 0.80 | 7/3 |
| `document-008` | 90.1 | 11.8 | 21.5 | 0.55 | 6/3 |
| `document-009` | 98.4 | 17.5 | 23.7 | 0.74 | 7/3 |
| `document-010` | 89.6 | 10.0 | 19.5 | 0.51 | 4/2 |
| `document-011` | 777.1 | 686.0 | 729.1 | 0.94 | 262/85 |
| `document-012` | 592.7 | 511.3 | 639.3 | 0.80 | 306/114 |
| `document-013` | 567.0 | 495.0 | 490.7 | 1.01 | 294/104 |
| `document-014` | 370.4 | 274.8 | 334.6 | 0.82 | 127/39 |
| `document-015` | 84.0 | 11.6 | 15.9 | 0.73 | 4/2 |
| `document-016` | 1083.9 | 995.8 | 948.6 | 1.05 | 418/137 |
| `document-017` | 128.6 | 51.3 | 59.7 | 0.86 | 61/26 |
| `document-018` | 134.4 | 53.6 | 81.3 | 0.66 | 37/16 |
| `document-019` | 86.0 | 11.9 | 18.4 | 0.65 | 5/2 |
| `document-020` | 87.8 | 11.4 | 17.1 | 0.67 | 4/2 |
| `document-021` | 85.4 | 11.8 | 19.3 | 0.61 | 8/4 |
| `document-022` | 87.2 | 11.3 | 17.9 | 0.63 | 5/3 |
| `document-023` | 82.5 | 8.9 | 18.5 | 0.48 | 5/3 |
| `document-024` | 84.5 | 14.5 | 20.4 | 0.71 | 6/3 |
| `document-025` | 82.1 | 10.1 | 19.0 | 0.53 | 5/2 |
| `document-026` | 87.1 | 15.9 | 22.2 | 0.72 | 7/3 |
| `document-027` | 169.2 | 96.7 | 168.2 | 0.57 | 54/23 |
| `document-028` | 138.4 | 67.7 | 132.8 | 0.51 | 43/18 |
| `document-029` | 163.5 | 85.2 | 165.5 | 0.51 | 61/25 |
| `document-030` | 84.9 | 12.4 | 22.1 | 0.56 | 9/3 |
| `document-031` | 119.4 | 43.3 | 73.3 | 0.59 | 21/7 |
| `document-032` | 84.6 | 10.7 | 17.2 | 0.62 | 3/1 |
| `document-033` | 99.5 | 23.2 | 31.5 | 0.74 | 8/4 |
| `document-034` | 113.8 | 39.5 | 38.7 | 1.02 | 10/5 |
| `document-035` | 80.0 | 11.0 | 19.2 | 0.57 | 4/2 |
| `document-036` | 84.9 | 11.3 | 19.8 | 0.57 | 3/2 |
| `document-037` | 93.1 | 23.5 | 32.4 | 0.72 | 12/6 |
| `document-038` | 176.3 | 101.9 | 145.6 | 0.70 | 47/13 |
| `document-039` | 100.7 | 28.7 | 67.8 | 0.42 | 24/7 |
| `document-040` | 132.3 | 65.3 | 106.5 | 0.61 | 41/12 |
| `document-041` | 99.7 | 29.8 | 69.9 | 0.43 | 24/8 |
| `document-042` | 103.1 | 28.4 | 67.8 | 0.42 | 21/6 |
| `document-043` | 108.0 | 37.2 | 84.1 | 0.44 | 33/9 |
| `document-044` | 110.6 | 33.4 | 75.8 | 0.44 | 26/8 |
| `document-045` | 126.6 | 56.2 | 97.8 | 0.58 | 38/11 |
| `document-046` | 99.3 | 24.7 | 64.5 | 0.38 | 25/7 |
| `document-047` | 104.3 | 29.1 | 39.7 | 0.73 | 18/8 |
| `document-048` | 168.0 | 91.6 | 137.7 | 0.67 | 51/15 |
| `document-049` | 96.7 | 23.4 | 60.5 | 0.39 | 23/7 |
| `document-050` | 85.6 | 14.2 | 19.8 | 0.72 | 5/2 |
| `document-051` | 148.8 | 77.1 | 120.5 | 0.64 | 62/24 |
| `document-052` | 83.5 | 11.0 | 16.2 | 0.68 | 7/3 |
| `document-053` | 192.7 | 120.6 | 155.9 | 0.77 | 61/24 |
| `document-054` | 86.2 | 16.0 | 22.8 | 0.70 | 6/3 |
| `document-055` | 85.3 | 15.1 | 26.3 | 0.57 | 14/5 |
