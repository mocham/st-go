# Building and packaging

Run commands from the repository root. Executables are written to `BINARIES/`.

## Requirements

- Go 1.26 or newer and Make.
- Native terminals and `zat-djvu`: cgo, C/C++ compilers, static glibc, `curl`,
  `tar`, and `cmake`. Make downloads and builds the required native libraries.
- `st-web` and the other Go applications build with `CGO_ENABLED=0`.

## Targets

| Command | Result |
| --- | --- |
| `make` | All terminals and applications |
| `make st` | Full X11 terminal |
| `make st-fb` | Full native renderer on Linux framebuffer |
| `make st-web` | CGO-free HTTP server and embedded browser client |
| `make file-browser govim` | Build named applications; see the [application index](../demo-go/README.md) |
| `make install` | Install `st`, `st.json`, and terminfo under `PREFIX` (default `/usr/local`) |
| `make clean` | Remove built binaries |
| `make distclean` | Also remove downloaded/built native libraries |

To make the applications available as file-browser openers:

```sh
export PATH="$PWD/BINARIES:$PATH"
```

Make builds include terminal tabs. Use `make TABS=0 st st-fb st-web` to omit
them, or `-no-tabs` to disable them at runtime. Direct Go builds must explicitly
select the `terminal_tabs` tag. X11 is the default backend; select only one of
`backend_web` and `backend_fb` for another backend.

## Standalone binaries

Make's binaries are statically linked and embed their default fonts/configuration.
X11 uses pure-Go XGB, without libX11, Xft, or fontconfig. Native terminals use
FreeType; full profiles add accelerated PNG/JPEG/WebP decoding, animated WebP,
and Poppler PDF rendering.

`st-web` embeds Canvas code, PDF.js, workers, fonts, and other browser assets.
It needs no native media libraries, npm build, CDN, or companion asset directory.
Images/PDFs render in the browser; TIFF/BMP conversion uses pure Go on the server.

All SQLite users use pure-Go `modernc.org/sqlite`. Full native terminals cache
decoded WebP frames; every backend supports opt-in shared clipboard storage.
`zat-djvu` statically links DjVuLibre in the client.

Native archives in `third_party/` are reused across Makefile edits. After changing
a library version/build setting, force its archive target or use `make distclean`.
Use Make for native linking: bare root Go build/test commands lack its link objects.

## Tests

```sh
make test                 # integrated native, web, and module tests
make test-tui             # standalone modules, without graphical libraries
make test-web             # CGO-free web and shared-clipboard checks
make test-web-browser     # Node >=22 and Chromium; no npm packages
make test-zat-djvu        # bundled static DjVuLibre profile
```

Set `CHROMIUM=/path/to/chromium` if needed. The module sweep includes `zat-djvu`
when system `ddjvuapi` is available; its dedicated target uses the bundled library.
Live X11 checks are available through `make test-webp-x11` and `make test-zat-x11`
with `DISPLAY` pointing to an isolated X server.

Go's `./...` does not cross module boundaries. Each module also works independently:

```sh
GOWORK=off go -C demo-go/govim test ./...
GOWORK=off go -C demo-go/file-browser build -o ../../BINARIES/file-browser .
```

## Licensing

See [LICENSE](../LICENSE). Native libraries, bundled
[PDF.js](../platform_dependent/web/assets/pdfjs/README.md),
[PDF fonts](../functionalities/latexpdf/pdf_assets/README.md), and
[Pinyin data](../functionalities/googlepinyin/assets/NOTICE) carry their own
licenses/notices. Distributed binaries must satisfy the licenses of their
linked libraries and embedded assets.
