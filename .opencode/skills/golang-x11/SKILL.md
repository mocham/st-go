---
name: golang-x11
description: Use when implementing or debugging Golang X11/XGB frontends, including event loops, window geometry, mouse input, framebuffer painting, EWMH interactions, and X11 lifecycle caveats.
---

# Golang X11 Frontends

Use this workflow for X11 frontend work in Go, especially code built on
`github.com/BurntSushi/xgb` and `xgbutil`.

## Start With The Ownership Model

Identify these objects before changing code:

- The raw X connection and the goroutine that reads it.
- The window, root window, visual, depth, colormap, GC, and backing pixmap.
- The lock that serializes terminal state, event handling, PTY output, and
  painting.
- Which state is authoritative in X, which is cached in Go, and which is owned
  by a window manager.
- Every X resource that must be freed during shutdown.

In this repository, begin with `x11.go`, `event.go`, `draw.go`, `mouse.go`, and
`window_geometry.go`.

## Connection And Window Creation

- Prefer one connection for one event loop. If a helper library opens another
  connection, remember that requests from the two connections are ordered only
  after server processing, not by Go call order.
- XGB unchecked requests are useful when several requests must reach the server
  as one batch. Checked requests and `Reply`/`Sync` introduce round trips.
- Set identity properties immediately after `CreateWindow`. Some window
  managers react to `CreateNotify` and synchronously read `WM_NAME`, `WM_CLASS`,
  or custom properties. A round trip between creation and those property writes
  creates a race.
- Select every event mask needed at creation. Typical terminal masks include
  exposure, key press/release, structure notifications, focus, visibility,
  property changes, button press/release, and pointer motion.
- Map only after identity, protocol, PID, and size-hint properties have been
  queued.

Repository caveat: `CAVEAT.md` documents the window-manager title race and the
reason the creation/property request batch must not be split.

## Event Loop

A robust XGB loop has this shape:

```go
for {
    event, err := conn.WaitForEvent()
    if err != nil {
        if closed.Load() {
            return
        }
        log.Printf("x11: %v", err)
        continue
    }
    if event == nil {
        continue
    }

    mu.Lock()
    handleEvent(event)
    mu.Unlock()
}
```

Rules:

- Treat `(nil, nil)` as no event, not as a valid event and not as a fatal error.
- Mark the connection closed before closing it, otherwise `WaitForEvent` may
  spin while logging errors forever.
- Keep state mutation serialized. PTY reads, cursor-blink timers, and X events
  commonly touch the same terminal and framebuffer state.
- Do not hold the frontend lock while waiting on unrelated child processes or
  long-running work.
- Consume `WM_DELETE_WINDOW` through `WM_PROTOCOLS`; do not depend on process
  signals alone.
- Ignore focus grab/ungrab transitions when matching st behavior.
- Let `ConfigureNotify` drive the final terminal resize. A window manager may
  reject or alter the requested geometry.

## Geometry And Window Managers

Keep coordinate systems explicit:

- Terminal rows/columns are cells.
- Client width/height are pixels.
- `GetGeometry.X/Y` are relative to the immediate parent.
- `TranslateCoordinates(window, root, 0, 0)` gives root-relative client
  coordinates.
- Window-manager frame geometry may differ from client geometry because of
  decorations.

For requested geometry:

1. Resolve ratios against the live root or work-area dimensions.
2. Convert the desired client dimensions to pixels.
3. Enforce at least one cell plus borders.
4. Send `_NET_MOVERESIZE_WINDOW` only when the WM advertises/supports it.
5. Use `ConfigureWindow` as the WM-less fallback.
6. Wait for `ConfigureNotify`; do not resize terminal state optimistically.

For reparenting WMs, raw client `ConfigureWindow` requests can be redirected or
interpreted relative to a frame. EWMH is preferable when available. Tiling WMs
may intentionally ignore both move and size requests; handle that as policy,
not as a terminal-core failure.

When memorizing geometry by tag, store immutable snapshots per terminal, bound
the number and length of tags, and delete temporary tags when applications
exit. Do not use process-global geometry maps.

A truly iconified window is unmapped and cannot receive a click in its client
area. For click-to-restore behavior, keep the window mapped and collapse it to
a small edge or corner rectangle.

## Mouse Ordering

Application mouse reporting usually runs before local selection. Any frontend
action that must consume a click, such as restoring collapsed geometry, must be
checked before normal mouse reporting.

When consuming a press:

- Consume its matching motion and release too.
- Do not update the normal pressed-button bitset for the consumed press.
- Clear suppression on release.
- Allow the next independent click to reach the application.

Otherwise applications receive an unmatched release or drag sequence.

For terminal mouse protocols, prefer SGR mode. It avoids the coordinate and
byte-range limitations of legacy X10 encoding.

## Painting Model

This repository uses a software framebuffer:

1. The terminal core marks dirty rows.
2. `DrawLine` clears and repaints the relevant cell span.
3. Text glyphs are rasterized into the framebuffer.
4. Image-cell glyphs copy full pixel blocks from an atlas.
5. Cursor drawing overlays or replaces the current cell representation.
6. `FinishDraw` uploads pixels with `PutImage`.

Important details:

- Framebuffer dimensions must match the live client pixel size.
- Recreate or resize framebuffer storage only after `ConfigureNotify`.
- Clip every pixel rectangle before slicing the framebuffer.
- A GC is depth-tied rather than drawable-tied. A GC created for a pixmap may be
  used on a same-depth window.
- Verify server image byte order, depth, scanline padding, and pixel format
  before changing `PutImage` code.
- `PutImage` is not alpha compositing. Prepare final opaque pixels first.
- Dirty-row repaint may touch a larger pixel span than one logical widget, but
  unchanged terminal cells must be repainted from the screen model.
- Keep image atlas offsets valid for every screen, including alternate and
  scrollback buffers. Globally resetting an atlas while inactive screens retain
  image glyphs creates dangling references.

## Resize Sequence

On a real size change:

1. Convert client pixels to rows/columns using border and cell dimensions.
2. Clamp to at least one row and one column.
3. Recreate/clear framebuffer storage.
4. Resize the terminal core.
5. Issue `TIOCSWINSZ` to the PTY.
6. Redraw.

Avoid doing this for move-only `ConfigureNotify` events when width and height
are unchanged.

## Resource And Concurrency Caveats

- Free pixmaps, GCs, windows, cursors, and all X connections exactly once.
- Do not leave a secondary `xgbutil` connection open when the raw connection is
  closed.
- Avoid package-global mouse, framebuffer, and atlas state if multiple terminal
  instances can coexist in tests or one process.
- Never call into a real X frontend from headless/dump hooks. Use a no-op hook,
  nil-safe frontend method, or capability boundary.
- Keep synchronous X queries out of hot drawing paths.

## Verification

Use layered tests:

- Pure tests for geometry resolution, ratio rounding, anchors, and clipping.
- Hook/parser tests without X.
- Xvfb tests for geometry queries, `ConfigureNotify`, mouse event suppression,
  and framebuffer resize.
- Tests under a lightweight reparenting/EWMH WM for frame coordinates and
  move/resize requests.
- Tests under a tiling WM for graceful policy rejection.
- `go test -race` for event-loop, PTY-reader, and blink-timer interactions.
- Pixel-buffer assertions before X readback; X framebuffer readback is often
  less deterministic.

Before finishing, run the repository-supported linker target (`make test` in
this project), because plain `go test ./...` does not include the native image,
PDF, and WebP link objects.
