# First-round platform split

This is a relocation and thin-adapter refactor, not a new frontend framework or
a new backend. All clients still use the terminal's existing ANSI/OSC/DCS text
protocol; its behavior and the existing build target names are unchanged.

## Layout

* **`stterminal/`**: the original `term/` engine and tests, with its screen model,
  ANSI/VT parser, DCS dispatch, selection, animation and damage tracking. Its
  existing `Hooks` and optional capability interfaces remain neutral contracts.
* **`api/`**: uniform terminal-to-event-host callbacks, painting/surface
  contracts, startup values, and the existing geometry-string parser. No X
  handles or native event types cross these contracts.
* **`frontend/`**: the former root terminal host: PTY scheduling, terminal key and
  mouse encoding, software glyph/image painting, retained previews/lists, and
  native-codec integration. Native events arrive as discrete callback calls;
  the native event loop and representation are not generalized.
* **`../platform_dependent/x11/`**: XGB resources, native event decoding,
  keyboard lookup, clipboard ownership/transfers, window-manager operations,
  and X pixel/pane presentation. These use the same request ordering, pixel
  format, dirty uploads and preview backing-pixmap strategy as before.
* **`../main.go`**: executable wiring, including the original embedded font.

The root **`term/`** package is a type-alias/forwarding compatibility shim, not a
second engine. Existing demo/client imports continue to work. Shared public
helpers (`config`, `limits`, `pdfmeta`, `tui`, etc.) retain their existing paths
in this first pass to avoid unrelated import churn.

## Dependencies and ownership

```text
main -> frontend -> neutral api contracts <- X11 implementation
             |
             +--> stterminal
```

The host supplies its existing serialization callbacks to the native event
subsystem. Terminal-to-native requests use `api.EventHost`; terminal painting
uses `api.Painter` / `api.Surface`. The driver keeps native events private and
dispatches semantic calls back into the host. The terminal engine never imports
a display backend. The architecture test rejects backend imports in neutral
production code; explicitly native integration tests may import XGB.

This pass deliberately preserves the existing locks, global raster/cache state,
timers, terminal protocols, configuration/key bindings, and feature boundaries.
It does not add backend selection, new capabilities, or a universal event model.

Here **platform-neutral means display-backend-neutral**. The relocated host
still uses Linux PTYs/signals and native FreeType/Poppler/WebP support. Making it
browser-local or fully OS-portable is separate work; the `stterminal` engine
itself does not require those native media libraries.

In particular, a `js/wasm` build of the engine still encounters the existing
Linux-specific nonblocking-file helpers in `limits`/`config`. This split does
not introduce replacements for those host services or claim browser support.

## Verification

```sh
go test ./platform_neutral/api ./platform_neutral/stterminal ./term
make test
make st
```

The full suite still requires the Makefile's existing static native link items.
Frontend regressions now live in `platform_neutral/frontend`; their test harness
preserves the original repository-relative process fixtures.
