# st-client

A Linux terminal client for `st-web`. Run it inside local `st` (X11) or `st-fb`
to use that terminal's existing text, image and PDF renderers. It connects to
the same HTTP/WebSocket service as the browser.

## Build and connect

From the repository root:

```sh
make -C demo-go/st-client build
BINARIES/st-client -list https://terminal.example.com/
BINARIES/st-client https://terminal.example.com/
BINARIES/st-client -pty PTY_ID https://terminal.example.com/
BINARIES/st-client -new -dir /workspace https://terminal.example.com/
```

A service URL opens a persistent tab UI and selects the first available PTY.
Each service PTY appears as a tab; `-pty` chooses the initial tab. The client
remains open when the last PTY exits, with controls to create another terminal.
Both managed view URLs (`…/api/ptys/ID/view/`) and standalone terminal URLs
(`…/t/TOKEN/`) can be supplied directly. Reverse-proxy path prefixes work.
Managed view URLs retain their parent service, so even a deleted PTY's URL
opens a usable service UI. Standalone servers expose one PTY and have no service
tab management API.

For an authenticating nginx proxy, set `ST_CLIENT_AUTHORIZATION` to the complete
HTTP header value, such as `Basic BASE64_USER_COLON_PASSWORD` or `Bearer TOKEN`.
The same header is used for service requests, WebSocket attachment and resources.
HTTPS uses the system certificate trust store.

This is an independent module; its Makefile uses `GOWORK=off`. It requires Go
1.26 and builds with `CGO_ENABLED=0`. Full native **local terminal** builds are
needed for PDF previews; minimal terminals support text and static images.

## Controls

| Keys | Action |
| --- | --- |
| `Ctrl-]`, then `q` | Detach; managed PTY keeps running |
| `Ctrl-]`, then `n` | Create and select a new service tab |
| `Ctrl-]`, then `1`…`9` | Select a tab; the next unused slot creates one |
| `Ctrl-]`, then `Tab` / `p` | Next / previous available tab |
| `Ctrl-]`, then `x` | Close the selected service PTY |
| `Ctrl-]`, then `l` | Refresh the service's terminal list |
| `Ctrl-]`, then `r` | Reconnect immediately |
| `Ctrl-]`, then `c` | Copy the latest remote selection through local OSC 52 |
| `Ctrl-]`, then `Ctrl-]` | Send a literal `Ctrl-]` remotely |

The tab strip sits above the bottom status row. Click a title to switch, `[+]`
to create, or `[x]` to close; wheel over the strip cycles tabs. `Alt+1`…`Alt+9`
and `Ctrl+PageUp/PageDown` also switch tabs when the local host forwards those
keys. The `Ctrl-]` commands work independently of host tab shortcuts. Occupied
tabs are marked `!`; the server enforces its single-controller attachment lease.

Only the selected tab holds a WebSocket attachment. Switching releases that
attachment while leaving the PTY running on the server. If the selected PTY
exits or is deleted, the client selects another available tab or shows the empty
service UI. The list refreshes every ten seconds and immediately on disconnect;
temporary service/network failures preserve the known tabs. Creating and closing
PTYs runs asynchronously, and mutations are never automatically replayed after
a lost response. `-dir` and `-title` supply defaults for newly created tabs.

Keyboard input, UTF-8, bracketed paste, focus, SGR mouse and terminal resize are
forwarded. Managed services reserve two local rows for tabs/status; standalone
views reserve one status row. Remote coordinates use the server's
font metrics, while local drawing uses terminal cells. Local retained previews
also support the host terminal's scrolling/navigation controls.

Reconnect is automatic with 1–15 second backoff. `-reconnect=false` disables
automatic attachment retries; managed service tabs remain usable for manual
reconnection and new terminals. Standalone mode exits on disconnect with that
flag. `-clipboard-read` enables remote clipboard queries through local OSC 52.
Run `st-client -help` for all options.

## Rendering and resource ownership

- Retained semantic text scenes become incremental true-color ANSI updates,
  committed at server paint boundaries before acknowledgement. Wide cells,
  combining clusters, colors, styles and cursor position are preserved.
- Input uses interruptible blocking polling. Heartbeats maintain idle
  attachments. Visible native media is queried once per second for status;
  framebuffer refresh leases are renewed only when painting remote activity.
- Media downloads and HTML conversion run outside the input/paint loop.
  Obsolete fetches are cancelled and stale completions are ignored.
- The private temporary resource cache is bounded to 32 files / 128 MiB, with
  active resources pinned. Each fetched resource is limited to 64 MiB; semantic
  HTML conversion is limited to 8 MiB of markup. Files are removed on exit.
- Content-addressed HTML/CSS JSON is retained unchanged, allowing an exact source
  round trip. View changes and resizing reuse the downloaded source. Local PDFs
  are generated with `semhtml` and cached by document URL and pane dimensions.
  Numeric media IDs are scoped to an attachment to avoid reuse after a service
  restart.
- PDF text/contents queries use the visible native preview; search uses the local
  PDF-search protocol. Chunked replies, cancellations and deadlines are bridged
  back to `st-web`.

## Current scope

This first client uses st-go's application-facing terminal protocols. It needs
the semantic-scene version of `st-web`; legacy raster WebSocket frames produce
an explicit error. Ordinary ANSI terminals can show text, while graphics need
the st-go display protocols.

One retained HTML/PDF/image preview pane is supported. PDFs use the native
continuous view. Inline static images/PDF pages support row clipping within an
onscreen placement; placements whose source origin has scrolled offscreen are
currently omitted. Animated media, browser text-selection UI, and simultaneous
multiple preview panes need further client work. PDF resources are downloaded
in full before native rendering.

`semhtml` handles a controlled semantic HTML subset. CSS is preserved as source;
general CSS layout, JavaScript and browser-managed external resources are outside
the native renderer's scope. Native and browser layout may differ. Source
preservation is lossless; PDF rendering is a presentation step.

## Verification

```sh
make -C demo-go/st-client test
# With BINARIES/st-web already built:
make -C demo-go/st-client test-live
```

Tests cover fragmented input, real terminal-parser rendering, wide/combining
cells, clipped/stale images, native preview commands, metadata chunks and
cancellation, cache reuse/source preservation, URL/resource bounds, and PTY
reconnect/resize/heartbeat/cleanup. Tab tests cover empty/deleted deep links,
switching, authenticated close, last-PTY exit/recreation, stale completions,
application-spawned tabs and narrow/wide-title mouse hit targets. `test-live`
additionally starts an isolated real `st-web` service and checks shell I/O,
recovery after shell exit and API deletion, and PTY survival after detach.
Physical X11/framebuffer display testing is separate.
