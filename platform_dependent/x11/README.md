# X11 backend

X11 is the default build-time backend:

```sh
make st
./BINARIES/st
./BINARIES/st -dir /path/to/project -e /bin/bash
```

Run commands from the repository root. [Reduced media builds](../../platform_neutral/BUILDING.md#build-targets)
are available as `st-min`, `st-stb`, and `st-pdf`.

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

See [shared usage/configuration](../../platform_neutral/frontend/README.md),
[backend ownership](../README.md), and [X11 implementation caveats](../../CAVEAT.md#1-xgb-library-traps).
