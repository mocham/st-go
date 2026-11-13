# X11/XGB caveats

These are correctness constraints and regression traps for the native driver.

## Window creation and event lifetime

- **Empty-title race:** a WM can read `WM_NAME`/`WM_CLASS` on `CreateNotify`.
  Queue unchecked `CreateWindow` and identity-property writes before any round
  trip. `CreateWindowChecked` between them exposed an unnamed, untiled window.
  See [`window.go`](window.go).
- **`Sync` is not a flush:** XGB writes requests to the socket; `Sync()` waits
  for a reply. Avoid it in hot paint paths. Requests on the auxiliary XGBUtil
  connection are not ordered with requests on the main connection.
- **Close-loop spin:** set closed state before closing connections, and stop
  `WaitForEvent` processing after closure. Handle nil events without treating them
  as real events. Free both connections and all child resources exactly once.
- Ignore focus grab/ungrab transitions. Ordinary focus changes must repaint the
  caret immediately. See [`events.go`](events.go).

## Geometry and input

- Use final mapped/configured dimensions for PTY sizing. Using configured
  rows/columns before a tiling WM adjusted the window caused incorrect editor
  layouts. Ignore move-only resizes.
- gxw publishes `_GXW_ST_WORKAREA` on its EWMH support window. Resolve the live
  support-window/self-check and read this allocation before `CreateWindow`, so
  st starts with its final drawable dimensions. Never cache it in child launch
  environments; a shell may create terminals after a screen resize. Only changed
  top-level client dimensions should dispatch a frontend resize callback.
- `GetGeometry` coordinates are parent-relative. Translate to root coordinates
  before remembering/restoring a reparented client. Prefer supported EWMH
  placement, with raw `ConfigureWindow` for WM-less operation; WMs can reject
  either request. See [`geometry.go`](geometry.go).
- Click-to-restore keeps a window mapped. Consume the restoring press, motion,
  and release before terminal mouse reporting, or the application receives an
  unmatched release. Clamp pointer pixels before converting to cell indexes.
- Resolve keysyms using the X server mapping, including its per-keycode stride
  and `NoSymbol` fallback. Exact modifier matching must first mask ignored bits.
  See [`keyboard.go`](keyboard.go) and its tests.

## Painting

- The current upload path uses depth 24 with four bytes per pixel. Packed
  three-byte RGB produces `BadLength`; depth 32 against a depth-24 drawable
  produces `BadMatch`. Changes must account for visual masks, byte order,
  scanline padding, and request-size limits. See [`paint.go`](paint.go).
- A GC is depth-tied, not tied to one drawable. Do not assume `PutImage` performs
  alpha composition; supply final opaque pixels.
- Clip before slicing buffers, especially after shrink with wide/image cells.
  Widget coordinates are local: source-buffer offsets and destination placement
  must stay distinct.
- Preview child windows share the driver's event loop. Cached scrolling shifts
  their backing pixmap and uploads only exposed rows; Expose must use the last
  committed frame, including during synchronized output.
- Keep the caret out of glyph content. Refresh its clean underlay when covered
  text changes, so moving/hiding it cannot restore stale pixels.

## Clipboard and verification

- INCR needs `PropertyChange` events; omitting the mask hangs large pastes.
- If `SelectionRequest.property` is `None`, resolve it to the requested target
  and advertise that resolved property in `SelectionNotify`. Reporting `None`
  signals failure even if data was written. See [`selection.go`](selection.go).
- Use isolated X servers for screenshot/`GetImage` assertions; shared or obscured
  windows can return misleading pixels. Prefer core/buffer assertions otherwise.
- `make test-webp-x11` and `make test-zat-x11` exercise built binaries.
  `ST_TEST_STARTUP_X11=1` enables copied-binary startup tests in the frontend;
  `ST_TEST_BINARY` selects the executable. Use the native link flags from
  [the build guide](../../platform_neutral/BUILDING.md#tests).
