# zat — native PDF viewing in st-go

A zathura-style keyboard-driven TUI reusing vimtex's continuous PDF region.
The terminal owns Poppler, page geometry, cached pixels, and smooth scrolling;
zat is a small pure-Go protocol client. No PDF conversion or external viewer
process runs while viewing a document.
Text extraction and search use Poppler's built-in Unicode handling, not host
`iconv`/`gconv` modules; they remain usable with the standalone static terminal.

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
| Ctrl-B, PageUp | One screen up |
| Tab | Toggle the table of contents; j/k/arrows select, Enter/click jumps |
| Shift-Tab | Toggle extracted text for the current PDF page |
| Ctrl-Shift-C | Copy a mouse selection in the page-text pane (terminal shortcut) |
| Ctrl-D / Ctrl-U | Half a screen down/up |
| `gg`, Home | Beginning |
| `G`, End | End |
| `42G`, `42gg` | Page 42 |
| `50%` | Middle of the scroll range |
| `+`, `=`, `zi` / `-`, `zo` | Increase/decrease zoom by ten percentage points |
| `z0`, `s` | Fit width |
| `a` | Best-fit the current page |
| `/text`, `?text`, Enter | Case-insensitive search; highlight all matches, jump forward/backward |
| `n/N` | Next/previous **occurrence**, including on the same page; wraps |
| `ma`, `'a` | Set/go to session mark a (any single-character mark) |
| `''` | Return to the position before the last jump |
| `r`, `R` | Reload the PDF, preserving position |
| `o` | Start an `:open` prompt |
| `:` | Command prompt |
| Escape, Ctrl-C | Cancel prompt/count/prefix, or close the open pane |
| Ctrl-L | Repaint status |
| `q`, `:q` | Quit |

Counts work with scrolling/page/zoom-step keys, e.g. `5j`, `3J`, `2+`.
Mouse input over the PDF is handled directly by st-go. Normally zat uses a
one-row terminal header. Tab opens a left-hand TOC/navigation pane; Shift-Tab
opens selectable page text alongside the PDF. Press the same key again, or
Escape, to close a pane. In the text pane, j/k/arrows and PageUp/PageDown scroll
the extracted text; J/K still change PDF pages and refresh the text. Drag to
select and press Ctrl+Shift+C to copy using st-go's normal clipboard shortcut.
Application mouse reporting is disabled in text mode, and periodic PDF status
polls do not repaint its body or clear the selection.
Loading, metadata errors, and confirmed empty-page results are distinct: an
unqueried or still-loading PDF is never reported as lacking a text layer.

Commands: `:open path.pdf`, `:page 12` (or `:12`), `:zoom 150`, `:zoom width`,
`:zoom fit`, `:nohlsearch` (or `:noh`), `:reload`, `:help`, `:quit`. Paths can contain spaces, semicolons,
and quotes; `~/` is expanded. Relative paths use zat's working directory.
Prompts use the shared `tui/readline` component: arrows/Ctrl-B/F edit at the
cursor, Home/End or Ctrl-A/E move to the ends, Backspace/Delete remove text,
and Ctrl-U/K/W/Y provide kill/yank. History uses Up/Down or Ctrl-P/N.
The header displays the
topmost visible page, total pages, scroll percentage, fit-width zoom percentage,
and filename. It also reports loading and rendering errors. At one row the PDF
temporarily hides and returns when the window grows.

### Search shortcuts

Type `/`, enter a query, and press Enter. Matches are yellow; the current match
is orange. `n` advances in the search direction and `N` reverses it, including
between occurrences on the same page. `?` starts a backward search. An empty
`/` or `?` repeats the previous query. Search also highlights selectable page
text and scrolls its current occurrence into view. `:noh` clears highlights;
`n` can restore the last search. Only explicit searches repaint text highlights,
not the periodic status updates used while selecting/copying text.

- `/{alpha}` searches for **α**; `/{Gamma}` searches for **Γ**. All 24 Greek
  letter names and `varepsilon`, `vartheta`, `varpi`, `varrho`, `varsigma`, and
  `varphi` are accepted inside braces.
- `/caf\'e` searches for **café**; `\'{e}` is also accepted. Other accents include
  grave (`` \`a ``), circumflex (`\^o`), diaeresis (`\"u`), and tilde (`\~n`).
  Braced letter commands include `\v{c}`, `\c{c}`, `\u{g}`, `\H{o}`, `\r{a}`, and
  `\k{a}`. Input is normalized to NFC Unicode.
- Unknown names/commands stay literal. Escape braces as `\{alpha\}` to search
  for the literal `{alpha}`, or double a backslash to search for a backslash.

Queries are literal, not regular expressions, and are limited to 1024 UTF-8
bytes after expansion (4096 typed bytes). At most 4096 document occurrences are
retained; reaching that limit is reported in the status row.

## Scope

This is a usable first viewer, not full zathura feature parity. Image-only scans
need embedded searchable text. The TOC uses embedded PDF bookmarks, including nested and named
local destinations; PDFs without bookmarks show an empty-TOC message. Only the
extracted-text pane supports selection, not the rendered PDF surface. Extraction
does not perform OCR; scanned pages without a text layer show a message instead.
Text/TOC replies are bounded, with truncation reported in the status row.
Rotation, link following, annotations,
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
- `panels.go`: shared TOC navigation, bounded metadata replies, selectable text.
- `search.go`: Greek/accent shortcuts and selectable text-pane highlights.
- Shared implementation: `term/display_regions.go`, `display_regions.go`,
  `display_search.go`, `display_metadata.go`, `pdfmeta`, `pdf_search.go`,
  `pdf_cgo.go`, `third_party_wrapper/pdf_bridge.cpp`.

Run `make test st zat st-min`. Tests cover PTY navigation/cleanup, unsupported
terminals, input fragmentation, layout isolation, tiny-window restoration,
actual mixed-page PDF offsets, zoom, native text search, TOC destinations,
chunked page text, and terminal-cell selection that survives status updates.
`TestZatNativePageTextPTY` also drives the real client through the terminal's
parser and native worker without canned replies or X11. For local regression
documents, set `ST_TEST_ZAT_PDF` and `ST_TEST_ZAT_TEXT` (text expected on each
page) when running that test with the repository's native linker flags.
`TestNativePDFTextWithoutHostModules` disables external UTF-16 conversion modules
to cover minimal hosts. With an isolated X server, run
`DISPLAY=:N make test-zat-x11` to exercise the built binaries, Shift+Tab, mouse selection, and the
actual Ctrl+Shift+C clipboard transfer under the same missing-module condition.
It also types Greek/accent search shortcuts, checks occurrence navigation and
server-side highlight pixels, and verifies highlight clearing.
The optional PDF/text variables also select a sample for this X11 test. See
[PERFORMANCE.md](PERFORMANCE.md) for the renderer comparison and measurement plan.
