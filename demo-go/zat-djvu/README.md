# zat-djvu — DjVu viewing in st-go

`zat-djvu` is the DjVuLibre-backed variant of [`zat`](../zat/README.md). It keeps
the same keyboard-oriented terminal UI, outline pane, selectable hidden-text
pane, literal Unicode search, marks, and command prompt. DjVu decoding belongs
to this client because st-go's native retained document renderer is PDF-only;
the resulting page raster is displayed through st-go's retained image region.

```sh
make st zat-djvu
make test-zat-djvu
./BINARIES/st -e ./BINARIES/zat-djvu document.djvu
./BINARIES/st -e ./BINARIES/zat-djvu -page 12 -zoom 120 document.djvu
```

`make zat-djvu` downloads the pinned DjVuLibre source, verifies its SHA-256,
and links DjVuLibre, libjpeg, libstdc++, pthread, libm, and libc statically. A
normal standalone `go test` uses the system `ddjvuapi.pc`; the Makefile test
target instead exercises the bundled static profile.

At runtime the viewer calls DjVuLibre directly. It does not execute `ddjvu`,
`djvutxt`, `djvused`, ImageMagick, or a desktop viewer. Bundled, single-page,
and indirect DjVu documents accepted by `ddjvu_document_create_by_filename_utf8`
are supported; an indirect document's referenced files must remain beside its
index.

## Keys

| Keys | Action |
| --- | --- |
| `j/k`, Down/Up, mouse wheel | Scroll; crossing an edge opens the adjacent page |
| `h/l`, Left/Right | Pan a zoomed page |
| `J/K` | Render next/previous DjVu page |
| Space, Ctrl-F, PageDown | One viewport down; then the next page |
| Ctrl-B, PageUp | One viewport up; then the previous page |
| Ctrl-D / Ctrl-U | Half a viewport down/up |
| `gg`, Home | First page |
| `G`, End | Last page |
| `42G`, `42gg` | Page 42 |
| `50%` | Page at 50% of the document |
| `+`, `=`, `zi` / `-`, `zo` | Change fit-width zoom by ten points |
| `z0`, `s` | Fit width |
| `a` | Fit page height |
| Tab | Toggle the DjVu outline; j/k select and Enter/click jumps |
| Shift-Tab | Toggle selectable hidden text for the current page |
| Ctrl-Shift-C | Copy a mouse selection in the hidden-text pane |
| `/text`, `?text`, Enter | Search hidden text forward/backward |
| `n/N` | Next/previous occurrence, wrapping across pages |
| `ma`, `'a`, `''` | Set/go to a mark; return to the previous jump |
| `r`, `R` | Reload from disk while preserving page and page position |
| `o`, `:` | Open prompt / command prompt |
| Escape, Ctrl-C | Cancel input or close a pane |
| `q`, `:q` | Quit |

Commands are `:open path.djvu`, `:page 12` (or `:12`), `:zoom 150`,
`:zoom width`, `:zoom fit`, `:nohlsearch`, `:reload`, `:help`, and `:quit`.
Paths can contain spaces, semicolons, quotes, and UTF-8; `~/` is expanded.

Search is case-insensitive Unicode literal search and supports zat's Greek and
accent shortcuts (for example, `/{alpha}` and `/caf\'e`). DjVu hidden text is
bounded per page and the document result is bounded to 4096 occurrences. Search
jumps the raster to the matching page and highlights occurrences in the
selectable text pane; it does not draw synthetic boxes over the page image.
Scans without a hidden text layer remain viewable but cannot be searched.

## DjVuLibre API flow

The native bridge deliberately follows
[zathura-djvu's API usage](https://github.com/pwmt/zathura-djvu):

1. Create one `ddjvu_context_t`, an RGBMASK32 top-to-bottom pixel format, and a
   document with `ddjvu_document_create_by_filename_utf8`.
2. Drain the required asynchronous message queue while waiting for document
   information and page-decoding jobs.
3. Create a page with `ddjvu_page_create_by_pageno`, wait for decoding, and call
   `ddjvu_page_render(DDJVU_RENDER_COLOR, ...)` at its native dimensions.
4. Obtain hidden text with `ddjvu_document_get_pagetext(..., "char")` and walk
   its bounded miniexp zone tree.
5. Obtain bookmarks with `ddjvu_document_get_outline`, recursively walk the
   `bookmarks` expression, and resolve both numeric links and component IDs via
   `ddjvu_document_get_fileinfo`.

All DjVuLibre calls are serialized on one worker so its context and message
queue have a single owner. New render/text/search requests coalesce before they
start, and an in-progress document search checks cancellation between pages.

## Raster and resource lifecycle

Rendered pixels are capped by st-go's decoded-image budget, encoded as a private
mode-0600 PNG in a mode-0700 temporary directory, and opened in the retained
image region at the final requested fit-width zoom. Only three frame generations
are retained to cover an in-flight terminal decode; the directory is removed on
exit. The previous committed terminal frame stays visible until its replacement
has decoded. Page changes therefore avoid an intermediate fit render while still
letting st-go own panning, scaling, damage, and presentation on X11, framebuffer,
and web backends.

The DjVu index itself is limited to 64 MiB, page rasters to the shared decoded
image budget, page text to 256 KiB, outlines to 4096 entries/1 MiB, and parser
control input to 8 KiB. Normal exit, EOF, and handled signals restore termios,
mouse reporting, wrapping, cursor state, the alternate screen, and the region
layout.

`-bg '#RRGGBB'` supplies DjVuLibre's output whitepoint and resulting page color;
the default is zat's warm off-white `#f7f5ef`.
