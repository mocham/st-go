# st-go

st-go is a pure-Go Linux framebuffer compositor, event system, and rectangular
widget host bundled with a faithful Go port of the suckless
[`st`](https://st.suckless.org/) terminal. The same terminal engine and software
rasterizer can be presented directly on a Linux framebuffer, in an X server, or
in a web browser.

The project is configured to produce fully static, standalone executables. The
framebuffer compositor and its console/evdev drivers remain Go code and do not
require an X server, Wayland compositor, DRM/KMS stack, libinput, or GPM.

## Architecture

The implementation separates terminal behavior from display ownership:

```text
applications <-> PTY <-> st terminal engine <-> shared software frontend
                                                   |
                         +-------------------------+------------------------+
                         |                         |                        |
                    Linux fbdev                   X11                HTTP/WebSocket
```

- The terminal engine owns the ANSI/VT parser, screen model, selection, damage
  tracking, and OSC/DCS protocols. It preserves `st` behavior while exposing
  display-neutral hooks instead of X11 objects.
- The shared frontend owns PTYs, key and mouse escape encoding, glyph/image
  rasterization, retained image/PDF regions, clipboard policy, and terminal
  lifecycle.
- The pure-Go widget layer manages rectangular retained surfaces, placement,
  visibility, focus, pointer capture, and independent client lifetimes. Pixel
  coordinates remain local to each widget; stale work from an old placement or
  visibility generation is rejected.
- A selected backend owns only native resources, event decoding, and final
  presentation. Terminal applications continue to communicate through terminal
  text protocols and never open framebuffer, X11, or browser connections.

This is intentionally not a desktop scene graph. Visible terminal widgets are
rectangular and non-overlapping, which keeps clipping, focus, damage, and input
ownership explicit and makes the same management layer usable by all three
backends.

## Linux framebuffer compositor

`st-fb` talks directly to Linux fbdev, virtual-terminal, and evdev interfaces by
using `mmap` and kernel ioctls from Go. It accepts packed truecolor 16-, 24-, and
32-bit framebuffers and honors their channel bitfields, native byte order, line
stride, and visible offsets. It does not change the video mode: mode selection
and hardware scanout remain the kernel driver's responsibility.

### Retained composition

Terminal cells, retained previews, widget surfaces, the text caret, and the
software pointer are kept as separate retained state. A paint operation updates
the relevant backing pixels and records damage; it does not synchronously write
to `/dev/fb0`. Keeping small moving elements separate means caret and pointer
movement only damages their old and new bounds instead of rerasterizing terminal
glyphs or large previews.

Paint batches commit related terminal pixels and caret state atomically. Opaque
retained surfaces allow covered terminal pixels to be skipped during composition,
and old preview pixels remain visible until an asynchronous replacement is ready.
Per-resource limits bound framebuffers, decoded media, glyph caches, control
strings, and retained strips before allocation.

### Damage and presentation

An independent presenter sleeps while the display is clean and coalesces dirty
rectangles on a 60 Hz software frame clock. Damage is a bounded batch rather than
an unbounded queue of obsolete frames. The presenter composes a complete result
into one reusable RAM back buffer in the framebuffer's native format, then
bulk-copies only damaged rows to mapped scanout.

Hardware copies happen outside the producer mutex. If terminal output or input
arrives during a copy, that newer damage remains pending for the next frame;
finishing an older batch cannot erase it. This render/commit/present split keeps
PTY parsing and input responsive while preventing deliberately half-composed
frames.

The 60 Hz clock is pacing, not hardware vertical synchronization. fbdev exposes
no portable page-flip contract, so a RAM back buffer reduces redundant work and
partial composition but cannot guarantee tear-free scanout. st-go deliberately
does not add DRM/KMS modesetting merely to hide that limitation.

### Events and console ownership

Keyboard input comes from the active Linux virtual terminal in `K_MEDIUMRAW`
mode and uses snapshots of the kernel keymap and accent table. Mice and touchpads
are read from evdev; packets commit at `SYN_REPORT`, high-resolution wheel deltas
are accumulated, held buttons are tracked across devices, and automatic discovery
recovers from hotplug. All events pass through the same neutral focus, selection,
widget hit-testing, and terminal mouse-encoding paths used by the other backends.

Startup validates the foreground VT and its framebuffer mapping before taking
control. VT release pauses input and scanout, drains in-flight presentation, and
releases device state; reacquisition repaints the retained frame. Normal exit and
handled signals restore termios, keyboard mode, VT mode, and display ownership.
See the [framebuffer guide](platform_dependent/fb/README.md) for device permissions,
supported touchpad behavior, lifecycle caveats, and hardware testing.

## X11 and web presentation

The X11 build uses pure-Go XGB protocol clients rather than libX11, Xft, or
fontconfig. The web build keeps the PTY, parser, codecs, and rasterizer on the
Linux server and sends bounded PNG damage rectangles to an embedded browser
client; the browser does not contain a second terminal implementation. Browser
canvas dimensions follow device pixels so glyphs and images are not stretched.

Because presentation is behind shared contracts, terminal rendering, retained
media, widgets, tabs, and application protocols behave consistently across
framebuffer, X11, and web hosts.

## Build and run

```sh
make st          # X11: BINARIES/st
make st-fb       # Linux framebuffer/virtual console: BINARIES/st-fb
make st-web      # embedded web server/client: BINARIES/st-web

./BINARIES/st
./BINARIES/st-fb -fb /dev/fb0 -tty /dev/tty -mouse auto
./BINARIES/st-web -port 8080
```

The framebuffer command must run from an active foreground text VT with access
to its framebuffer and selected input devices. `st-web` prints a private session
URL; its server-side PTY survives browser reconnects. See the
[build and packaging guide](platform_neutral/BUILDING.md) for toolchain
requirements, reduced media profiles, static-link details, and verification.

## Documentation

- [Architecture and source layout](platform_neutral/README.md)
- [Shared frontend, configuration, rendering, and limits](platform_neutral/frontend/README.md)
- [Terminal protocols](platform_neutral/stterminal/README.md)
- [Framebuffer backend](platform_dependent/fb/README.md)
- [X11 backend](platform_dependent/x11/README.md)
- [Web backend](platform_dependent/web/README.md)
- [Terminal applications and demos](demo-go/README.md)
