# Platform-dependent implementation

`x11/` is the current native event and painting implementation. Production XGB,
Xproto and XGBUtil imports are confined here. The root executable selects it;
neutral terminal and frontend code use the interfaces in
[`platform_neutral/api`](../platform_neutral/api).

Only native resource/protocol operations belong here. Terminal parsing,
key/mouse escape encoding, clipboard authorization, layout policy, software
composition and preview state remain outside the driver. Event handling and
painting share the existing native connection rather than opening a connection
per subsystem. The pre-existing XGBUtil auxiliary connection is unchanged.

See the [split notes](../platform_neutral/README.md) for the first-round scope
and verification commands. No additional backend is implemented by this move.
