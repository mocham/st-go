# Platform-neutral terminal

The shared engine, frontend, and host contracts serve X11, Linux framebuffer,
and web-browser backends. Applications communicate using ANSI/OSC/DCS terminal
protocols.

## Documentation

- [Building, targets, testing, packaging, and licensing](BUILDING.md)
- [Frontend usage, configuration, rendering, and resource limits](frontend/README.md)
- [Terminal engine and protocols](stterminal/README.md)
- [Backend guides](../platform_dependent/README.md)
- [Go applications](../demo-go/README.md)

## Layout

| Directory | Responsibility |
| --- | --- |
| [`stterminal/`](stterminal/README.md) | Screen model, ANSI/VT parser, DCS dispatch, selection, animation, and damage tracking |
| [`api/`](api/) | Host callbacks, semantic scenes, media/document handles, pixel surfaces, widgets, and geometry contracts |
| [`frontend/`](frontend/README.md) | PTYs, terminal input encoding, layout, scene generation, native raster adaptation, and retained preview state |
| [`sharedclipboard/`](sharedclipboard/) | Opt-in cross-process clipboard storage using pure-Go SQLite |
| [`../platform_dependent/`](../platform_dependent/README.md) | X11, framebuffer, and web resource/event/presentation implementations |
| [`../functionalities/`](../functionalities/README.md) | Independent modules for widgets, TUI helpers, document generation, audio, PTYs, configuration, and limits |
| `../term/` | Type-alias/forwarding compatibility module for `st-go/term` consumers |
| `../main.go`, `../backend_*.go` | Executable wiring and build-time backend selection |

`go.work` selects local modules. Per-consumer replacements also support
standalone `GOWORK=off` builds. X11 is the default; select `backend_web` or
`backend_fb` for another backend, using only one backend tag at a time.

## Dependencies and ownership

```text
main -> frontend -> neutral api contracts <- X11, web or fbdev
             |
             +--> stterminal
```

The engine owns canonical terminal cells. The frontend resolves coordinates,
Unicode sequences, widths, colors, decorations, and preview parameters into
`api.SceneUpdate` operations. A browser Canvas renderer and the frontend's
native raster adapter consume those operations. `api.Painter` / `api.Surface`
carry native pixel updates; scene and media interfaces carry semantic state and
opaque resource handles.

Native font caches, framebuffers, image atlases, and preview bitmaps remain in
the shared frontend. Web surfaces retain semantic state, serve original
supported image/PDF bytes, and delegate decoding/rendering to browser APIs and
bundled PDF.js. Web builds exclude FreeType, Poppler, and native codecs.

Drivers keep native events private and dispatch callbacks through the widget
adapter to each client's lock. PTY lifecycle, mouse state, and retained preview
jobs are instance-owned. The native process-wide font bundle/glyph cache is
shared and serialized. Generation checks reject stale asynchronous presentation.

The default layout has one terminal, plus the framebuffer's system bar when
applicable. The optional `terminal_tabs` policy creates a bottom tab bar on the
first successful spawn request; see [terminal tabs](frontend/README.md#lazy-terminal-tabs).
Physical sizing precedes PTY startup, and child exit returns through host cleanup
so console modes and display resources can be restored.

Platform-neutral means **display-backend-neutral**: the host still uses Linux
PTYs and signals. The browser is a remote presentation client for that Linux
server. Architecture tests reject backend imports in neutral production code.
