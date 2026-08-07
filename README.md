# st-go — static terminal written in Go

**st-go** is a from-scratch Go implementation of the suckless [st](https://st.suckless.org/)
terminal emulator. The terminal core is a faithful port of `st`'s escape-sequence
parser and screen model, and the X11 frontend is built on the pure-Go
[`xgb`](https://github.com/BurntSushi/xgb) library.

## Selling points

- **Static binary, zero external dependencies.** The binary is fully statically
  linked — `ldd` reports *"not a dynamic executable"*. There is no runtime
  dependency on X11, fontconfig, Xft, or any shared library:
  - X is driven by **pure Go** (`xgb`) over the X protocol sockets, so no `libX11`
    is needed.
  - **FreeType2** is the only third-party C library and is built as a static
    archive into `third_party/` by the Makefile.
  - glibc is linked statically as well.
- **Built-in font for portability.** The small `Monaco_Linux.ttf` (59 KB) is
  embedded into the binary with `go:embed` and loaded by FreeType directly from
  memory (`FT_New_Memory_Face`). The terminal renders correctly even with no
  font file installed on the system. Use `-f /path/to/font.ttf` to override.
- **Self-contained build.** `third_party/` is gitignored; a fresh `make`
  downloads the FreeType source tarball, extracts it, and builds the static
  archive automatically.

## Features

- Full VT100/ANSI escape-sequence handling (cursor movement, editing, erase,
  scroll regions, SGR colors, alt-screen, DECSET/DECRST modes)
- 256-color palette + 24-bit truecolor
- Mouse reporting (X10, X11, SGR), word/line/rectangular selection
- Clipboard & PRIMARY selection with INCR transfer, bracketed paste
- XIM-free keyboard handling with a full Linux-console keymap
- Inline image display via a DCS display DSL (stb_image decoder)
- JSON configuration (`config.json`)

## Inline images via the DCS display DSL

st-go can display images inside the terminal. A small, extensible DSL is
carried in an otherwise-unused escape code, the **DCS** (device control
string): `ESC P <statements> ESC \`. Each statement is `command args...`
terminated by `;`, and string arguments may be quoted. Image decoding uses
[stb_image](https://github.com/nothings/stb), precompiled into
`third_party/stb/stb_image.o` by the Makefile.

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
```

`fit-height` clears the screen before drawing so the whole image is visible.
Without a fit option the image keeps its native cell size.

Supported DSL commands:

| command | meaning |
|---------|---------|
| `open '<path>' [fit-width] [fit-height]` | load and display an image file |
| `clear`                    | clear the screen and remove all images |
| `delete <id>`              | remove a previously opened image |

Unknown commands are ignored, so the DSL is forward-compatible.

## Requirements

- Go toolchain (with cgo)
- A C compiler (`gcc`)
- `curl` and `tar` (first build only, to fetch FreeType + stb_image)
- A static glibc (`/usr/lib/libc.a`)

## Build

```sh
make          # builds ./st (fetches + builds FreeType into third_party/ if missing)
make install  # installs to $(BINDIR), default /usr/local/bin
make clean    # removes ./st
make distclean  # removes ./st and third_party/
```

Result:

```
$ file ./st
./st: ELF 64-bit LSB executable ... statically linked
$ ldd ./st
not a dynamic executable
```

## Usage

```sh
./st                            # start a terminal with $SHELL
./st -e bash                    # start bash
./st -f DejaVuSansMono.ttf      # override the embedded font
./st --ratio 1.3                # scale the glyph geometry by 1.3
./st -v                         # print version (st 0.9.2)
./st -config /path/config.json  # alternate config file
```

## License

See `LICENSE`. (Based on the st terminal which is MIT-licensed.)
