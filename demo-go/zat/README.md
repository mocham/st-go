# zat — native PDF viewing in st-go

A zathura-style keyboard-driven TUI reusing vimtex's continuous PDF region.
The terminal owns Poppler, page geometry, cached pixels, and smooth scrolling;
zat is a small pure-Go protocol client. No PDF conversion or external viewer
process runs while viewing a document.

```sh
make st zat
./st -e ./zat document.pdf
./st -e ./zat -page 12 -zoom 120 -bg '#ffffff' document.pdf
```

Requires the updated `st` or `st-pdf`, a readable PDF, and initially at least
two terminal rows. `-page` is one-based; `-zoom` is 10–500% **relative to
fit-width**, not physical DPI. Default paper/canvas is warm off-white
`#f7f5ef`; opaque page artwork can cover that paper color.
[Embedded Liberation fonts](https://codeberg.org/go-fonts/liberation) (SIL OFL 1.1)
supply substitutes for PDFs using non-embedded
Helvetica, Times, or Courier. Specialized symbol/CJK fonts should be embedded
in the PDF for complete glyph coverage in the minimal Poppler build.

## Keys

| Keys | Action |
| --- | --- |
| `j/k`, Down/Up, mouse wheel | Smooth vertical scrolling |
| `h/l`, Left/Right | Horizontal panning at larger zoom |
| `J/K` | Next/previous PDF page |
| Space, Ctrl-F, PageDown | One screen down |
| Ctrl-B, PageUp, Shift-Tab | One screen up |
| Ctrl-D / Ctrl-U | Half a screen down/up |
| `gg`, Home | Beginning |
| `G`, End | End |
| `42G`, `42gg` | Page 42 |
| `50%` | Middle of the scroll range |
| `+`, `=`, `zi` / `-`, `zo` | Increase/decrease zoom by ten percentage points |
| `z0`, `s` | Fit width |
| `a` | Best-fit the current page |
| `/text`, `?text`, Enter | Case-insensitive text search forward/backward |
| `n/N` | Next/previous matching **page**, wrapping at document ends |
| `ma`, `'a` | Set/go to session mark a (any single-character mark) |
| `''` | Return to the position before the last jump |
| `r`, `R` | Reload the PDF, preserving position |
| `o` | Start an `:open` prompt |
| `:` | Command prompt |
| Escape, Ctrl-C | Cancel prompt, count, or key prefix |
| Ctrl-L | Repaint status |
| `q`, `:q` | Quit |

Counts work with scrolling/page/zoom-step keys, e.g. `5j`, `3J`, `2+`.
Mouse input over the PDF is handled directly by st-go. Text input remains with
zat's one-row terminal header, isolated from the retained PDF surface.

Commands: `:open path.pdf`, `:page 12` (or `:12`), `:zoom 150`, `:zoom width`,
`:zoom fit`, `:reload`, `:help`, `:quit`. Paths can contain spaces, semicolons,
and quotes; `~/` is expanded. Relative paths use zat's working directory.
Backspace edits the prompt and Ctrl-U clears it. The header displays the
topmost visible page, total pages, scroll percentage, fit-width zoom percentage,
and filename. It also reports loading and rendering errors. At one row the PDF
temporarily hides and returns when the window grows.

## Scope

This is a usable first viewer, not full zathura feature parity. Search jumps to
the first occurrence on each matching page without highlighting; `n/N` advance
between pages, not occurrences on the same page. Image-only scans need embedded
searchable text. Rotation, outlines, link following, text selection, annotations,
password entry, two-page layouts, persistent bookmarks, and automatic file
watching are not implemented. Use `r` after an external update. The status row
uses ASCII-safe filename display; actual paths and search text support UTF-8.

On normal exit, EOF, or handled termination signals, zat resets the region,
restores the alternate screen, cursor/wrapping modes, and original termios.
Malformed/oversized input frames are bounded and incomplete frames expire.
Files are checked before entering raw mode; native PDF errors retain the prior
view and use st-go's bounded diagnostic log.

## Implementation and checks

- `main.go`: CLI and file checks.
- `run.go`: raw terminal lifecycle, region negotiation, polling and resize.
- `input.go`: fragmented UTF-8, keys, and framed terminal replies.
- `viewer.go`: bindings, commands, marks, and incremental header drawing.
- Shared implementation: `term/display_regions.go`, `display_regions.go`,
  `pdf_cgo.go`, `third_party_wrapper/pdf_bridge.cpp`.

Run `make test st zat st-min`. Tests cover PTY navigation/cleanup, unsupported
terminals, input fragmentation, layout isolation, tiny-window restoration,
actual mixed-page PDF offsets, zoom, and native text search. See
[PERFORMANCE.md](PERFORMANCE.md) for the renderer comparison and measurement plan.
