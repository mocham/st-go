# Building and packaging st-go

Run the commands below from the repository root. See the [documentation
index](README.md) for architecture, usage, protocols, and backend guides.

## Requirements

- Go toolchain with cgo
- A C compiler (`gcc`) and C++ compiler (`g++`)
- `curl`, `tar`, and `cmake`/`ninja` for the initial library builds
- A static glibc (`/usr/lib/libc.a`)

## Build

```sh
make            # same as make all: every terminal, demo, and native helper
make st         # full X11 terminal: BINARIES/st
make st-min     # text-only X11 terminal
make st-web     # embedded HTTP server and browser client
make st-fb      # Linux virtual-console framebuffer terminal
make test       # full test suite with native libraries linked
make test-tui   # independent TUI/helper modules, without graphical libraries
make install    # installs to $(BINDIR), default /usr/local/bin
make clean      # removes built binaries
make distclean  # removes binaries and downloaded/built third_party/ files
```

Generated executables live in **`BINARIES/`**, which is gitignored. Targets such
as `make st`, `make govim`, and `make file-browser` keep their familiar names.
To make the demo tools available by name, including default file-browser openers:

```sh
export PATH="$PWD/BINARIES:$PATH"
```

The default backend is X11. `backend_web` selects the web backend and
`backend_fb` selects framebuffer; use only one backend tag at a time. Both use
the full media profile. The [source layout](README.md#layout) separates the
terminal engine and shared frontend from backend-owned event and painting code.

Make's terminal targets include `terminal_tabs` by default, but tabs initialize
only on the first successful application request, never at startup. Use
`make TABS=0 st st-web st-fb` to omit support, or `-no-tabs` / `-tabs=false` to
disable it at runtime. Direct Go builds must include `terminal_tabs` explicitly
to enable the feature. See [tab behavior and controls](frontend/README.md#lazy-terminal-tabs).

## Self-contained binaries and libraries

The binaries are statically linked. X11 uses pure-Go
[`xgb`](https://github.com/BurntSushi/xgb) over X protocol sockets, without
`libX11`, fontconfig, or Xft. FreeType2 is the only C library always linked;
the full builds also link native media decoders. glibc is linked statically.

The embedded `Monaco_Linux.ttf` is loaded directly from memory through
FreeType's `FT_New_Memory_Face`; no installed font is needed. Use
`-f /path/to/font.ttf` to override it. See [configuration](frontend/README.md#configuration)
for fallback behavior.

`third_party/` is gitignored. A fresh `make` downloads the required source
tarballs and builds their static archives automatically. Existing archives
are reused across Makefile edits; application binaries still rebuild when their
build rules change. After deliberately changing a library version or build
settings, force that archive target with `make -B path/to/archive.a`, or use
`make distclean` before a full rebuild.

All binary targets use Go's `-trimpath` to omit local source and module-cache
paths from debug information and stack traces. For example:

```text
$ file ./BINARIES/st
./BINARIES/st: ELF 64-bit LSB executable ... statically linked
$ ldd ./BINARIES/st
not a dynamic executable
```

## Build targets

The X11 targets use the same Go sources and differ in their linked native
libraries. A dropped decoder is replaced by a no-op dummy object, so unsupported
formats do nothing instead of crashing. The full build enables the SQLite WebP
cache with a build tag; reduced builds omit SQLite entirely.

| Target | Extra libraries | Image/PDF support | Approximate size |
| --- | --- | --- | --- |
| `st-min` | FreeType only | None | 8 MB |
| `st-stb` | + stb_image | PNG/JPEG/GIF/BMP/TGA | 8.6 MB |
| `st-pdf` | + stb_image + Poppler | Raster images + PDF | 14.6 MB |
| `st` | + stb_image + WebP + Poppler + SQLite | All of the above + WebP | 16.5 MB |

`st-web` embeds its HTML, JavaScript, CSS, and fonts; no npm build, CDN, or
separate asset directory is required. `st-fb` uses a pure-Go Linux fbdev/evdev
driver. Neither backend links XGB; both retain the shared native font/media
libraries.

### Minimal terminal

```sh
make st-min
./BINARIES/st-min
```

VT100/ANSI handling, colors, mouse reporting, selection, clipboard, bracketed
paste, the embedded font, and JSON configuration remain available. There is no
inline image or PDF decoding; display requests for these formats are no-ops.

### Other media profiles

- **`st-stb`** adds [stb_image](https://github.com/nothings/stb) for raster images;
  WebP and PDF remain stubbed.
- **`st-pdf`** adds a minimal static Poppler with the C++ API and Splash renderer,
  plus FreeType, zlib, libpng, and libjpeg-turbo. The libjpeg API decodes embedded
  JPEG (`DCTDecode`) images, including scanned covers/pages. NASM optionally
  enables x86 SIMD when building libjpeg-turbo. PDF page selection is supported;
  WebP remains stubbed.
- **`st`** is the full default build, including libwebp and the SQLite WebP cache.

Per-target libraries are passed through `-extldflags`, deliberately not through
the cgo files' `#cgo LDFLAGS`. Stub objects come from
`third_party_wrapper/dummy-{stb,webp,pdf}.c`. See
[build and linking caveats](../CAVEAT.md#3-build--packaging-caveats).

## Verification

Every demo is a separate module, and shared modules live under
[`functionalities/`](../functionalities/README.md). The workspace supports
cross-module editing, but each module also works with `GOWORK=off`:

```sh
GOWORK=off go -C demo-go/govim build -o ../../BINARIES/govim .
GOWORK=off go -C demo-go/file-browser test ./...
go -C functionalities/tui test ./...
make test-tui
```

Govim, zat, and vimtex build without cgo. `zat-djvu` deliberately uses cgo; its
pinned DjVuLibre source is downloaded, checksum-verified, and statically built
against the bundled static libjpeg. The decoder remains in that client rather
than the terminal. File-browser and GoLS need a C compiler
for bundled SQLite, **not** graphical codecs or ALSA. File-browser sends audio
requests to st-go, whose audio module owns libasound and MP3 playback. The optional
Pinyin helper keeps its native decoder isolated. Distribution binaries remain
static, including terminal-owned audio with `alsa_static` and `zat-djvu`.

Go's `./...` does not cross module boundaries. The root pattern tests only the
host; `make test` additionally tests every shared/demo module, including the
previously separate latexpdf suite. `make test-tui` uses standard standalone Go
commands and excludes the native host audio module.

```sh
go -C platform_neutral/api test ./...
go -C platform_neutral/stterminal test ./...
make test
```

`make test` is the authoritative integrated suite: bare `go test ./...` lacks
the native static link objects. Static-glibc linker warnings are expected;
undefined image/PDF/WebP symbols are not. Backend-specific checks are documented
with the [backends](../platform_dependent/README.md).

## License

See [LICENSE](../LICENSE). This repository does not ship downloaded third-party
binaries or library sources. The Makefile builds those libraries on demand under
the gitignored `third_party/` directory, and their own licenses apply. A
distributed binary's combined work is subject to the most restrictive linked-library
license. The [Pinyin helper guide](../demo-go/pinyind/README.md) also documents
its bundled source/data notices and redistribution obligations.
