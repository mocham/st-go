# X11 backend

```sh
make st
./BINARIES/st -dir /path/to/project -e /bin/bash
```

X11 is the default backend. It uses pure-Go XGB for window/input/clipboard
operations and the shared native renderer for text and media. Retained preview
surfaces reuse pixels during scrolling instead of uploading entire panes.
Keyboard lookup uses the X server's mapping; no XIM is required.

Clipboard/PRIMARY selection and large transfers are supported. Cross-process
clipboard synchronization starts disabled and uses the authenticated
[shared-clipboard protocol](../../platform_neutral/stterminal/clipboard_sync.go).

[Window geometry requests](../../platform_neutral/stterminal/escape.go)
support placement and click-to-restore. Window-manager policy may override them.

See [build profiles and tests](../../platform_neutral/BUILDING.md),
[configuration](../../platform_neutral/frontend/README.md#configuration), and
[backend ownership](../README.md).

For driver work, read [XGB caveats and regression traps](CAVEATS.md).
