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
- JSON configuration (`config.json`)

## Requirements

- Go toolchain (with cgo)
- A C compiler (`gcc`)
- `curl` and `tar` (first build only, to fetch FreeType)
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
