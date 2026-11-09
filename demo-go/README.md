# Terminal demo applications

The applications here are **TUI clients**, not desktop/display frontends.
They exchange keyboard/mouse data and terminal protocols with their host.
Display ownership, native clipboard access, windows and graphics surfaces
belong to st-go (or another host implementing the same capabilities).

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

Other portability boundaries remain: the current entry points use Unix TTY/
PTY/signal APIs; the file-browser audio adapter uses Linux ALSA, and GoLS uses
SQLite/cgo. These are not X11 dependencies, but a direct web/framebuffer port
will need host adapters for those facilities. They are not removed by this
display-backend separation.

`TestNoDisplayBackendDependencies` guards demo source imports and display-server
environment probes. X11 integration tests belong beside the st-go frontend,
not inside the demo applications.
