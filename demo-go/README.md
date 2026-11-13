# Terminal applications

These applications run inside st-go and use terminal protocols for graphics,
clipboard, and tabs. Each is a separate Go module; builds go to `BINARIES/`.

| Application | Purpose |
| --- | --- |
| [file-browser](file-browser/README.md) | Files, archives, document previews, and backend-aware openers |
| [govim](govim/README.md) | Modal editor, recovery, document previews, and Pinyin |
| [opencode-tui](opencode-tui/README.md) | OpenCode V2 client with rich Markdown/math responses |
| [zat](zat/README.md) | PDF navigation, search, outline, and page text |
| [zat-djvu](zat-djvu/README.md) | DjVu viewer with outline, text, and search |
| [goiv](goiv/README.md) | Image fit, zoom, pan, and sibling navigation |
| [gov](gov/README.md) | MP4 playback through st-fb's bundled decoder; pause, seek, and audio controls |
| [gols](gols/README.md) | Read-only SQLite corpus browser |

```sh
make file-browser govim zat
export PATH="$PWD/BINARIES:$PATH"
./BINARIES/st -e ./BINARIES/file-browser /some/path
./BINARIES/st-web -e ./BINARIES/file-browser --web /some/path
```

`--web` in file-browser, govim, and opencode-tui enables browser-native HTML/SVG
previews. Otherwise document generation uses the shared Go PDF renderer.
Media presentation belongs to the host; applications do not open display
connections. On web, applications and audio still run on the Linux server.

## Presentation scheduling and document caching

The apps keep using the PTY region/HTML protocols. st-web turns published HTML
into immutable HTTP resources and caches them in the browser; app code does not
need HTTP resource IDs or to resend documents on resize or reconnect. PDF/image
viewers keep using retained regions and the host's media delivery.

Refresh leases are optional: `DCS refresh INTERVAL_MS DURATION_MS ST` accelerates
pending framebuffer output during time-sensitive work. Request/renew only while
that work is active, and cancel with `DCS refresh 0 0 ST` on completion or terminal
handoff. Ordinary input-driven updates already wake the host. A lease does not
generate redraws or keep hidden tabs presenting. Hidden-tab suspension affects
presentation; PTY input, protocol replies, and app jobs continue running.

## Building and testing

File-browser and GoLS use pure-Go SQLite. These applications build without cgo
except `zat-djvu`, which statically links its DjVu decoder. Shared components
live in [`functionalities/`](../functionalities/README.md).

```sh
GOWORK=off go -C demo-go/govim test ./...
make test-tui
```

See [building and tests](../platform_neutral/BUILDING.md).
