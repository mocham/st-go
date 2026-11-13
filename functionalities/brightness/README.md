# Shared backlight control

`st-go/brightness` is the original framebuffer system-bar implementation,
shared by `st-fb` and `gxw`. It is CGO-free.

- `Read()` discovers the first valid backlight in stable sysfs directory order
  and returns its raw brightness and maximum.
- `Change(delta)` reads the current value, clamps the raw delta to `0..maximum`,
  and invokes `org.freedesktop.login1.Session.SetBrightness` for the active
  session through `dbus-send`, with the original two-second timeout.

Unavailable devices or failed operations return `ok=false`.
