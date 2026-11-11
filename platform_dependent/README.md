# Platform-dependent implementation

[`x11/`](x11/README.md) is the native X event and painting implementation. Production XGB,
Xproto and XGBUtil imports are confined here. The root executable selects it by default;
neutral terminal and frontend code use the interfaces in
[`platform_neutral/api`](../platform_neutral/api).

[`web/`](web/README.md) serves semantic scenes and media over HTTP/WebSocket.
The browser owns Canvas text, image decoding, and PDF.js rendering.
`make st-web` selects `backend_web` and builds without cgo or XGB.

[`fb/`](fb/README.md) implements Linux fbdev output, console keyboard input and
evdev mice. `make st-fb` selects it with `backend_fb`. It uses kernel ioctls and
mmap directly in Go; no X server, libdrm, libinput or GPM is needed.

Backends own display resources, event decoding, retained presentation, and
backend-specific composition. Terminal parsing, key/mouse escape encoding,
clipboard authorization, and application layout policy belong to shared code.
Native raster adaptation remains in the shared frontend; browser media resources
and document rendering belong to the web backend. X11 preview surfaces share
the driver's connection and event loop.

See the [architecture and documentation index](../platform_neutral/README.md),
[build guide](../platform_neutral/BUILDING.md), and
[terminal protocol reference](../platform_neutral/stterminal/README.md).
