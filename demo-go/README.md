# Terminal demo applications

The applications here are **TUI clients**, not desktop/display frontends.
They exchange keyboard/mouse data and terminal protocols with their host.
Display ownership, native clipboard access, windows and graphics surfaces
belong to st-go (or another host implementing the same capabilities).

## Applications

Each application is a separate Go module, independent of the root graphical
host. Generated executables live in `BINARIES/`; see the
[build guide](../platform_neutral/BUILDING.md).

```sh
go -C demo-go/govim build -o ../../BINARIES/govim .
go -C demo-go/govim test ./...
GOWORK=off go -C demo-go/zat test ./...   # standalone, without workspace help
GOWORK=off go -C demo-go/goiv test ./...  # retained raster image viewer
make test-tui                           # all demo/shared client modules
make test-zat-djvu                     # bundled static DjVuLibre profile
```

The checked-in `go.work` supports cross-module development. Every module also
has local replacements for its dependencies so `go build`, `go test`, and
`go mod tidy` work from its directory with the workspace disabled. Root
`go test ./...` intentionally excludes nested modules; `make test` includes them.
The sibling `demo-go` module contains architecture/dependency guards only.

| Application | Guide |
| --- | --- |
| Minimal OpenCode V2 chat client with a retained Markdown/LaTeX response pane | [opencode-tui](opencode-tui/README.md) |
| Native Go modal editor, recovery journals, retained document preview, and lazy CJK input | [govim](govim/README.md), [roadmap](govim/ROADMAP.md), [Pinyin frontend](../functionalities/googlepinyin/README.md) |
| Real Vim with a saved-document LaTeX preview | [vimtex](vimtex/README.md) |
| Native, smoothly scrolling PDF viewer | [zat](zat/README.md), [performance notes](zat/PERFORMANCE.md) |
| DjVuLibre-backed DjVu viewer with retained page rasters, outline, text, and search | [zat-djvu](zat-djvu/README.md) |
| Retained raster image viewer with fit, zoom, drag, and sibling navigation | [goiv](goiv/README.md) |
| Filesystem/archive browser, document previews, and backend-aware openers | [file-browser](file-browser/README.md) |
| Immutable SQLite corpus browser and document search | [gols](gols/README.md) |

The [shell demos](../demo/README.md) include a Bash image/PDF viewer and file
browser. The [protocol reference](../platform_neutral/stterminal/README.md)
documents the shared host interface.

## Display-backend boundaries

* No demo imports XGB/X11 or connects to a display server. Govim's former native
  clipboard connection/helper was removed. Its host clipboard transport is OSC
  52, and ordinary editor registers/paste never query the host clipboard.
* `functionalities/tui/readline` supplies shared, backend-neutral single-line editing for all
  app-owned command/search/path prompts. Vimtex delegates editing to child Vim.
* Images/PDFs, region placement, mouse events, and window geometry are terminal
  protocol requests. The descriptions of X11 surfaces in zat's performance
  notes describe the **st-go frontend**, not the zat application.
* Govim's LaTeX/Markdown preview uses `tui/preview` and the pure-Go `latexpdf`
  engine in-process, with save hooks instead of a file watcher. Its retained PDF
  panel uses the same terminal region protocol as vimtex, without child Vim.
* External openers are explicitly configured child commands, not a built-in
  dependency on a particular desktop. Vimtex/TeX tools are external programs.

Other portability boundaries remain: entry points use Unix TTY/PTY/signal APIs;
file-browser and GoLS use bundled SQLite/cgo. File-browser has **no ALSA/native
audio linkage**: MP3 playback is an authenticated terminal request, handled by
st-go's `functionalities/audio` service. Metadata remains available on hosts
without audio support. Universal readline Pinyin and its dictionary frontend
are pure Go and run in process.
These are OS/runtime constraints, not graphical dependencies. On the web backend
the clients, PTYs, and native audio still run on the Linux server.

`zat-djvu` is the deliberate native-code exception among the document clients:
it links DjVuLibre for DjVu page, hidden-text, and outline APIs but still has no
display-server dependency. Reusable modules live under [`functionalities/`](../functionalities/README.md),
with unchanged import paths (`st-go/tui`, `st-go/limits`, etc.). The terminal
engine/contracts are separate modules under `platform_neutral/`; no TUI module
requires the root graphical host.

`TestNoDisplayBackendDependencies` guards source imports and display-server
environment probes. `TestApplicationModulesDoNotDependOnGraphicalHost` checks
actual transitive package dependencies with `GOWORK=off`. X11 tests belong beside the st-go frontend,
not inside the demo applications.

## Shared TUI navigation

The reusable `st-go/tui/navigation` package owns navigation state without
terminal I/O. `Panel` handles visibility, floating/pinned mode, and preview-width
calculations; `DefaultPanel()` starts visible and floating. `Selection` clamps
indexes and scrolls to keep the selected row visible. GoLS and file-browser
retain their own entries, TOC/filesystem actions, and redraw policies.
`Panel.LivePreviewWidth` also preserves the file-browser animation/sidebar
non-overlap invariant.
