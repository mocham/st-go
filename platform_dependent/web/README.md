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
terminal stops it too. Browser resizing updates the PTY dimensions. Keyboard/IME,
mouse reporting, selections, bracketed paste, images/PDFs, and retained previews
use the same terminal engine and [text protocols](../../platform_neutral/stterminal/README.md).

## Ownership and protocol

* The existing neutral frontend owns the Linux PTY, terminal parser, software
  rasterizer, codecs, clipboard authorization and preview state. All demos
  continue to communicate using terminal ANSI/OSC/DCS text.
* This driver owns HTTP/WebSocket input decoding, a copied retained pixel
  buffer, retained `api.Surface` panes and browser presentation. It invokes
  uniform callbacks under the frontend lock, never importing frontend internals.
* The browser receives PNG **damage rectangles**, not a second interpretation of
  ANSI. A binary frame is `STW1`, six little-endian uint32 values (canvas width,
  canvas height, x, y, patch width, patch height), then a PNG. Separate dirty
  rows stay separate; a bounded backlog may collapse to a full refresh. Retained
  preview panes are composited over underlying terminal updates.
* Input/control messages are bounded JSON. PNG encoding and network writes do
  not hold the terminal lock. Async browser image decoding commits in wire order.
* One browser controls the one server session. Disconnecting leaves its PTY
  alive; reconnecting gets a full snapshot. Idle heartbeats expire vanished
  attachments after 90 seconds. Slow writes time out after five seconds.
* Titles/icon titles and `BAR_DATA` are ignored. Browser viewport resizing is
  supported; desktop placement/collapse/restore is deliberately unsupported.

Clipboard text stays terminal-owned. Selection/OSC 52 writes make the toolbar's
Copy action available, rather than attempting unsolicited browser clipboard
writes. Native browser paste feeds the terminal's paste callback (including
bracketed-paste handling). Authorized OSC 52 reads ask the browser and fail
after 1.5 seconds or on denial/disconnect. Browsers may require an explicit
gesture or permission; native Ctrl-Shift-V remains the fallback.

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
canvas, WebSocket, `createImageBitmap` and `ResizeObserver` is needed. Canvas
pixels do not provide a semantic accessibility tree. Browser/OS-reserved
shortcuts may be unavailable to the terminal; Esc and Tab toolbar buttons
provide basic alternatives. HTTP outside loopback may disable Clipboard APIs.

## Verification

```sh
go test -race ./platform_dependent/web
make test                 # includes built-server PTY/cwd/input/image regression
make test-web-browser     # optional: Node >=22 and Chromium; no npm packages
```

Set `CHROMIUM=/path/to/chromium` for the optional live canvas smoke test.

See [shared configuration and limits](../../platform_neutral/frontend/README.md)
and the [build guide](../../platform_neutral/BUILDING.md).
