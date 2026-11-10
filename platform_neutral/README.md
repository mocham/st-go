# Platform-neutral terminal

The shared terminal engine, frontend, and host contracts serve the X11, Linux
framebuffer, and web-browser backends. Clients communicate using ANSI/OSC/DCS
terminal text rather than native display connections.

## Documentation

- [Building, targets, testing, packaging, and licensing](BUILDING.md)
- [Shared frontend usage, configuration, rendering, and resource limits](frontend/README.md)
- [Terminal engine and custom protocols](stterminal/README.md): DCS display DSL,
  retained regions/lists, PDF search/metadata, clipboard policy, and native-video requests
- [Backend guides](../platform_dependent/README.md):
  [X11](../platform_dependent/x11/README.md),
  [framebuffer](../platform_dependent/fb/README.md), and
  [web browser](../platform_dependent/web/README.md)
- [Go applications](../demo-go/README.md) and [shell demos](../demo/README.md)
- [Implementation caveats and historical porting notes](../CAVEAT.md)

## Layout

* **[`stterminal/`](stterminal/README.md)**: the original `term/` engine and tests,
  with its screen model, ANSI/VT parser, DCS dispatch, selection, animation and
  damage tracking. `Hooks` and optional capability interfaces are neutral contracts.
* **`api/`**: uniform terminal-to-event-host callbacks, painting/surface
   contracts, scoped widget identity/lifecycle, startup values, and the existing geometry-string parser. No X
  handles or native event types cross these contracts.
* **[`frontend/`](frontend/README.md)**: the former root terminal host: PTY
  scheduling, terminal key and mouse encoding, software glyph/image painting,
  retained previews/lists, and native-codec integration. Native events arrive as
  discrete callback calls; the native event loop and representation are not generalized.
* **`../platform_dependent/x11/`**: XGB resources, native event decoding,
  keyboard lookup, clipboard ownership/transfers, window-manager operations,
  and X pixel/pane presentation. These use the same request ordering, pixel
  format, dirty uploads and preview backing-pixmap strategy as before.
* **[`../functionalities/`](../functionalities/README.md)**: independent reusable
  Go modules for TUI helpers, PDF generation, PTYs, configuration, limits, metadata,
   terminfo, terminal-owned audio, and [widget hosts](../functionalities/widgets/README.md). Their original import paths are preserved.
* **`../main.go`**: executable wiring, including the original embedded font.
  `backend_x11.go` selects X11 by default; `backend_web.go` selects web with
  `backend_web`; `backend_fb.go` selects fbdev with `backend_fb`. No backend is
  imported by neutral production code. Select only one backend tag at a time.

The root **`term/`** package is a type-alias/forwarding compatibility shim, not a
second engine. Existing demo/client imports continue to work. Shared public
helpers (`config`, `limits`, `pdfmeta`, `tui`, etc.) now live in `functionalities/`
as independent modules, with unchanged import paths. The engine, `api`, and
compatibility shim also have their own modules. `go.work` selects local modules;
per-consumer replacements keep standalone `GOWORK=off` workflows working.

## Dependencies and ownership

```text
main -> frontend -> neutral api contracts <- X11, web or fbdev implementation
             |
             +--> stterminal
```

The host supplies its existing serialization callbacks to the native event
subsystem. Terminal-to-native requests use `api.EventHost`; terminal painting
uses `api.Painter` / `api.Surface`. The driver keeps native events private and
dispatches semantic calls back into the host. The terminal engine never imports
a display backend. The architecture test rejects backend imports in neutral
production code; explicitly native integration tests may import XGB.

The widget adapter routes native callbacks to each client's own lock. Terminal
rasters, mouse state, image/PDF atlases and PTY lifecycle are instance-owned;
only the process font bundle/glyph cache is shared and serialized. The default
layout remains one terminal plus the framebuffer's existing passive system bar.
No terminal-level tabs or new layout controls are implemented.

The separation preserves timers, terminal protocols, configuration/key bindings, and feature boundaries, without
introducing a universal native event model. The web host accepts committed UTF-8
input alongside key/paste callbacks and advertises unsupported desktop geometry,
so click-to-restore interception is not armed in a browser. The framebuffer host
uses the same APIs. Physical sizing happens before PTY startup, and PTY exit
returns through host cleanup rather than `os.Exit`, so Linux console modes can
be restored.

Here **platform-neutral means display-backend-neutral**. The relocated host
still uses Linux PTYs/signals and native FreeType/Poppler/WebP support. Making it
browser-local or fully OS-portable is separate work; the `stterminal` engine
itself does not require those native media libraries.

In particular, a `js/wasm` build of the engine still encounters the existing
Linux-specific nonblocking-file helpers in `limits`/`config`. This split does
not introduce replacements for those host services. The web backend keeps the
engine, PTY and native codecs on the Linux server; it is not a Wasm port.

## Verification

```sh
go -C platform_neutral/api test ./...
go -C platform_neutral/stterminal test ./...
go -C term test ./...
make test-tui
make test
make st
make st-web
make st-fb
CGO_ENABLED=0 go test ./platform_dependent/fb
```

The full suite still requires the Makefile's existing static native link items.
Frontend regressions now live in `platform_neutral/frontend`; their test harness
preserves the original repository-relative process fixtures.
