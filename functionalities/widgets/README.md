# Rectangular widgets

`st-go/widgets` is a standalone, pure-Go host adapter over the neutral
`st-go/platform_neutral/api` contracts. It contains no terminal parser, native
display connection, tab system, tab bar or automatic tiling policy.
The frontend's optional `terminal_tabs` policy builds on these primitives.

`Wrap(native)` registers the existing full-client terminal. `NewWidget` creates
another scoped terminal or bar host with its own committed pixel buffer. All
widget uploads and preview geometry use **local pixels**; the adapter clips
and translates destinations without changing source-buffer addressing. The
native backends implement `PositionedPainter`, so no shared frontend canvas is
allocated. Backend scanout/network staging remains a native implementation
detail.

Call layout/visibility/focus methods outside client callback locks: they notify
clients under those locks. Clients must still serialize their own rendering and
terminal state. Visible terminal rectangles are expected not to overlap; there
is no general overlap/blending compositor. The existing framebuffer system bar
is the deliberate exception: an opaque, pointer-transparent overlay above
content/caret and below the software pointer, in the bottom-right corner.

Visibility is not focus. Visible widgets may paint; only the focused terminal
gets keyboard/paste events. Pointer drags retain their original owner and local
coordinates. Hiding a widget discards new presentation, unmaps/hides its retained
previews, and cancels input capture without redirecting the matching release.
Background PTYs and protocol replies must keep running. `WidgetPresentation`
and `GuardedSurface` reject preview work from old visibility/placement
generations. Committed pixels remain available during synchronized output.
`Hidden` creates a widget without input/presentation; `WidgetSurfaceFactory`
also creates its previews unmapped/hidden from the outset. Private committed
preview pixels survive placement/resize independently of uncommitted work.

Clipboard protocol replies keep their requester; interactive paste additionally
requires the original focus generation. Titles/geometry/native video use the
focused owner. Closing one terminal releases its resources; closing the last
terminal closes the native window. `Run` waits for native console restoration.
`WidgetWindow.RunWindow` instead waits for the whole window, even if the widget
used to obtain it closes first. Window hooks and `PostWindow` serialize optional
layout/shortcut policy outside widget callback locks; `SetAutoResize` hands
geometry ownership to that policy without replacing PTYs.
`WindowBarLayout.PassiveBarBounds` exposes visible pointer-transparent bars for
optional layout reservations. Changes queue the window resize/layout hook in
the native event domain, outside widget/client locks.

The frontend exposes `NewTerminalWidget`, `Terminal.Widget`, `StartSession` and
`Run` for these ownership boundaries. Its raster, mouse state, image atlas, PDF
jobs and PTY are instance-owned. The existing process-wide font bundle/glyph
cache is shared with complete raster keys and serialized FreeType calls.

```sh
GOWORK=off go -C functionalities/widgets test -race ./...
```
