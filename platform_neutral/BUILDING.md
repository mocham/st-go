# Building and packaging

Run commands from the repository root. Executables are written to `BINARIES/`.

## Requirements

- Go 1.26 or newer and GNU Make 4.3 or newer.
- Full native terminals (`st`, `st-fb`) and `zat-djvu`: cgo, C/C++ compilers, static glibc, `curl`,
  `tar`, and `cmake`. Make downloads and builds the required native libraries.
- `st-fb` also requires `pkg-config` and, on x86, NASM to build its bundled
  FFmpeg/x264 recording and playback components. These are build-time tools only.
- `st-min`, `st-fb-min`, `st-web`, and the other Go applications build with
  `CGO_ENABLED=0`. Minimal targets require no C compiler or `third_party/` assets.

## Targets

| Command | Result |
| --- | --- |
| `make` | All terminals and applications |
| `make st` | Full X11 terminal |
| `make st-min` | CGO-free minimal X11 terminal |
| `make st-fb` | Full native renderer on Linux framebuffer |
| `make st-fb-min` | CGO-free minimal Linux framebuffer terminal |
| `make st-web` | CGO-free HTTP server and embedded browser client |
| `make file-browser govim` | Build named applications; see the [application index](../demo-go/README.md) |
| `make install` | Install `st`, `st.json`, and terminfo under `PREFIX` (default `/usr/local`) |
| `make clean` | Remove built binaries |
| `make distclean` | Also remove downloaded/built native libraries |

To make the applications available as file-browser openers:

```sh
export PATH="$PWD/BINARIES:$PATH"
```

Make builds include terminal tabs. Use `make TABS=0 st st-min st-fb st-fb-min st-web` to omit
them, or `-no-tabs` to disable them at runtime. Direct Go builds must explicitly
select the `terminal_tabs` tag. The native input-helper bar remains available
when tab spawning is disabled. X11 is the default backend; select only one of
`backend_web` and `backend_fb` for another backend.

## Standalone binaries

Make's binaries are statically linked and embed their default fonts/configuration.
X11 uses pure-Go XGB, without libX11, Xft, or fontconfig. Full native profiles use
FreeType C, accelerated PNG/JPEG/WebP decoding, animated WebP,
and Poppler PDF rendering.

`st-fb` also embeds a restricted FFmpeg 8.1.3 build and pinned x264 source for
H.264/MP4 screen recording and [`gov`](../demo-go/gov/README.md) playback.
Make downloads checksum-verified sources into `third_party/` and links seven
archives: avformat, avfilter, avcodec, avutil, swscale, swresample, and x264. MP4/MOV demuxing,
H.264/HEVC/MPEG-4 video decoding, and AAC/MP3 audio decoding are enabled by the
tracked `third_party_wrapper/ffmpeg-configure.sh` build profile. The audio-only
filter subset (`abuffer`, `atempo`, `abuffersink`) provides pitch-preserving speed
changes; two atempo stages cover 0.25×–4×. External library
autodetection, networking, devices, other filters, and other
encoders are disabled. The Go bridge selects this implementation with
`ffmpeg_static`; `st-fb-min` continues to invoke external `ffmpeg` at runtime.

`st-min` and `st-fb-min` use the Go FreeType port and Go raster codecs, including
static WebP. PDF rendering and animated WebP use unsupported-operation stubs.
Font selection, cell placement, and color composition are shared with full builds.
The Go rasterizer accepts TrueType fonts and the first face of TrueType collections;
unsupported font overrides fall back to embedded Monaco. Hinting can differ slightly
between font engines. The framebuffer status atlas also regenerates without cgo.

The `purego` tag selects this implementation even when cgo is enabled (useful for
race tests). Disabling cgo selects it automatically for X11/framebuffer builds.

`st-web` embeds Canvas code, PDF.js, workers, fonts, and other browser assets.
It needs no native media libraries, npm build, CDN, or companion asset directory.
Images/PDFs render in the browser; TIFF/BMP conversion uses pure Go on the server.
The [web-service Compose image](../deploy/web/README.md) builds these CGO-free
binaries and runs st-web behind a TLS/authenticating nginx sibling.

All SQLite users use pure-Go `modernc.org/sqlite`. Full native terminals cache
decoded WebP frames; every backend supports opt-in shared clipboard storage.
`zat-djvu` statically links DjVuLibre in the client.

Native archives in `third_party/` are reused across Makefile edits. After changing
a library version/build setting, force its archive target or use `make distclean`.
Use Make for native linking: bare root Go build/test commands lack its link objects.
Minimal builds need no link flags, for example:

```sh
CGO_ENABLED=0 go build -tags 'purego terminal_tabs' -o BINARIES/st-min .
CGO_ENABLED=0 go build -tags 'purego backend_fb terminal_tabs' -o BINARIES/st-fb-min .
```

## Tests

```sh
make test                 # integrated native, web, and module tests
make test-tui             # standalone modules, without graphical libraries
make test-web             # web protocol, shared clipboard, service/PTY lifecycle
make test-web-browser     # direct + shared-service Chromium checks; Node >=22, no npm
make test-zat-djvu        # bundled static DjVuLibre profile
make test-fb-recording    # external and bundled-static framebuffer recording
make test-gov             # bundled MP4 playback and terminal/client lifecycle
go test -race ./platform_neutral/fontraster
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
