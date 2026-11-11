# Web backend

```sh
make st-web
./BINARIES/st-web -port 8080 -dir /absolute/project/path
```

Open the printed `/t/<random-token>/` URL. `-listen` defaults to `127.0.0.1`;
`-port 0` chooses a free port. Normal configuration/font/ratio options work,
and `-e PROGRAM ARGS...` runs an explicit command in the server-side directory.
Use absolute program paths or programs on PATH when combining `-e` with `-dir`.
Copying this one binary is enough; all browser assets and built-in fonts are
embedded, including PDF.js. `st-web` builds with `CGO_ENABLED=0`; it needs no
FreeType, Poppler, native image codecs, or decoded-WebP cache. There is no X
connection or browser-side terminal reimplementation.

Use the complete random session URL printed by the server, such as
`http://127.0.0.1:8080/t/<token>/`. `-dir` selects the initial server-side PTY and
relative-preview directory; it is not a browser file picker or static-file root.
Terminal flags must precede `-e`.

One server runs one terminal session with one attached browser at a time.
Closing/reloading the browser leaves the PTY running; reconnecting receives its
current semantic scene. Shell exit stops the server; Ctrl-C in the server's launching
terminal stops it too. The terminal backing buffer follows the browser viewport
in **device pixels** (`CSS size × devicePixelRatio`), while canvas presentation
divides by the same ratio. This fills the viewport while keeping each
server-selected cell coordinate mapped to one browser device pixel—additional
space creates more terminal rows/columns instead of stretching raster content.
Keyboard/IME, mouse reporting, selections, bracketed paste,
images/PDFs, and retained previews use the same terminal engine and
[text protocols](../../platform_neutral/stterminal/README.md).

## Ownership and protocol

* The frontend owns the PTY, parser, grid, Unicode sequences, resolved colors,
  selection and preview parameters. Demos still use ANSI/OSC/DCS.
* `api.SceneUpdate` describes text runs, explicit advances, fills, media handles
  and the cursor. Terminal text, tab labels and preview lists use these operations
  across Canvas and the native raster adapter. X11/framebuffer keep their native
  glyph cache and damage/scroll presentation paths.
* `scene` WebSocket messages replace changed horizontal bands. The server retains
  every state mutation while coalescing presentations; it does not allocate a
  terminal framebuffer for semantic web surfaces. Canvas uses the embedded font,
  server coordinates and explicit wide/combining-cell advances without reflow.
* Media registration returns an opaque ID immediately. Two bounded workers load
  immutable encoded files; the browser fetches `media/<id>` and owns decoding.
  PNG/JPEG/GIF/WebP are sent unchanged. TIFF/BMP have bounded pure-Go PNG fallback.
  The resource store allows 64 registrations and 128 MiB of encoded content;
  individual inputs retain the shared 64 MiB limit. Inline media retains at most
  16 resources. Cancellation and document generations reject stale completions.
* PDF.js is bundled locally, with its worker, CMaps, standard fonts and WASM.
  Original PDF bytes support HTTP range requests with stable offsets. Continuous
  views rasterize nearby pages with two tasks at a time, evict offscreen canvases,
  and provide selectable text. Search and outline/page-text queries use the
  existing terminal metadata protocol. Replacement keeps the committed preview
  visible while the new resource loads.
* Media decoding/document queries run outside the ordered text paint queue.
  A stalled preview therefore cannot delay terminal text acknowledgements.
  Network writes never hold the terminal lock.
  The browser opts into flow control with `{"type":"paint_ack","id":0}`.
  Each presentation ends with `{"type":"paint","id":N}`; the browser returns
  `paint_ack` for N after queued paints complete. Only one presentation remains
  in flight, so slow presentation coalesces newer state on the server instead of
  building a browser backlog. Input and clipboard controls continue meanwhile.
* One browser controls the one server session. Disconnecting leaves its PTY
  alive; reconnecting gets a full snapshot. Idle heartbeats expire vanished
  attachments after 90 seconds. Slow writes time out after five seconds.
