# Rectangular widgets

`st-go/widgets` adapts backend hosts into independent terminal/bar rectangles.
It manages placement, visibility, focus, pointer capture, and client lifetimes.
The frontend's optional tab policy builds on these primitives.

Native widgets retain committed pixels; web widgets retain semantic scenes and
media views. Coordinates stay local to each widget. Generation checks discard
stale preview work after placement or visibility changes, and hidden PTYs keep
running without presenting.

Only the focused widget receives keyboard/paste input. Pointer drags retain their
original owner. Closing one widget leaves others alive; the last closes the host.
Visible terminals normally do not overlap; the framebuffer system bar is an
intentional overlay.

For APIs and lock/lifecycle contracts, see [`host.go`](host.go),
[`layout.go`](layout.go), and [`surface.go`](surface.go).

```sh
GOWORK=off go -C functionalities/widgets test -race ./...
```
