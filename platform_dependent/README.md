# Platform-dependent implementation

[`x11/`](x11/README.md) is the native X event and painting implementation. Production XGB,
Xproto and XGBUtil imports are confined here. The root executable selects it by default;
neutral terminal and frontend code use the interfaces in
[`platform_neutral/api`](../platform_neutral/api).

[`web/`](web/README.md) is an embedded HTTP/WebSocket/canvas implementation of
the same interfaces. `make st-web` selects it with the `backend_web` build tag;
its production dependency graph has no X11 backend or XGB packages.

[`fb/`](fb/README.md) implements Linux fbdev output, console keyboard input and
evdev mice. `make st-fb` selects it with `backend_fb`. It uses kernel ioctls and
mmap directly in Go; no X server, libdrm, libinput or GPM is needed.

Only native resource/protocol operations belong here. Terminal parsing,
key/mouse escape encoding, clipboard authorization, layout policy, software
composition and preview state remain outside the driver. Event handling and
painting share the existing native connection rather than opening a connection
per subsystem. The pre-existing XGBUtil auxiliary connection is unchanged.

See the [architecture and documentation index](../platform_neutral/README.md),
[build guide](../platform_neutral/BUILDING.md), and
[terminal protocol reference](../platform_neutral/stterminal/README.md).
