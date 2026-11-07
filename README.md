# st-go — static terminal written in Go

**st-go** is a from-scratch Go implementation of the suckless [st](https://st.suckless.org/)
terminal emulator. The terminal core is a faithful port of `st`'s escape-sequence
parser and screen model, and the X11 frontend is built on the pure-Go
[`xgb`](https://github.com/BurntSushi/xgb) library.

## Features

- **Static binary, zero external dependencies.** The binary is fully statically
  linked — `ldd` reports *"not a dynamic executable"*. There is no runtime
  dependency on X11, fontconfig, Xft, or any shared library:
  - X is driven by **pure Go** (`xgb`) over the X protocol sockets, so no `libX11`
    is needed.
  - **FreeType2** is the only C library always linked and is built as a static
    archive into `third_party/` by the Makefile.
  - glibc is linked statically as well.
- **Built-in font for portability.** The small `Monaco_Linux.ttf` (59 KB) is
  embedded into the binary with `go:embed` and loaded by FreeType directly from
  memory (`FT_New_Memory_Face`). The terminal renders correctly even with no
  font file installed on the system. Use `-f /path/to/font.ttf` to override.
- **Self-contained build.** `third_party/` is gitignored; a fresh `make`
  downloads the needed source tarballs, extracts them, and builds the static
  archives automatically.
- **Tiered build sizes.** One source tree produces several binaries — from a
  minimal `st-min` (text only, ~8 MB) up to a full-featured `st` (~16.5 MB). See
  [Build targets](#build-targets).
- Full VT100/ANSI escape-sequence handling (cursor movement, editing, erase,
  scroll regions, SGR colors, alt-screen, DECSET/DECRST modes)
- 256-color palette + 24-bit truecolor
- Mouse reporting (X10, X11, SGR), word/line/rectangular selection
- Clipboard & PRIMARY selection with INCR transfer, bracketed paste
- XIM-free keyboard handling with a full Linux-console keymap
- Inline image display via a DCS display DSL (stb_image / libwebp decoders)
- PDF display via a minimal static poppler (C++ API, first page or `page N`)
- JSON configuration (`config.json`)

## Resource limits and error logs

Resource budgets are centralized in [`limits/limits.go`](limits/limits.go).
`MaxBytes` defaults to **64 MiB** per input/buffer; smaller budgets are derived
from it:

| Resource | Limit |
| --- | --- |
| Preview file / encoded image or PDF | 64 MiB |
| Decoded bitmap, framebuffer, image atlas, pending WebP cache pixels | 64 MiB each |
| Terminal screen grids (both buffers plus bookkeeping) | 64 MiB |
| Glyph cache | 32 MiB / 16,384 entries |
| Clipboard transfer | 8 MiB |
| OSC/DCS control string | 1 MiB |
| Animation frame count | 65,536 |

Oversized inputs are rejected before file loading or pixel decoding. Oversized
control strings are discarded through their terminator; oversized clipboard
transfers are drained without pasting partial content. A rejected runtime resize
retains the previous framebuffer/grid. Atlas reuse remains latency-oriented.

The first rejection creates `/tmp/st-go-<pid>-errors-<random>.log` and prints its
exact path to stderr. The private log is capped at **1 MiB** and wraps in place,
keeping recent diagnostics. These are per-resource limits; native decoder
working memory and overall process RSS are separate from the budgets.

## Inline images via the DCS display DSL

st-go can display images inside the terminal. A small, extensible DSL is
carried in an otherwise-unused escape code, the **DCS** (device control
string): `ESC P <statements> ESC \`. Each statement is `command args...`
terminated by `;`, and string arguments may be quoted.

Image decoding supports PNG, JPEG, GIF, BMP, TGA and WebP (via
[stb_image](https://github.com/nothings/stb) + [libwebp](https://chromium.googlesource.com/webm/libwebp)),
and PDF files (first page by default) via a minimal poppler build.

Display an image file at the current cursor position:

```sh
printf '\033Popen /path/to/image.png;\033\\'
```

The image is split into a grid of glyphs, one per terminal cell, with each cell
retaining its full pixel block. Without placement options it is written like
text, advancing the cursor and scrolling at the bottom, so it scrolls with the
buffer and can be overwritten by normal output.

With a fit option the image is scaled to the terminal (preserving aspect
ratio):

```sh
printf '\033Popen /path/to/image.png fit-width;\033\\'   # fit terminal width
printf '\033Popen /path/to/image.png fit-height;\033\\'  # clear screen, then fit height
printf '\033Popen /path/to/doc.pdf fit-height page 3;\033\\'  # PDF page 3
printf '\033Popen /path/to/image.png rect 1 3 48 20 fit-contain;\033\\'
```

`fit-height` clears the screen only in legacy cursor-placement mode. `rect`
uses one-based cell coordinates, clears only that rectangle, clips all output
to it, and leaves the cursor unchanged. `fit-contain` preserves aspect ratio
while fitting both rectangle dimensions.

Supported DSL commands:

| command | meaning |
|---------|---------|
| `open '<path>' [fit-width] [fit-height] [fit-contain] [page N]` | load and display an image/PDF |
| `open '<path>' rect X Y W H [fit-contain] [page N]` | paint text/image/PDF inside a fixed cell rectangle |
| `open '<path>'` (text file) | render a plain text file row-by-row from the cursor, stopping at the last row (no scroll) |
| `setpwd '<dir>'`           | set the working directory for relative paths |
| `clear`                    | clear the screen and remove all images |
| `delete <id>`              | remove a previously opened image |
| `window remember TAG`      | memorize the current terminal position and size |
| `window restore TAG`       | restore a memorized geometry |
| `window forget TAG`        | remove a memorized geometry |
| `window place ANCHOR X Y W H [restore TAG]` | move/resize and optionally restore on the next left click |

Unknown commands are ignored, so the DSL is forward-compatible. Plain text
files (non-image, non-PDF) are detected by content and rendered as text rows
starting at the current cursor position, stopping when the last screen row is
reached — so a mini file browser can show a file tree in one pane and a text
preview in another.

Window geometry values require a unit: pixels (`320px`), a screen ratio
(`0.25r`), or a percentage (`25%`). `absolute` treats X/Y as screen positions.
The other anchors are `top-left`, `top`, `top-right`, `right`, `bottom-right`,
`bottom`, `bottom-left`, and `left`; X/Y are offsets inward from that anchor.
Corner and boundary placement is resolved against the X screen. A window
placed with `restore TAG` stays mapped, consumes the next left-button
press/release, and restores the tagged geometry. These operations obey
`allowgeometryops`; the default config enables geometry while leaving the
broader OSC clipboard/window operations controlled by `allowwindowops` off.
At runtime st-go exports a random `ST_GO_GEOMETRY_TOKEN` capability to its
child. Child-emitted window commands use `window auth TOKEN ...`; commands
without the matching token are ignored. The file browser adds this prefix
automatically.

### Independent display regions

st-go can reserve a **full-height right-hand preview** beside the main terminal.
The main terminal grid and PTY both shrink to the left region's width. Clears,
scrolling, Vim's alternate screen, and cursor drawing stay inside that region;
the preview has its own retained pixels. Ordinary terminal updates do not
repaint the preview.

For example, run this inside st-go:

```sh
printf '\033P%s\033\\' 'region split 45%; region secondary open "/path/to/document.pdf"'
vim /path/to/document.tex
printf '\033P%s\033\\' 'region reset'
```

This displays an existing PDF; a live LaTeX editor demo can regenerate its PDF
and issue another `region secondary open` after each update. Decode work runs
on one background worker with one replaceable pending request. The previous
preview stays visible until its replacement succeeds; stale results are
discarded. Input/bitmap limits apply, and failures are logged under `/tmp/`.

| Command | Meaning |
| --- | --- |
| `region split 45%` | Reserve 45% of columns for the right preview |
| `region split 40` | Reserve 40 columns initially |
| `region layout main X Y W H secondary X Y W H` | Atomically set explicit cell rectangles |
| `region secondary open '<path>' [page N\|current] [fit-contain\|fit-width\|fit-height\|native]` | Replace the preview with a static image or PDF page |
| `region secondary open '<path>' fit-width continuous` | Display a continuous PDF with locally animated pixel scrolling |
| `region secondary clear` | Clear the preview, retaining the split |
| `region secondary scroll report\|pages` | Report navigation to the application, or use default PDF-page scrolling |
| `region secondary scroll N` / `region secondary seek N` | Scroll locally in continuous mode, or emit an application navigation event |
| `region secondary position N` | Set a continuous view's position in millionths of its scroll range (0–1000000) |
| `region query` | Reply on terminal input with `DCS region layout … ST` |
| `region reset` | Release the preview and restore the full-width PTY |

Rectangle X/Y coordinates and PDF page numbers are **one-based**. An explicit
layout must cover the entire cell grid: `main` at the left edge, `secondary`
immediately adjoining it at the right, both full-height. A single full-window
`main` rectangle is also accepted. Unsupported IDs, gaps, overlaps, and other
layouts are rejected without changing the active layout. For a 120×40 window:

```sh
printf '\033P%s\033\\' 'region layout main 1 1 70 40 secondary 71 1 50 40'
```

Window resizing preserves the requested width ratio, resizing the main PTY and
re-rendering the preview. At a one-column window, the preview is temporarily
hidden and returns when space is available. `fit-contain` is the default for
region images/PDFs; PDF `fit-width` fills the width and clips vertical overflow.
Adding `continuous` stacks all PDF pages and scrolls cached strips immediately,
independently of application refresh timing. A three-pixel divider occupies the
left edge of the preview pane.
By default the mouse wheel over the
preview changes PDF pages (wrapping at either end); preview clicks are consumed
locally. Keyboard input stays with the main terminal. The split persists across
terminal resets and alternate-screen changes until `region reset`; applications
should send that command during cleanup.

The optional Go interface is `term.RegionHooks` in
[`term/display_regions.go`](term/display_regions.go): stable `RegionID`s,
`DisplayRegion` rectangles (zero-based in Go), `SetRegions`, `Regions`,
`OpenRegion`, and `ClearRegion`. The first backend accepts only the two-region
side-by-side layout, while the interface leaves room for future layouts.

### Vim + live LaTeX demo

[`demo-go/vimtex`](demo-go/vimtex/README.md) launches real Vim after checking
that the source exists, watches saved changes (including local TeX includes),
and renders a continuous fit-width preview with a minimum 2.1-second save-refresh gap:

```sh
make st vimtex
./st -e ./vimtex /path/to/document.tex
```

The default Vim:preview width ratio is **4:6**. Set `-split VIM:PREVIEW`, for
example `./vimtex -split 1:1 document.tex`, to customize it.

The mouse wheel over the right pane scrolls smoothly through cached document
pixels without waiting for a LaTeX render. Preview padding is trimmed and unused
page-bottom whitespace is cropped. `:VimtexPreview [line]` provides an approximate
source-line jump; `:VimtexScroll count` scrolls by wheel steps from Vimscript.
Ordinary keyboard input stays with Vim.

## Requirements

- Go toolchain (with cgo)
- A C compiler (`gcc`) and C++ compiler (`g++`)
- `curl`, `tar` and `cmake`/`ninja` (first build only, to fetch & build libs)
- A static glibc (`/usr/lib/libc.a`)

## Build

```sh
make          # builds ./st (full: all feature libraries) — see targets below
make st-min   # minimal: text-only terminal
make test     # run the test suite (links the full feature set)
make install  # installs to $(BINDIR), default /usr/local/bin
make clean    # removes all built binaries
make distclean  # removes binaries and third_party/
```

All binary targets use Go's `-trimpath` to omit local source and module-cache
paths from Go debug information and stack traces.

Result:

```
$ file ./st
./st: ELF 64-bit LSB executable ... statically linked
$ ldd ./st
not a dynamic executable
```

## Build targets

The same Go source builds several binaries that differ only in which
third-party C libraries are linked. When a decoder is dropped, a **dummy
object** (a no-op stub) is linked in its place, so the affected feature does
nothing instead of crashing. The full build also enables its SQLite WebP cache
with a build tag; reduced builds omit SQLite entirely.

| target   | extra libraries        | image/PDF support          | size    |
|----------|------------------------|----------------------------|---------|
| `st-min` | (freetype only)        | none                       | ~8 MB   |
| `st-stb` | + stb_image            | PNG/JPEG/GIF/BMP/TGA       | ~8.6 MB |
| `st-pdf` | + stb_image + poppler  | raster images + PDF        | ~14.6 MB|
| `st`     | + stb_image + webp + poppler + SQLite | all of the above + WebP | ~16.5 MB |

### `st-min` — the minimal build

`make st-min` produces a terminal that links **only FreeType2** (for glyph
rendering) and nothing else. It is the smallest binary (~8 MB) and is the
right choice when you want a static terminal with no image or PDF features:

```sh
make st-min
./st-min            # full terminal, text only
```

Everything still works — VT100/ANSI handling, colors, mouse, selection,
clipboard, bracketed paste, the built-in font, JSON config. What is **not**
present:

- **No inline image decoding** (`stb_image`, `libwebp` stubs)
- **No PDF display** (poppler stub)

Because the image/PDF C symbols are satisfied by no-op dummies, trying to
`open` an image in `st-min` simply shows nothing (a clean no-op) rather than
erroring or crashing.

### The other targets

Beyond `st-min`, each target adds libraries on top of the previous one:

- **`st-stb`** — adds [stb_image](https://github.com/nothings/stb) for
  PNG/JPEG/GIF/BMP/TGA decoding. This enables the inline-image DSL for raster
  formats. WebP and PDF remain stubbed.
- **`st-pdf`** — adds a **minimal static poppler** built with only the C++
  API (`page_renderer` → raw BGRA; no cairo/glib/gobject/ffi/pixman/lcms/
  openjpeg/turbojpeg). Enables displaying PDFs (first page, or `page N`).
  WebP remains stubbed.
- **`st`** (default, `make`) — the full build: stb_image, libwebp, poppler, and
  the SQLite WebP cache, so every format the DSL supports is decoded.

The per-target third-party libraries are passed to the linker via
`-extldflags` (they are deliberately not in the cgo files' `#cgo LDFLAGS`), and
the stub objects live in `third_party_wrapper/dummy-{stb,webp,pdf}.c`. See
`CAVEAT.md` for the link-order traps.

## Usage

```sh
./st                            # start a terminal with $SHELL
./st -e bash                    # start bash
./st -f DejaVuSansMono.ttf      # override the embedded font
./st --ratio 1.3                # scale the glyph geometry by 1.3
./st -v                         # print version (st 0.9.2)
./st -install-terminfo          # generate/install ~/.terminfo/s/st-256color
./st -config /path/config.json  # alternate config file
./st -t auto-gtex -e bash       # title/class/instance for WM tiling
./st vim notes.md               # gvim-like terminal Vim window
./st vim -c 'set number' notes.md
```

`st-go` generates the compiled `st-256color` entry directly in Go; it does not
require `tic` or a system terminfo development package. `make install` writes
the entry and its `stterm-256color` alias beneath
`$(PREFIX)/share/terminfo`. For a per-user install, run
`./st -install-terminfo`; `$TERMINFO` overrides the default `~/.terminfo`
destination.

Decoded WebP images and animation frames are cached as PNG blobs in a private
SQLite database at `/tmp/st-go-webp-cache-<uid>/cache.sqlite3`. Override the
location, or disable the cache with an empty path, in JSON. `{uid}` in a
configured path expands to the current numeric user ID. The cache directory
must be owned by the current user and not writable by other users.
PNG encoding and SQLite writes run on a bounded background worker so cache
population does not block terminal rendering or input.
Animated WebP metadata is read without rasterizing frames; frames are decoded
and queued for caching individually only as playback requests them.

```json
{
  "webp_cache_path": "/path/to/st-go-webp-cache.sqlite3"
}
```

`st vim [vim-options] <file>` treats the final argument as the file and passes
the preceding arguments to Vim. It starts Vim with the file's directory as the
child working directory, passes the basename after `--`, sets the initial
terminal title to `emulated-vim`, and sizes the window to the screen except for
a bottom strip reserved for the collapsed graphical file browser.

## Demo

`demo/image-viewer.sh` is a pure-bash image/PDF viewer that uses the DCS DSL
(arrow keys navigate files, Up/Down flips PDF pages when viewing a PDF, `q` or
Esc quits):

```sh
./st -e ./demo/image-viewer.sh          # view images in the current directory
./st -e ./demo/image-viewer.sh -d /path/to/images
./st -e ./demo/image-viewer.sh -p 3 /path/to/docs   # start PDFs at page 3
```

`demo/file-browser.sh` is a graphical, pane-based file browser. The right pane
contains file metadata and a scrollable directory list; the left pane previews
text, images, and PDFs using the display DSL. It runs in the alternate screen
and enables SGR mouse reporting:

```sh
./st -e ./demo/file-browser.sh           # browse the current directory
./st -e ./demo/file-browser.sh /some/path
./st -e ./demo/file-browser.sh --hidden /some/path
```

Single-clicking or using the arrow keys selects an entry and updates its info
and preview. Double-clicking opens a file (or enters a directory), and the mouse
wheel scrolls the list. Over a PDF preview, the wheel changes pages. Keyboard
controls include arrows, Page Up/Down, Enter, Backspace, `[`/`]` for PDF pages,
`.` for hidden files, `r` to refresh, `/` to enter a path, `:` to run a shell
command (or `:s/old/new/` to rename, `:help` for the manual), and `q` to quit.

The path prompt accepts absolute paths and paths relative to the current browser
directory, including `file`, `./file`, and `../../file`. Backspace can remove
the initial `/`; `Ctrl+A`/`Ctrl+E` move to the beginning/end. Live matching
shows a popup menu, standard `*`, `?`, and bracket wildcards are expanded, and
Up/Down selects a match before Enter activates it. Escape, `Ctrl+C`, or any
mouse click cancels path entry.

The `:` key opens a shell command prompt built on the same modal input loop as
the `/` path prompt. The selected file is exported as `$F` and the browsed
directory as `$D`, and the command runs with the browsed directory as its
working directory, so `:vim $F`, `:xdg-open $F`, or `:ls -l` all work. While a
command runs the browser leaves the alternate screen and restores it
afterward.

A command beginning with `:s/` is a rename instead of a shell command. Vim-style
`:s/old/new/` replaces the first occurrence of `old` in the name of every
matching entry, and `:s/old/new/g` replaces every occurrence:

```sh
:s/txt/md/       # report.txt -> report.md, notes.txt -> notes.md
:s/-/X/g         # a-b-c.txt -> aXbXc.txt
```

As the pattern is typed, every entry whose name would change is painted in a
highlight color in the list so the change can be previewed before Enter commits
it. Entries whose target already exists are skipped, and the parent entry is
never renamed.

`:help` opens a detailed user manual in the preview pane. It is navigated like
a PDF — `]` next page, `[` previous page, and the mouse wheel flips pages —
and `q` or Escape closes it.

File, directory, symlink, image, PDF, text, archive, audio, video, source,
configuration, and executable icons have built-in Unicode defaults. Override
them in a partial terminal config:

```json
{
  "file_browser": {
    "icons": {
      "directory": "D",
      "pdf": "P",
      "code": "C"
    }
  }
}
```

The resolved values are exported to the browser. A one-shot
`ST_FILE_BROWSER_ICON_PDF=X` environment variable takes precedence over JSON.

The external opener is shell code rather than a command-plus-one-argument. Set
it with `--open CODE` or `ST_FILE_BROWSER_OPEN`; `FILE`, `NAME`, and
`BROWSER_DIR` are exported to `bash -c`, so dispatch functions and command
sequences are supported:

By default, text, markup, configuration, shell, and source-code extensions are
opened through the exact st-go executable that launched the browser:
`$ST_GO_EXECUTABLE vim "$FILE"`. Other formats continue to use `xdg-open`.

```sh
./st -e ./demo/file-browser.sh --open '
  open_file() {
    case $FILE in
      *.pdf) zathura "$FILE" ;;
      *.txt) xterm -e less "$FILE" ;;
      *)     xdg-open "$FILE" ;;
    esac
  }
  open_file
' /some/path
```

Text, image, PDF, and directory selections redraw only the fixed preview
rectangle, metadata, and changed list rows. Double-clicking a file remembers
the full geometry, opens the configured command, and collapses the terminal to
a mapped bottom-left strip; clicking that strip restores the saved geometry.

> **tmux note:** the image DSL rides the DCS escape code, which tmux reserves
> for its own protocol and does not forward from a pane to the outer terminal.
> `image-viewer.sh` therefore does not display images inside tmux (a tmux
> limitation, not a st-go bug).

### Go file browser

`demo-go/file-browser` is the native Go variant. Build and run it with:

```sh
make file-browser
./st -e ./file-browser /some/path
```

Its navigation sidebar is visible and floating by default, so previews use the
full terminal width and the sidebar is painted on top. Tab shows or hides the
sidebar. Shift+Tab toggles floating and pinned-right mode without repainting the
current preview; the next selected file or page uses the new geometry. TeX and
Markdown previews share the native `latexpdf` vector-PDF pipeline. Markdown
supports inline `$...$`/`\(...\)` and display `$$...$$`/`\[...\]` math. HTML,
XHTML, and EPUB previews pass sanitized semantic markup to the separate native
`semhtml` PDF renderer; EPUB files first resolve their package spine. This path
preserves headings, paragraphs, lists, inline emphasis, code, links, table text,
and image alt text while deliberately ignoring CSS and publication fonts.

### Go corpus browser

`demo-go/gols` combines the Go file browser's terminal UI with the immutable
SQLite archive and native LaTeX rendering used by GoLS. It opens corpus
databases directly in read-only, immutable mode—there is no HTTP server—and
navigates **database → corpus → documents → PDF/TeX/Markdown contents**.
Opening a TeX document expands archived `\input`/`\include` records. Markdown
documents, including inline and display LaTeX math, use the same shared
`latexpdf` vector-PDF pipeline; other archived text is displayed as source. The
final level is an outline whose entries jump to their exact rendered PDF pages.

```sh
make gols
./st -e ./gols /path/to/lectures.sqlite3
./st -e ./gols /path/to/corpus-directory   # recursively finds corpus DBs
```

The navigation pane is focused and visible at startup. Arrow keys and Enter
move down the hierarchy, and Backspace or Left moves up. Opening a document
automatically hides the pane for a full-width preview; Tab shows/focuses it and
Tab again hides it. `/` searches document paths/titles/content,
`[` and `]` change rendered document pages, and `q` exits. PDFs are generated only
when a document is opened. The selected page-size variant remains pinned while
the window is resized; reopening at another size creates or reuses that cached
variant. `GOLS_DATABASES` supplies a colon-separated default database list when
no command-line path is given.

## License

See `LICENSE`.
This repository does not ship any third-party binaries or library sources.
The Makefile downloads and builds third-party libraries on demand into the
gitignored `third_party/` directory; they are governed by their own licenses
(see `NOTICE`). If you build and distribute a binary, the combined work's
license is the most restrictive of the linked libraries.
