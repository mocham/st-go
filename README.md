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
  minimal `st-min` (text only, ~8 MB) up to a full-featured `st` (~15 MB). See
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

The image is split into a grid of glyphs — one per terminal cell — and placed
into the screen buffer exactly like text. It is **not resized**; each cell
shows the average color of the image pixels it covers. The image is written
row by row, advancing the cursor (and scrolling the screen when the bottom is
reached), just like text — so it scrolls with the buffer and is overwritten by
new text.

With a fit option the image is scaled to the terminal (preserving aspect
ratio):

```sh
printf '\033Popen /path/to/image.png fit-width;\033\\'   # fit terminal width
printf '\033Popen /path/to/image.png fit-height;\033\\'  # clear screen, then fit height
printf '\033Popen /path/to/doc.pdf fit-height page 3;\033\\'  # PDF page 3
```

`fit-height` clears the screen before drawing so the whole image is visible.
Without a fit option the image keeps its native cell size.

Supported DSL commands:

| command | meaning |
|---------|---------|
| `open '<path>' [fit-width] [fit-height] [page N]` | load and display an image/PDF |
| `open '<path>'` (text file) | render a plain text file row-by-row from the cursor, stopping at the last row (no scroll) |
| `setpwd '<dir>'`           | set the working directory for relative paths |
| `clear`                    | clear the screen and remove all images |
| `delete <id>`              | remove a previously opened image |

Unknown commands are ignored, so the DSL is forward-compatible. Plain text
files (non-image, non-PDF) are detected by content and rendered as text rows
starting at the current cursor position, stopping when the last screen row is
reached — so a mini file browser can show a file tree in one pane and a text
preview in another.

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

Result:

```
$ file ./st
./st: ELF 64-bit LSB executable ... statically linked
$ ldd ./st
not a dynamic executable
```

## Build targets

The same Go source builds several binaries that differ only in which
third-party C libraries are linked. When a library is dropped, a **dummy
object** (a no-op stub) is linked in its place, so the code compiles and runs
unconditionally — the affected feature just does nothing instead of crashing.
This is how `st-min` can omit image/PDF decoding without any `//go:build` tags.

| target   | extra libraries        | image/PDF support          | size    |
|----------|------------------------|----------------------------|---------|
| `st-min` | (freetype only)        | none                       | ~8 MB   |
| `st-stb` | + stb_image            | PNG/JPEG/GIF/BMP/TGA       | ~8.6 MB |
| `st-pdf` | + stb_image + poppler  | raster images + PDF        | ~14.6 MB|
| `st`     | + stb_image + webp + poppler | all of the above + WebP | ~15 MB |

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
- **`st`** (default, `make`) — the full build: stb_image **and** libwebp **and**
  poppler, so every format the DSL supports is decoded.

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
./st -config /path/config.json  # alternate config file
./st -t auto-gtex -e bash       # title/class/instance for WM tiling
```

## Demo

`demo/image-viewer.sh` is a pure-bash image/PDF viewer that uses the DCS DSL
(arrow keys navigate files, Up/Down flips PDF pages when viewing a PDF, `q` or
Esc quits):

```sh
./st -e ./demo/image-viewer.sh          # view images in the current directory
./st -e ./demo/image-viewer.sh -d /path/to/images
./st -e ./demo/image-viewer.sh -p 3 /path/to/docs   # start PDFs at page 3
```

`demo/file-browser.sh` is a mini file browser: the right panel lists the
current directory (`..` at the top, current entry highlighted), the left panel
previews the selected file (text rows, or image/PDF via fit-height):

```sh
./st -e ./demo/file-browser.sh           # browse the current directory
./st -e ./demo/file-browser.sh /some/path
```

Up/Down move the active entry, Right enters a directory (or previews a file),
Left goes up to the parent directory, `q`/Esc quits.

> **tmux note:** the image DSL rides the DCS escape code, which tmux reserves
> for its own protocol and does not forward from a pane to the outer terminal.
> `image-viewer.sh` therefore does not display images inside tmux (a tmux
> limitation, not a st-go bug).

## License

See `LICENSE`.
This repository does not ship any third-party binaries or library sources.
The Makefile downloads and builds third-party libraries on demand into the
gitignored `third_party/` directory; they are governed by their own licenses
(see `NOTICE`). If you build and distribute a binary, the combined work's
license is the most restrictive of the linked libraries.
