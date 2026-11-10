# Shared terminal frontend

This package owns PTY scheduling, keyboard/mouse escape encoding, software
glyph and image painting, retained previews, clipboard policy, and native media
integration. Backends own native resources and presentation; applications use
the [terminal protocols](../stterminal/README.md), not backend connections.
See the [architecture](../README.md) and [build guide](../BUILDING.md).

## Usage

Run these commands from the repository root:

```sh
./BINARIES/st                            # start $SHELL on X11
./BINARIES/st -e bash
./BINARIES/st -f DejaVuSansMono.ttf       # override the embedded font
./BINARIES/st --ratio 1.3                 # scale glyph geometry
./BINARIES/st -v                         # print version (st 0.9.2)
./BINARIES/st -install-terminfo           # install ~/.terminfo/s/st-256color
./BINARIES/st -config /path/config.json
./BINARIES/st -dir /path/to/project
./BINARIES/st -t auto-gtex -e bash        # X11 title/class/instance for WM tiling
./BINARIES/st vim notes.md                # terminal Vim window
./BINARIES/st vim -c 'set number' notes.md
```

The same frontend supports [X11](../../platform_dependent/x11/README.md),
[web browser](../../platform_dependent/web/README.md), and
[Linux framebuffer](../../platform_dependent/fb/README.md) hosts. Their startup
options, clipboard behavior, and native ownership restrictions are documented
with each backend.

## Lazy terminal tabs

st-go starts with one full-client PTY and **no tab bar**. Make builds include
the optional `terminal_tabs` build tag. `-no-tabs` or `-tabs=false` disables
requests and removes the advertised capability; `make TABS=0 st st-web st-fb`
omits the implementation entirely. Switching `TABS` rebuilds existing binaries.

The Go file browser's default govim/zat openers request a **detached background
tab**. The first successful request creates a bottom tab bar, keeps the original
terminal in tab 1, and appends the new PTY as the last tab without stealing focus.
The framebuffer's top-right system bar is unchanged. Failed exec leaves the
original layout intact. Once initialized, the tab bar stays even with one tab.

- Click a tab to select it; wheel over the bar or **Ctrl+Shift+PageUp/PageDown**
  switches tabs. Middle-click a tab or **Ctrl+Shift+W** closes it.
- Background PTYs keep draining output and answering protocols, but do not
  receive keyboard/paste/mouse input or present pixels. Each retains its own
  images/PDFs and last committed synchronized-output frame.
- Child EOF, exit or crash closes only its tab. A surviving tab becomes active;
  closing tab 1 does not exit st-go while another tab remains. There is no
  automatic restart. Closing the last tab restores native display/input state.
  Terminal-owned temporary file copies are retained on abnormal exit for
  recovery rather than being deleted underneath an editor's journal.
- Admission is limited to eight tabs and a 256 MiB aggregate terminal-raster
  estimate at spawn time. Existing per-resource codec/cache limits still apply;
  this is not a process-wide RSS limit.

