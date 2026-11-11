# Shared terminal frontend

The frontend manages PTYs, input encoding, layout, semantic scenes, native
raster rendering, and previews. See [architecture](../README.md),
[building](../BUILDING.md), and [terminal protocols](../stterminal/README.md).

## Usage

```sh
./BINARIES/st                           # start $SHELL on X11
./BINARIES/st -e bash
./BINARIES/st -dir /path/to/project
./BINARIES/st -f /path/to/font.ttf --ratio 1.3
./BINARIES/st -config /path/to/config.json
./BINARIES/st -install-terminfo
./BINARIES/st vim notes.md
```

The same options generally apply to `st-fb` and `st-web`; see their
[backend guides](../../platform_dependent/README.md) for launch requirements.
Put terminal options before `-e`.

## Lazy terminal tabs

Startup has one terminal and no tab bar. The first successful tab request creates
the bar and preserves the original terminal in tab 1. The Go file browser uses
new focused tabs for its built-in document tools when support is enabled.

- Click a tab to select it; wheel over the bar or Ctrl+Shift+PageUp/PageDown switches.
- Middle-click a tab to close it; Ctrl+Shift+W closes the selected tab.
- Background PTYs continue running; switching restores their committed content.
- Closing one tab leaves the others alive; closing the last exits the host.
- Tabs are limited to eight, with a 256 MiB aggregate raster estimate at spawn.

Labels have a fixed 20-cell width. Framebuffer tabs share the bottom area with
the system bar, moving above it on narrow screens. `-no-tabs` disables support;
`make TABS=0` omits it. Applications use the
[tab-spawn protocol](../stterminal/tab_spawn.go).

## Configuration

Lookup order: `-config FILE`, `st.json` beside the executable, `config.json`
beside it, then embedded defaults. Missing or invalid settings fall back to
embedded defaults. See [default configuration](../../functionalities/config/config.json).

Relative font paths resolve beside the selected configuration. Native font
loading keeps embedded Monaco as a fallback. Copying the binary requires no
companion configuration or font files.

`-install-terminfo` writes the built-in `st-256color` entry and `stterm-256color`
alias to `$TERMINFO` or `~/.terminfo`; `make install` uses `PREFIX/share/terminfo`.
No `tic` is required.

## Rendering and caching

Terminal text, tabs, and lists use semantic operations with explicit cell widths,
colors, and decorations. Browser Canvas and the native raster adapter consume
the same operations. The server remains authoritative for the terminal grid.
Caret updates are separate from glyph content, reducing redraw work.

Native previews use a background decoder with one replaceable pending request.
Stale results are discarded and the last successful preview remains visible.
Continuous PDFs retain their document across zoom/resize and cache up to five
screenfuls of pixels, reusing overlapping rows. X11 scrolling shifts retained
pixels and uploads only exposed rows; framebuffer presentation coalesces damage
on a 60 Hz software clock.

Web surfaces retain semantic state instead of terminal pixel buffers. Browser
image/PDF work runs independently of ordered text painting; paint acknowledgements
bound network backlog. See [web rendering](../../platform_dependent/web/README.md).

Full native builds cache decoded WebP frames in SQLite. `webp_cache_path` defaults
to `/tmp/st-go-webp-cache-{uid}/cache.sqlite3`; an empty value disables it.
Encoding and writes run asynchronously. The same configured database supports
opt-in [shared clipboard synchronization](../stterminal/clipboard_sync.go).

## Resource limits and error logs

| Resource | Budget |
| --- | --- |
| Preview input / encoded image or PDF | 64 MiB |
| Decoded native still image | 256 MiB |
| Native framebuffer, page bitmap, animation canvas, or image atlas | 64 MiB each |
| Native glyph cache | 32 MiB / 16,384 entries |
| Continuous native PDF strip | 256 MiB |
| Clipboard | 8 MiB |
| OSC/DCS message | 1 MiB |
| Assembled HTML document | 64 MiB |

These are per-resource limits, not a process memory ceiling. Oversized requests
are rejected. Diagnostics use a bounded private `/tmp/st-go-<pid>-errors-*.log`;
its path is printed to stderr. [Web media budgets](../../platform_dependent/web/README.md#ownership-and-protocol)
apply separately.

## Child environment

The host exports `ST_GO_EXECUTABLE`, `ST_GO_BACKEND=x11|fb|web`, and supported
protocol capabilities. Applications should use these rather than infer a backend
from `DISPLAY`. See the [file-browser guide](../../demo-go/file-browser/README.md)
for icons and backend-aware openers.
