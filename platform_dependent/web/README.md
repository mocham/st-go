# Web backend

```sh
make st-web
./BINARIES/st-web -port 8080 -dir /absolute/project/path
```

Open the printed `/t/<random-token>/` URL. `-listen` defaults to `127.0.0.1`;
`-port 0` chooses a free port. `-e PROGRAM ARGS...` runs a command on the server.
Use absolute program paths when combining `-e` with `-dir`, and put terminal
options before `-e`.

The CGO-free binary embeds the browser client, fonts, and PDF.js. No native media
libraries, npm build, CDN, or separate assets are needed. One browser controls
the direct-launch terminal session, including its tabs. Disconnecting leaves PTYs
running; reconnecting restores current content. Closing the last terminal exits
the server.

## Persistent shared service

```sh
./BINARIES/st-web -service -port 8080 -base-path /terminal/ -dir /absolute/project/path
```

Service mode stays alive with zero PTYs. Its top-level browser page provides
**New terminal**, **Refresh terminals**, rename, close, and per-browser hide/show.
One compact tab bar holds the terminals and **+** button. The **☰** menu contains
refresh/hide-list controls, the tools icon opens clipboard and Esc/Tab controls,
and each tab's **⋯** menu contains hide/show, rename, and close. Tabs scroll
horizontally on narrow screens; dropdowns overlay the terminal without resizing it.
Different browsers can control different PTYs; each PTY has one exclusive,
expiring attachment. Hiding detaches without closing the PTY. Occupancy and the
terminal list update on page load or manual refresh, with no notification stream.
The default heartbeat/expiry are 20/90 seconds (`-attachment-timeout` changes
expiry); clean disconnects release immediately. Refresh and select to reattach.

For remote access, use the [nginx/Docker Compose deployment](../../deploy/web/README.md):
TLS and bcrypt Basic authentication at nginx, a private st-web upstream, mandatory
prefix preservation, and `-origin https://your-host` validation. APIs, media,
PDF.js workers, pages, redirects, and attachments all stay under `-base-path`.

## Ownership and protocol

- The server owns terminal parsing, cells, Unicode widths, geometry, colors,
  selection, and preview parameters. The browser draws Canvas text from semantic
  scenes; it does not reimplement terminal behavior or reflow the grid.
- The browser decodes original PNG/JPEG/GIF/WebP/PDF bytes. TIFF/BMP use bounded
  pure-Go PNG conversion on the server. PDF.js supports selectable text, lazy
  page rendering, search, and outline/page-text queries.
- Media/document work is asynchronous and separate from ordered text painting.
  Paint acknowledgements keep only one presentation in flight, coalescing newer
  state instead of building an input-blocking backlog.
- Media storage is bounded to 64 registrations / 128 MiB encoded content, with
  64 MiB per input and at most 16 inline resources. PDF canvases are evicted when
  offscreen. Replacement/generation checks discard stale work.
- In service mode each PTY has its own renderer, callback/input serialization,
  scene/media stores and clipboard requests. The listener and PTY factory outlive
  individual terminals. `-max-ptys` (default 8) bounds their aggregate count.

Canvas dimensions follow device pixels, so viewport size changes add/remove
terminal cells rather than stretch their content. Browser zoom and display
scaling are accounted for.

## Browser-native document previews

```sh
make st-web file-browser govim opencode-tui
./BINARIES/st-web -e ./BINARIES/file-browser --web /some/path
./BINARIES/st-web -e ./BINARIES/govim --web notes.md
./BINARIES/st-web -e ./BINARIES/opencode-tui --web --server http://127.0.0.1:4096 /project
```

`--web` renders Markdown/LaTeX as HTML with inline SVG mathematics, avoiding PDF
generation. File-browser and govim also preview HTML directly; govim shows CSS/JSON
as source. Documents use script-disabled, style-isolated iframes with native
selection and scrolling. Esc returns focus to the application. File-browser's
navigation stays pinned; Tab hides/shows it even when a preview has focus.

Previews follow resize, tabs, and reconnect. Large documents transfer atomically
in chunks, up to 64 MiB. X11/framebuffer discard HTML content, so use `--web` in
st-web. See the [region implementation](../../platform_neutral/stterminal/display_regions.go).

## Clipboard

Select terminal text with the mouse (Shift overrides application mouse reporting),
then use **Copy** or Ctrl-Shift-C. Paste with Ctrl-Shift-V or **Paste**. Browser
permissions and a secure context are required; loopback HTTP qualifies.
OSC 52 clipboard reads remain disabled by default in terminal configuration.

**Sync off/on** opts the server into the shared SQLite clipboard used by enabled
st-go windows. It starts off each launch. Press **Copy** to transfer shared text
to the browser/OS clipboard; synchronization cannot bypass browser permissions.

## Remote access and limits

Direct-launch token URLs grant shell access with the server user's permissions;
`-dir` selects a working directory. Loopback plus an SSH tunnel supports private
direct access. Shared remote access uses the authenticated Compose deployment
above; authentication and TLS belong to nginx, and Docker provides the sandbox.

A modern browser is required. Canvas terminal text has no semantic accessibility
tree. Browser-reserved shortcuts may not reach the terminal; toolbar Esc/Tab
buttons provide alternatives. Desktop window placement is unsupported.

## Verification

```sh
make test-web
make test-web-browser     # Node >=22 and Chromium; CHROMIUM can override its path
```

See [configuration and limits](../../platform_neutral/frontend/README.md) and
[building](../../platform_neutral/BUILDING.md).
