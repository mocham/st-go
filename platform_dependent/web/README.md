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
embedded. There is no X connection or browser-side terminal reimplementation.

Use the complete random session URL printed by the server, such as
`http://127.0.0.1:8080/t/<token>/`. `-dir` selects the initial server-side PTY and
relative-preview directory; it is not a browser file picker or static-file root.
Terminal flags must precede `-e`.

One server runs one terminal session with one attached browser at a time.
Closing/reloading the browser leaves the PTY running; reconnecting receives its
current pixels. Shell exit stops the server; Ctrl-C in the server's launching
terminal stops it too. The terminal backing buffer follows the browser viewport
in **device pixels** (`CSS size × devicePixelRatio`), while canvas presentation
divides by the same ratio. This fills the viewport while keeping each
server-rendered glyph/image pixel mapped to one browser device pixel—additional
space creates more terminal rows/columns instead of stretching raster content.
Keyboard/IME, mouse reporting, selections, bracketed paste,
images/PDFs, and retained previews use the same terminal engine and
[text protocols](../../platform_neutral/stterminal/README.md).

## Ownership and protocol

* The existing neutral frontend owns the Linux PTY, terminal parser, software
  rasterizer, codecs, clipboard authorization and preview state. All demos
  continue to communicate using terminal ANSI/OSC/DCS text.
* This driver owns HTTP/WebSocket input decoding, a copied retained pixel
  buffer, retained `api.Surface` panes and browser presentation. It invokes
  uniform callbacks under the frontend lock, never importing frontend internals.
* The browser caches **rendered glyph cells** and receives compact glyph-ID draws
  for repeated cells, alongside PNG **damage rectangles** for uncached pixels.
  FreeType rendering remains server-side; the browser does not interpret ANSI.
  Exact opaque pixel comparisons include colors, font changes, selection and
  composited overlays. Wide glyphs occupy two cached cells. Only whole cells
  reused at different positions are admitted, so repainting an image in place
  does not turn its tiles into glyph definitions;
  adjacent identical glyphs use a single repeated-ID run.
* `STW1` is followed by six little-endian uint32 values (canvas width, canvas
  height, x, y, patch width, patch height), then a PNG. `STG1` is followed by
  canvas width, canvas height and operation count, then ordered operations:
  `0` clears the glyph cache; `2,id,x,y,count` draws a horizontal run;
  `3,firstID,count,cellWidth,cellHeight,columns,length,PNG` defines consecutive
  glyph IDs in one row-major PNG atlas. Every integer is uint32 little-endian.
  The browser decodes each atlas once and draws glyphs from source rectangles.
  Subsequent draws contain no PNG. Both peers bound the cache (including atlas
  padding) to 32 MiB of pixels and 16,384 entries. A presentation admits at most
  256 new glyphs; fragmented fallback damage collapses after four PNG patches.
* Glyph caches are connection-local and rebuilt on reconnect. Viewport resizing
  preserves cached bitmaps; definitions/resets from obsolete-size frames still
  apply, while their screen draws are discarded. Separate dirty rows stay
  separate; bounded damage fragmentation may collapse to a PNG refresh.
  Retained preview panes are composited over underlying terminal updates.
* Input/control messages are bounded JSON. PNG encoding and network writes do
  not hold the terminal lock. Async browser image decoding commits in wire order.
  The browser opts into flow control with `{"type":"paint_ack","id":0}`.
  Each presentation ends with `{"type":"paint","id":N}`; the browser returns
  `paint_ack` for N after queued paints complete. Only one presentation remains
  in flight, so slow decoding coalesces newer damage on the server instead of
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
preview blank. Payloads must fit the terminal's 1 MiB DCS control budget.

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
shared text can replace the browser/OS clipboard. No separate service exists.

Select with the mouse (Shift overrides application mouse reporting), then use
**Copy** or Ctrl-Shift-C. Use native Ctrl-Shift-V or the **Paste** button to paste.
Clipboard APIs depend on browser permissions and a secure context; loopback HTTP
qualifies. Clipboard-read authorization remains disabled by default.

## Security and scope

The directory is **not a sandbox**. Anyone with the random URL can use a shell
with the server user's permissions. Keep the URL private and bind loopback;
use an SSH tunnel for remote access. The server validates numeric/localhost
Host values and exact HTTP Origin, disallows framing, serves only three
embedded assets and never serves arbitrary files. It provides no TLS,
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
go test ./platform_dependent/web -run '^$' -bench BenchmarkWebDamage -benchmem
make test                 # includes built-server PTY/cwd/input/image regression
make test-web-browser     # optional: Node >=22 and Chromium; no npm packages
```

Set `CHROMIUM=/path/to/chromium` for the optional live canvas smoke test.

See [shared configuration and limits](../../platform_neutral/frontend/README.md)
and the [build guide](../../platform_neutral/BUILDING.md).
