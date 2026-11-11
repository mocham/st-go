# zat — PDF viewer

A keyboard-driven PDF viewer with smooth scrolling, search, outlines, and
selectable page text. The TUI is pure Go; the host owns decoding and rendering.

```sh
make st zat
./BINARIES/st -e ./BINARIES/zat document.pdf
./BINARIES/st -e ./BINARIES/zat -page 12 -zoom 120 -bg '#ffffff' document.pdf
```

Pages are one-based. Zoom is 10–500% of fit-width; default paper is warm off-white.
Native rendering retains Poppler documents and nearby pixel strips, so cached
scrolling does not repeatedly decode pages. Web hosts use PDF.js.

## Keys

| Keys | Action |
| --- | --- |
| `j/k`, arrows, wheel | Scroll; Left/Right pan at larger zoom |
| `J/K` | Next/previous page |
| Space, Ctrl-F/B, PageDown/Up | Screen down/up |
| Ctrl-D/U | Half screen down/up |
| `gg/G`, Home/End | Beginning/end |
| `42G`, `42gg`, `50%` | Page or percentage jump |
| `+`/`-`, `zi`/`zo` | Zoom |
| `z0`, `s` / `a` | Fit width / best-fit page |
| Tab | Toggle clickable outline |
| Shift+Tab | Toggle selectable page text |
| Ctrl+Shift+C | Copy text selection through the terminal |
| `/`, `?`, `n/N` | Search forward/backward, next/previous occurrence |
| `ma`, `'a`, `''` | Set/jump to mark, return before last jump |
| `r`, `o`, `:` | Reload, open prompt, command prompt |
| Escape / Ctrl-C | Cancel input or close a pane |
| `q` | Quit |

Counts work with scrolling/page/zoom keys. Commands include `:open PATH`,
`:page N`, `:zoom N|width|fit`, `:noh`, `:reload`, `:help`, and `:quit`.
Prompts support shared [readline editing](../../functionalities/tui/readline/README.md).

### Search shortcuts

Search is case-insensitive literal Unicode, with yellow matches and an orange
current occurrence. `/{alpha}` finds **α**, `/{Gamma}` finds **Γ**, and
`/caf\'e` finds **café**. TeX-style accents and Greek variants are supported.
Empty searches repeat the previous query; `:noh` clears highlights.

## Limits

Search needs embedded text; there is no OCR. Native PDF selection uses the
extracted-text pane. Rotation, annotations, passwords, two-page layouts,
persistent bookmarks, and automatic reload are unsupported. Use `r` after an
external update. Outlines/search results are bounded to 4096 entries/occurrences.

Native standard Latin font substitutes are embedded. Specialized symbol/CJK
fonts should be embedded in the document for reliable native rendering.

## Tests

```sh
GOWORK=off go -C demo-go/zat test ./...
make test-zat-x11          # isolated X server required
```
