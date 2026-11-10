# Platform-neutral terminal engine and protocols

`stterminal` contains the Go port of suckless st's escape-sequence parser and
screen model. It handles ANSI/VT sequences, cursor movement, editing/erase,
scroll regions, SGR colors, alternate screens, DECSET/DECRST modes, selection,
animation, and damage tracking. Supported terminal behavior includes 256-color
and 24-bit color, X10/X11/SGR mouse reporting, word/line/rectangular selection,
and bracketed paste.

Applications communicate through terminal text: ANSI/VT, OSC, and the custom
DCS protocols below. The engine exposes backend-neutral hooks; it does not
import a display backend. The root `term/` package is a compatibility shim.
See the [architecture](../README.md), [shared frontend](../frontend/README.md),
and [backend guides](../../platform_dependent/README.md).

## DCS display DSL

A DCS frame is `ESC P <statements> ESC \`. Statements are `command args...`,
separated by `;`; string arguments may be quoted. Unknown commands are ignored.
Paths use the terminal's working directory, not the child shell's directory;
send `setpwd` when changing it. The display DSL's quoting is not shell escaping:
clients must not interpolate arbitrary filenames containing delimiters. The
file browser uses safe temporary symlinks for previews.

```sh
printf '\033Popen /path/to/image.png;\033\\'
printf '\033Popen /path/to/image.png fit-width;\033\\'
printf '\033Popen /path/to/image.png fit-height;\033\\'
printf '\033Popen /path/to/doc.pdf fit-height page 3;\033\\'
printf '\033Popen /path/to/image.png rect 1 3 48 20 fit-contain;\033\\'
```

Images are split into terminal-cell glyphs, each retaining its full pixel block.
Without placement options, output advances the cursor, scrolls at the bottom,
and can be overwritten by normal output. Fit options preserve aspect ratio.
`fit-height` clears the screen only in legacy cursor-placement mode. `rect`
clears and clips only its one-based cell rectangle, leaving the cursor unchanged.
`fit-contain` fits both rectangle dimensions.

| Command | Meaning |
| --- | --- |
| `open '<path>' [fit-width] [fit-height] [fit-contain] [page N]` | Display an image/PDF; PDFs default to their first page |
| `open '<path>' rect X Y W H [fit-contain] [page N]` | Paint text/image/PDF inside a fixed cell rectangle |
| `open '<path>'` (text file) | Render rows from the cursor, stopping at the last row without scrolling |
| `setpwd '<dir>'` | Set the working directory for relative paths |
| `clear` | Clear the screen and remove all images |
| `delete <id>` | Remove a previously opened image |
| `pdfsearch query TOKEN '<path>' '<text>'` | Start asynchronous search for fixed-page PDF previews |
| `pdfsearch chunk TOKEN OFFSET` | Request another bounded search-result chunk |
| `pdfsearch clear` | Cancel fixed-page search and discard retained matches |

Text/image/PDF detection uses file content. Image support includes PNG, JPEG,
GIF, BMP, TGA, and WebP, depending on the [build profile](../BUILDING.md#build-targets).
PDFs use the shared native Poppler renderer.

### Window geometry

Window requests require `window auth TOKEN ...`, using the runtime
`ST_GO_GEOMETRY_TOKEN` exported to the child. Requests without the matching
capability are ignored. The file browser supplies it automatically.

| Command after `window auth TOKEN` | Meaning |
| --- | --- |
| `remember TAG` | Memorize the current position and size |
| `restore TAG` | Restore memorized geometry |
| `forget TAG` | Remove memorized geometry |
| `place ANCHOR X Y W H [restore TAG]` | Move/resize, optionally restoring on the next left click |

Values require units: pixels (`320px`), screen ratios (`0.25r`), or percentages
(`25%`). `absolute` uses screen X/Y positions. Other anchors are `top-left`,
`top`, `top-right`, `right`, `bottom-right`, `bottom`, `bottom-left`, and `left`;
X/Y are inward offsets. On X11, placement resolves against the X screen. A
window placed with `restore TAG` stays mapped, consumes the next left-button
press/release, then restores the tagged geometry.

These requests obey `allowgeometryops`, enabled in the default configuration.
Broader OSC window operations use `allowwindowops`, disabled by default.
Web/framebuffer do not implement desktop window placement or collapse/restore.

### Independent display regions

The terminal can reserve a right-hand preview beside its main grid, or a
full-width preview below a fixed-height terminal header. The terminal grid and
PTY shrink to their region. Clears, scrolling, alternate screens, and cursor
drawing stay inside the main region; previews retain independent pixels.

Run inside st-go:

```sh
printf '\033P%s\033\\' 'region split 45%; region secondary open "/path/to/document.pdf"'
vim /path/to/document.tex
printf '\033P%s\033\\' 'region reset'
```

A client can regenerate its PDF and send another `region secondary open` after
each update. Decoding is asynchronous; the previous preview remains visible
until replacement succeeds. Input/bitmap [limits](../frontend/README.md#resource-limits-and-error-logs)
apply, and failures use the bounded diagnostic log.

| Command | Meaning |
| --- | --- |
| `region split 45%` | Reserve 45% of columns for the right preview |
| `region split 40` | Reserve 40 columns initially |
| `region stack 1` | Reserve one terminal row at the top, with a full-width preview below |
| `region layout main X Y W H secondary X Y W H` | Atomically set explicit cell rectangles |
| `region secondary open '<path>' [page N\|current] [fit-contain\|fit-width\|fit-height\|native]` | Replace the preview with a static image or PDF page |
| `region secondary open '<path>' fit-width continuous [background '#RRGGBB']` | Show a continuous PDF with local animated pixel scrolling and optional paper/canvas color |
| `region secondary clear` | Clear the preview, retaining the split |
| `region secondary list BASE64` | Overlay a retained, smoothly scrollable text list encoded as base64 JSON |
| `region secondary list restore` | Restore the previous image/PDF and navigation without changing layout |
| `region secondary scroll report\|pages` | Report navigation to the application or use default PDF-page scrolling |
| `region secondary scroll N` / `region secondary seek N` | Scroll locally in continuous mode or emit an application navigation event |
| `region secondary position N` | Set continuous scroll position in millionths of its range (0–1000000) |
| `region secondary page N` / `pages N` | Jump to a page / move by signed pages using actual page dimensions |
| `region secondary view N` | Animate by millionths of viewport height (500000 = half a screen) |
| `region secondary zoom N` | Set 10–500% of fit-width; 0 best-fits the current page |
| `region secondary pan N` | Pan horizontally by signed steps when zoomed in |
| `region secondary find forward\|backward\|next\|previous 'text'` | Case-insensitive PDF search with retained highlights; next/previous wraps by occurrence |
| `region secondary find clear` | Cancel pending search and clear highlights |
| `region secondary info` | Reply with `DCS region secondary status PAGE PAGES POSITION ZOOM LOADING FAILED ST` |
| `region secondary toc TOKEN OFFSET` | Request a bounded PDF outline metadata chunk |
| `region secondary text TOKEN PAGE OFFSET` | Request a bounded extracted-page-text chunk |
| `region query` | Reply on terminal input with `DCS region layout … ST` |
| `region reset` | Release the preview and restore the full-window PTY |

Here `DCS` means `ESC P`, and `ST` means `ESC \`. Rectangle coordinates and PDF
page numbers are **one-based**. An explicit layout must cover the entire cell
grid: `main` at the top left, adjoining `secondary` on the right (both full-height)
or below (both full-width). A full-window `main` alone is accepted. Unsupported
IDs, gaps, overlaps, and other layouts leave the current layout unchanged.
For a 120×40 window:

```sh
printf '\033P%s\033\\' 'region layout main 1 1 70 40 secondary 71 1 50 40'
```

Resizing preserves the requested side-by-side width ratio or stacked main-row
count, updates the PTY, and re-renders the preview. At one column/row respectively,
the preview hides temporarily and returns when space is available. `fit-contain`
is the default; PDF `fit-width` fills the width and clips vertical overflow.
`continuous` stacks all PDF pages and immediately scrolls cached strips,
independently of client refresh timing. A three-pixel divider occupies the left
edge of a side-by-side pane. Continuous PDFs retain their native document across
zoom/resize; a new `open` reloads it. Failed reloads retain previous pixels.

Search replies with `DCS region secondary found PAGE ST` (0 means no match), then
`DCS region secondary matches PAGE INDEX TOTAL LOCAL_INDEX TRUNCATED ST`.
Indexes are one-based; `LOCAL_INDEX` identifies the occurrence on that page.
Next/previous includes other occurrences on the same page. At most 4096 hits
are retained as zoom-independent rectangles; `TRUNCATED` reports that limit.
Status positions use millionths, zoom uses percent, and loading/failed are 0 or
1. Background prefetch does not report loading when the visible area is cached.

By default, the wheel over a preview changes PDF pages, wrapping at the ends;
preview clicks are consumed locally. Keyboard input stays with the main grid.
The split survives resets and alternate screens until `region reset`, which
clients should send during cleanup. See [frontend rendering](../frontend/README.md#rendering-and-caching)
for worker, strip-cache, and presentation details.

### Retained lists

`region secondary list BASE64` accepts base64-encoded JSON:

```json
{"token":1,"title":"Table of contents","items":[{"id":1,"level":0,"text":"Chapter"}]}
```

The terminal owns glyph rendering, local pixel scrolling, and hit-testing; it
never interprets item IDs as paths or commands. A left click replies with
`DCS region secondary event activate TOKEN ID ST`; clients must reject stale
tokens. Control characters, duplicate/nonpositive IDs, excessive depth, and
oversized lists are rejected. Limits are 4096 items, at most 512 KiB of text,
and the bounded DCS payload. `RegionListHooks` is an optional frontend capability.

### PDF metadata

Continuous PDF queries reply with
`DCS region secondary data TOKEN KIND PAGE OFFSET NEXT BASE64 ST`. Decode and
append JSON bytes in offset order; request `NEXT` until it is `-1`. TOC replies
use page 0; destinations are one-based, with 0 meaning no local destination.
[`pdfmeta`](../../pdfmeta/metadata.go) defines the payload: `toc` entries have
`title`, `page`, and `level`; page text uses `text`; `truncated` and `error`
report limits/failures. Use fresh positive tokens and discard stale replies.

Replies are limited to 1 MiB total, with 3072 JSON bytes per chunk. Extracted
page text is limited to 256 KiB; outlines to 4096 entries and 32 levels.
Extraction uses the existing native worker and never follows external links or
executes PDF actions.

### Fixed-page PDF search

`pdfsearch query`, `chunk`, and `clear` use the native Poppler matcher, not an
external converter. Replies are tokenized, chunked base64 JSON:

```text
DCS pdfsearch data TOKEN OFFSET NEXT BASE64 ST
```

The [`pdfmeta.Search`](../../pdfmeta/metadata.go) payload has `pages` (one page
per occurrence), plus `truncated`/`error`. `NEXT=-1` ends the reply. Append
`pdfsearch TOKEN MATCH` to `open ... rect` to paint retained highlights.
`MATCH` is one-based; zero highlights all occurrences equally. The terminal
checks the document digest before applying coordinates, so stale results cannot
tint another PDF. [GoLS](../../demo-go/gols/README.md) uses this protocol.

## OSC clipboard policy

OSC 52 clipboard writes use `allowclipboardwrite` (default true); reads require
`allowclipboardread` (default false). Clipboard handling belongs to the terminal
host, not client-owned X connections or external clipboard helpers. Native
selection transfers, browser permissions/timeouts, and process-local framebuffer
clipboard behavior are described in the [backend guides](../../platform_dependent/README.md).

## Native video handoff

This optional protocol asks a capable host to play a regular media file. It is
not a general shell-execution channel. The framebuffer host advertises
`ST_GO_NATIVE_VIDEO=1` and a runtime `ST_GO_NATIVE_VIDEO_TOKEN`; clients use:

```text
DCS native-video auth TOKEN ID BASE64_JSON ST
```

`ID` is 32 hexadecimal characters. `BASE64_JSON` is raw, unpadded standard-base64
JSON, at most 32768 encoded bytes, containing only:

```json
{"file":"/absolute/movie.mp4","alsa_config":"/absolute/alsa.conf","audio_device":"alsa/plughw:1,0","volume":33}
```

File/config paths must be absolute, valid UTF-8, NUL-free, and at most 8192 bytes
each. The ALSA device must start with `alsa/`, contain no control characters,
and be at most 256 bytes; volume is 0–100. Unknown JSON fields and trailing
values are rejected. Authentication is independent of window-geometry policy.
The host additionally validates regular files and its own console ownership;
clients cannot choose a tty or executable.

Replies use the same ID:

```text
DCS native-video ID accepted ST
DCS native-video ID done ST
DCS native-video ID error [BASE64_MESSAGE] ST
```

The [browser helper](../../demo-go/file-browser/README.md#framebuffer-video)
waits up to five seconds for acknowledgement, then waits for completion. Only
the [framebuffer backend](../../platform_dependent/fb/README.md#native-video-handoff)
owns console handles, fixed mpv options, VT handoff, and recovery. There is no
fallback to launching DRM mpv on a PTY.

## Go contracts

[`display_regions.go`](display_regions.go) defines `RegionHooks`: stable
`RegionID`s, zero-based Go `DisplayRegion` rectangles, `SetRegions`, `Regions`,
`OpenRegion`, and `ClearRegion`. Navigation/search/document metadata use optional
`RegionNavigationHooks`, `RegionSearchHooks`, and `RegionDocumentHooks`.
[`region_list.go`](region_list.go) defines `RegionListHooks`;
[`pdf_search.go`](pdf_search.go) defines `PDFSearchHooks`.
[`native_video.go`](native_video.go) and
[`api/native_video.go`](../api/native_video.go) define the narrow native-video
hook and host contracts. These capabilities remain display-backend-neutral.

## Multiplexers

tmux reserves DCS for its own protocol and does not forward these frames from a
pane by default. DCS image/preview demos therefore need an appropriate passthrough
mechanism or a direct st-go session.
