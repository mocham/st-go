# Terminal demo applications

The applications here are **TUI clients**, not desktop/display frontends.
They exchange keyboard/mouse data and terminal protocols with their host.
Display ownership, native clipboard access, windows and graphics surfaces
belong to st-go (or another host implementing the same capabilities).

## Applications

Run build and launch commands from the repository root. Generated executables
live in `BINARIES/`; see the [build guide](../platform_neutral/BUILDING.md).

| Application | Guide |
| --- | --- |
| Native Go modal editor, recovery journals, retained document preview, and lazy CJK input | [govim](govim/README.md), [roadmap](govim/ROADMAP.md), [Pinyin helper](pinyind/README.md) |
| Real Vim with a saved-document LaTeX preview | [vimtex](vimtex/README.md) |
| Native, smoothly scrolling PDF viewer | [zat](zat/README.md), [performance notes](zat/PERFORMANCE.md) |
| Filesystem/archive browser, document previews, and backend-aware openers | [file-browser](file-browser/README.md) |
| Immutable SQLite corpus browser and document search | [gols](gols/README.md) |

The [shell demos](../demo/README.md) include a Bash image/PDF viewer and file
browser. The [protocol reference](../platform_neutral/stterminal/README.md)
documents the shared host interface.

## Display-backend boundaries

* No demo imports XGB/X11 or connects to a display server. Govim's former native
  clipboard connection/helper was removed. Its host clipboard transport is OSC
  52, and ordinary editor registers/paste never query the host clipboard.
* `tui/readline` supplies shared, backend-neutral single-line editing for all
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
the file-browser audio adapter uses Linux ALSA, and GoLS uses SQLite/cgo. These
are OS/runtime constraints, not X11 dependencies. On the web backend the clients
and their PTYs still run on the Linux server, not inside the browser.

`TestNoDisplayBackendDependencies` guards demo source imports and display-server
environment probes. X11 integration tests belong beside the st-go frontend,
not inside the demo applications.

## Shared TUI navigation

The reusable `st-go/tui/navigation` package owns navigation state without
terminal I/O. `Panel` handles visibility, floating/pinned mode, and preview-width
calculations; `DefaultPanel()` starts visible and floating. `Selection` clamps
indexes and scrolls to keep the selected row visible. GoLS and file-browser
retain their own entries, TOC/filesystem actions, and redraw policies.
`Panel.LivePreviewWidth` also preserves the file-browser animation/sidebar
non-overlap invariant.
