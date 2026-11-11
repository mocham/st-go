# X11 backend

X11 is the default build-time backend:

```sh
make st
./BINARIES/st
./BINARIES/st -dir /path/to/project -e /bin/bash
```

Run commands from the repository root. [Reduced media builds](../../platform_neutral/BUILDING.md#build-targets)
are available as `st-min` and `st-pdf`.

The driver uses pure-Go [`xgb`](https://github.com/BurntSushi/xgb), without a
runtime libX11 dependency. It owns X resources and native event decoding,
keyboard lookup, clipboard/PRIMARY selection ownership and INCR transfers,
window-manager interactions, and pixel/pane presentation. Keyboard handling
does not require XIM and uses the Linux-console keymap.

The shared frontend owns PTYs, terminal key/mouse encoding, glyph/media
rendering, and retained preview state. [Window geometry requests](../../platform_neutral/stterminal/README.md#window-geometry)
use the terminal's authenticated DCS protocol; application clients do not open
their own X connections. Selection and clipboard retain the terminal's normal
authorization and bracketed-paste policy.

Shared clipboard synchronization starts disabled and has no X11 toolbar or
configuration switch. A trusted client must use the authenticated
[`clipboard-sync` DCS](../../platform_neutral/stterminal/README.md#shared-clipboard)
with the per-PTY `ST_GO_CLIPBOARD_SYNC_TOKEN`. Enabled windows coordinate through
the existing SQLite cache. One enabled window per X display temporarily owns a
private X selection and projects cache updates to `CLIPBOARD`/`PRIMARY`; XFixes
owner notifications import external X selections. The role moves automatically
when that window closes or disables synchronization. It is not a daemon and
does not remain after the participating X11 sessions exit.

See [shared usage/configuration](../../platform_neutral/frontend/README.md),
[backend ownership](../README.md), and [X11 implementation caveats](../../CAVEAT.md#1-xgb-library-traps).
