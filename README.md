# st-go

st-go is a Go terminal with X11, Linux framebuffer, and web-browser backends.
It combines the [suckless st](https://st.suckless.org/) terminal engine with
retained document previews, rectangular widgets, and optional terminal tabs.

## Build and run

```sh
make st          # X11: BINARIES/st
make st-fb       # Linux framebuffer/virtual console: BINARIES/st-fb
make st-web      # CGO-free web server/client: BINARIES/st-web

./BINARIES/st
./BINARIES/st-fb -fb /dev/fb0 -tty /dev/tty -mouse auto
./BINARIES/st-web -port 8080
```

Native builds statically link FreeType and media libraries. `st-web` needs only
Go and Make; its browser assets, PDF.js, and font are embedded. See the
[build guide](platform_neutral/BUILDING.md) for requirements.

`st-fb` runs on an active foreground Linux text VT with access to its framebuffer
and input devices. It uses fbdev, console, and evdev interfaces directly from Go,
with retained damage and paced scanout. See the
[framebuffer guide](platform_dependent/fb/README.md) for device setup and controls.

`st-web` prints a private session URL. The server owns the PTYs and terminal
state; the browser draws Canvas text, decodes images, and renders PDFs with
PDF.js. Browser reconnects restore the current session. See the
[web guide](platform_dependent/web/README.md) for remote access and clipboard use.

## Architecture

```text
applications <-> PTY <-> terminal engine <-> shared frontend / widgets
                                                    |
                              +---------------------+---------------------+
                              |                     |                     |
                          Linux fbdev              X11             HTTP/WebSocket
```

- The engine owns the ANSI/VT parser, cells, selection, damage, and OSC/DCS
  protocols. The frontend owns PTY scheduling, input encoding, layout, resolved
  text geometry/colors, and preview parameters.
- Semantic scene operations feed browser Canvas and a native raster adapter.
  Native glyph caches, image atlases, and preview bitmaps still live in the
  shared frontend; X11/framebuffer present those pixels.
- Widgets manage rectangular surfaces, visibility, focus, and client lifetimes.
  Backends own display resources and events. Browser media loading and PDF work
  run asynchronously, independently of ordered terminal text painting.
- Applications use terminal protocols for graphics, clipboard, and tabs.

## Applications

```sh
make file-browser govim
./BINARIES/st -e ./BINARIES/file-browser /some/path
./BINARIES/st-web -e ./BINARIES/file-browser --web /some/path
./BINARIES/st-web -e ./BINARIES/govim --web notes.md
```

See [terminal applications](demo-go/README.md) for the file browser, editors,
PDF/image/DjVu viewers, corpus browser, and OpenCode client.

## Documentation

- [Architecture and source layout](platform_neutral/README.md)
- [Building, tests, and packaging](platform_neutral/BUILDING.md)
- [Shared frontend, configuration, rendering, and limits](platform_neutral/frontend/README.md)
- [Terminal protocols](platform_neutral/stterminal/README.md)
- [Framebuffer](platform_dependent/fb/README.md), [X11](platform_dependent/x11/README.md), and [web](platform_dependent/web/README.md) backends
- [Reusable modules](functionalities/README.md)