Requests and acknowledgements use the authenticated
[tab-spawn protocol](../stterminal/README.md#terminal-tab-spawn), shared by all
three backends. No client-owned native window/display connection is involved.

`st vim [vim-options] <file>` treats the final argument as the file and passes
preceding arguments to Vim. It uses the file's directory as the child working
directory, passes its basename after `--`, and sets the initial title to
`emulated-vim`. On X11, it sizes the window to the screen except for the bottom
strip reserved for the collapsed graphical file browser.

## Configuration

Lookup order is `-config <file>`, `st.json` beside the executable (the name
written by `make install`), legacy `config.json` beside it, then embedded
defaults. Copying `st` into `~/.local/bin` needs no companion files. Missing,
malformed, or unusable configuration falls back to embedded defaults with a
diagnostic; it must not prevent startup or leave black text on black.

Relative font paths are resolved beside the selected configuration. A missing
or corrupt custom font, or missing glyphs in it, falls back to embedded Monaco
before optional system fallback faces. See the [default configuration](../../functionalities/config/config.json).

The terminal generates compiled `st-256color` terminfo directly in Go; neither
`tic` nor a system terminfo development package is required. `make install`
writes the entry and its `stterm-256color` alias under
`$(PREFIX)/share/terminfo`. For a per-user install, run
`./BINARIES/st -install-terminfo`; `$TERMINFO` overrides `~/.terminfo`.

## Rendering and caching

Glyph/image content stays separate from the terminal text caret. The frontend
prepares a small caret tile and clean underlay through `api.TextCursorPainter`;
cursor movement, hiding, shape and focus changes do not dirty glyph cells.
Backends restore/recompose only the affected caret rectangles. X11 and browsers
retain native mouse pointers; software mouse/touchpad pointer and system-bar
painting belong exclusively to the framebuffer backend.

Painter uploads and retained-surface updates transfer pixels/state to a backend;
they do not require immediate physical presentation. The framebuffer backend's
[presentation worker](../../platform_dependent/fb/README.md#rendering-and-input)
coalesces damage and composes a finished back buffer before paced scanout copies.
Optional `api.PaintBatcher` groups a terminal paint's uploads and caret into one
commit, so the presenter cannot sample a half-updated terminal frame.

The frontend decodes PNG, JPEG, GIF, BMP, and TGA through stb_image, WebP through
libwebp, and PDF through static Poppler/Splash, including embedded JPEG images.
Availability depends on the [build profile](../BUILDING.md#build-targets).
Media is placed through the terminal's DCS display and region protocols.

Retained region decoding runs on one background worker with one replaceable
pending request. Previous preview pixels remain visible until a replacement
succeeds; stale results are discarded. Preview state has its own mutex, and a
dedicated painter uses a private framebuffer with a minimum 16 ms frame interval.
The native rendering engine is retained across continuous PDF zoom/resize and
reuses overlapping strip pixels. On X11, panes have child drawables and cached
scrolling shifts a retained pixmap, uploading only newly exposed rows.

Decoded WebP images and animation frames are cached as PNG blobs in a private
SQLite database at `/tmp/st-go-webp-cache-<uid>/cache.sqlite3`. Configure another
path, or an empty path to disable the cache:

```json
{
  "webp_cache_path": "/path/to/st-go-webp-cache.sqlite3"
}
```

`{uid}` expands to the numeric user ID. The cache directory must belong to the
current user and must not be writable by others. PNG encoding and SQLite writes
run on a bounded background worker, so caching does not block rendering/input.
Animated WebP metadata is read without rasterizing frames; frames are decoded
and queued for caching only when playback requests them.

## Resource limits and error logs

Budgets are centralized in [`limits/limits.go`](../../functionalities/limits/limits.go).
`MaxBytes` defaults to **64 MiB** per input/buffer; decoded still images have a
separate **256 MiB** budget before preview scaling.

| Resource | Limit |
| --- | --- |
| Preview file / encoded image or PDF | 64 MiB |
| Decoded still image (JPEG/PNG/GIF/BMP/TGA/WebP) | 256 MiB |
| Framebuffer, PDF page bitmap, WebP animation canvas, image atlas, pending WebP cache pixels | 64 MiB each |
| Terminal screen grids (both buffers plus bookkeeping) | 64 MiB |
| Glyph cache | 32 MiB / 16,384 entries |
| Continuous PDF cache | 256 MiB per strip, up to five screenfuls |
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
working memory and overall process RSS are separate.

## Child environment

The host exports `ST_GO_EXECUTABLE`, the actual `ST_GO_BACKEND=x11|fb|web`, and
the runtime capabilities described in the [protocol reference](../stterminal/README.md).
Inherited backend identities are replaced rather than trusted. File-browser
icons come from terminal configuration, with application-specific environment
overrides; see the [browser guide](../../demo-go/file-browser/README.md).