* Titles/icon titles and `BAR_DATA` are ignored. Browser viewport resizing
  updates terminal pixel/grid dimensions; desktop placement/collapse/restore is
  deliberately unsupported.

## Browser-native document previews

```sh
make st-web govim
./BINARIES/st-web -e ./BINARIES/govim --web notes.md
```

`govim --web` reserves the normal preview region and emits
`DCS region secondary html BASE64 ST`, whose payload is UTF-8 JSON
`{"html":"…","css":"…"}`. The browser displays it in an absolutely positioned
HTML iframe matching that retained surface's bounds. Markdown/LaTeX uses semantic
HTML and inline SVG mathematics; HTML files render directly, and CSS/JSON files
use escaped source views. No preview PDF is created or rasterized.

The iframe has isolated styling and scripts disabled. It supports native text
selection and scrolling; Esc returns keyboard focus to the editor. Content and
geometry survive viewport changes and reconnects; hiding a tab or closing the
region removes its overlay. `region secondary position` and `scroll` navigate
the HTML document. Framebuffer and X11 discard HTML content, leaving the reserved
preview blank. Documents larger than one control message use `html begin`,
`html chunk`, and `html commit` transfers (see the terminal-core protocol docs).
The old preview stays visible until the complete document arrives. JSON documents
are limited to 64 MiB; individual DCS messages remain limited to 1 MiB.

Clipboard text stays terminal-owned. Selection/OSC 52 writes make the toolbar's
Copy action available, rather than attempting unsolicited browser clipboard
writes. Native browser paste feeds the terminal's paste callback (including
bracketed-paste handling). Authorized OSC 52 reads ask the browser and fail
after 1.5 seconds or on denial/disconnect. Browsers may require an explicit
gesture or permission; native Ctrl-Shift-V remains the fallback.

The **Sync off/on** toolbar control opts this server into the cache-backed shared
clipboard used by other enabled st-go fb/web/X11 windows. It starts off on every
launch and is not persisted. Disabled sessions perform no shared-clipboard SQL
or polling. While enabled, the server polls revisions only while a browser is
attached; browser security still requires the user to press **Copy** before the
shared text can replace the browser/OS clipboard. The web build uses a pure-Go
SQLite driver for this shared clipboard database; native builds retain their
existing driver. No separate service exists.

Select with the mouse (Shift overrides application mouse reporting), then use
**Copy** or Ctrl-Shift-C. Use native Ctrl-Shift-V or the **Paste** button to paste.
Clipboard APIs depend on browser permissions and a secure context; loopback HTTP
qualifies. Clipboard-read authorization remains disabled by default.

## Security and scope

The directory is **not a sandbox**. Anyone with the random URL can use a shell
with the server user's permissions. Keep the URL private and bind loopback;
use an SSH tunnel for remote access. The server validates numeric/localhost
Host values and exact HTTP Origin, disallows framing, and serves embedded assets
and registered media resources. It provides no TLS,
multi-user authentication or reverse-proxy configuration. Direct exposure on
an untrusted network is not safe.

This is a native Linux server, not a `js/wasm` build. A modern browser with
canvas, WebSocket and `createImageBitmap` is needed. Canvas
pixels do not provide a semantic accessibility tree. Browser/OS-reserved
shortcuts may be unavailable to the terminal; Esc and Tab toolbar buttons
provide basic alternatives. HTTP outside loopback may disable Clipboard APIs.

## Verification

```sh
go test -race ./platform_dependent/web
make test-web             # CGO-free build, renderer and clipboard tests
go test ./platform_dependent/web -run '^$' -bench BenchmarkWebDamage -benchmem
make test                 # includes built-server PTY/cwd/input/image regression
make test-web-browser     # optional: Node >=22 and Chromium; no npm packages
```

Set `CHROMIUM=/path/to/chromium` for the optional live canvas smoke test.

See [shared configuration and limits](../../platform_neutral/frontend/README.md)
and the [build guide](../../platform_neutral/BUILDING.md).
