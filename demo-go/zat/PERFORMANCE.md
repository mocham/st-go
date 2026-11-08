# Rendering design and zathura comparison

Reviewed 2026-11-08 against upstream:

- [`zathura-pdf-poppler` render.c](https://github.com/pwmt/zathura-pdf-poppler/blob/165f37248f235bb192d154b06094c52ca92f5b2d/zathura-pdf-poppler/render.c)
  and [page.c](https://github.com/pwmt/zathura-pdf-poppler/blob/165f37248f235bb192d154b06094c52ca92f5b2d/zathura-pdf-poppler/page.c).
- [zathura render.c](https://github.com/pwmt/zathura/blob/6889036e71c76fa49b0cebc074ecf85991e995d1/zathura/render.c).

## What zathura actually optimizes

The Poppler plugin keeps `PopplerPage` objects and delegates to
`poppler_page_render` (or its printing variant). Scheduling and raster caching
mostly belong to zathura's core, not the plugin:

- One render worker, separate from GTK input/painting; renderer locking around
  plugin calls.
- Lazy page loading, with full-page Cairo surfaces at the requested resolution.
- Duplicate outstanding requests suppressed per generation; stale jobs/results
  discarded, and recent-view requests prioritized.
- A configurable page-count cache with least-recently-viewed eviction.
- Cached surfaces reused for display, so scrolling does not require a new
  Poppler render every animation frame.
- Specialized recoloring paths, with the simpler path selected when its color
  and alpha assumptions hold.

## What zat reuses

Zat delegates to st-go's continuous viewer. Poppler uses a retained native
Splash engine here, whereas zathura-pdf-poppler uses the GLib/Cairo
path. Sharing Poppler does not guarantee identical raster speed or output.
The minimal build omits fontconfig; embedded Liberation substitutes supply the
standard Latin Helvetica/Times/Courier families, including bold/italic variants.
They are registered once through sealed, close-on-exec memory-backed files and
stay alive for Poppler's process-wide font map. No system font lookup or disk
extraction is needed for these substitutes.

- One native-document-owning decoder worker, retaining its Splash font/render
  engine across slices. Decoding never holds the terminal or preview-state mutex.
- Separate preview-state mutex, private pixel buffer, painter, X11 child window,
  GC and backing pixmap. The child shares the parent's X connection/event loop.
  Frame preparation and uploads run outside the terminal mutex; only layout and
  shutdown synchronize with outstanding preview uploads.
- One replaceable pending request; stale-generation results cannot overwrite
  the current view. Document revision is separate from viewport generation,
  so resize/zoom retain the parsed PDF. Explicit reload opens a new revision.
- Up to five screenfuls of pixel rows, bounded by `limits.PreviewCacheBytes`
  (256 MiB per strip). Cache-budget-aware prefetch margins leave enough space
  after recentering to avoid repeated decoding on small movements.
- Strip replacement copies overlapping rows and renders only missing spans.
  Open/zoom/jump publishes visible pixels first, then fills surrounding rows.
- Native rendering requests only the page slices intersecting that strip.
  Page offsets use actual CropBox dimensions, with binary search for the first
  intersecting page. No document-sized raster allocation.
- A minimum 16 ms interval between frame preparations; input bursts coalesce
  to the latest view. Cached scrolling copies rows, including at zoomed/panned
  widths, without per-pixel resampling. The X backing pixmap shifts existing
  pixels; only newly exposed rows are uploaded. Expose events reuse the last
  committed pixmap, including during synchronized output.
- Status queries run at 4 Hz. Background prefetch does not report loading while
  the visible area is cached.
- UTF-8 text search uses the same native worker and checks cancellation between
  pages. It is page-wise and does not retain a whole-document text index.

The 256 MiB strip budget is **not a process RSS ceiling**: displayed and newly
rendered strips can coexist, as can the painter's immutable snapshot, terminal
and preview framebuffers, X pixmap, encoded PDF, and Poppler's internal
allocations. Storage grows on demand, rather than reserving 256 MiB on open.
Other limits (64 MiB input, 16,384 pages, bounded total
pixel height) are centralized in `limits/limits.go`.

## Tradeoffs and next measurements

Strips avoid allocating huge full-page surfaces at high zoom and make retained
pixel storage independent of document length. A full-page LRU can be faster
when revisiting distant pages: st-go retains the current surrounding strip and
reuses its overlap, rather than retaining an LRU of previously visited pages.
Complex vector pages can
still cost substantial interpretation time even for a clipped render.
At high zoom, horizontal panning uses the full zoomed strip width; exceeding
the cache budget produces a reported render failure instead of an unbounded
allocation.

No comparative performance claim is made yet. Benchmark both viewers with the
same Poppler version, effective pixel scale, viewport, and antialias settings:

1. Text-heavy, vector/math-heavy, scanned/image-heavy, mixed-size, and long PDFs.
2. Cold open, first visible page, first changed scroll frame, p50/p95 frame
   intervals, fast sustained scrolling, reverse scrolling, large page jumps,
   resize/zoom latency, and peak RSS.
3. Compare warmed and cold runs separately, including cache misses.

Then consider a byte-budgeted page/tile LRU and cancellation inside native page
rendering (currently stale generations are checked around jobs, and search is
cancelled between pages). Measure those changes separately
from the TUI. Blindly adding render threads is not the baseline used by zathura
and would require revisiting native-document ownership and memory budgets.

## Regression checks

`TestPreviewCachePressureDoesNotChurn` checks warmed twelve-step scrolling at
1920×1080/200%, 2560×1440/100%, and 3840×2130/100–200%: no strip job is needed.
`TestContinuousPDFOverlapReuse` verifies that a 20-row cache move renders only
20 new rows and preserves overlapping pixels. Painter tests exercise navigation
during a blocked upload, terminal-lock independence, frame pacing/coalescing,
and pixel equivalence between pixmap scrolling and a full redraw.
