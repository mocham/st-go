---
name: terminal-graphical-apps
description: Use when building terminal-based graphical applications with ANSI/VT sequences, mouse input, alternate screens, inline image/PDF protocols, incremental redraws, pane layouts, and external command integration.
---

# Terminal Graphical Applications

Keep application state separate from ANSI drawing and terminal-owned graphics.
Use `platform_neutral/stterminal/README.md` to locate protocol implementations;
reuse clients from `functionalities/tui/` where possible.

## Layout and lifecycle

- Derive rectangles from live PTY dimensions and update on resize. Protocol
  placement uses one-based cells; Go model rectangles are zero-based.
- Preserve cursor/scroll state for fixed previews. Erase bounded rows with ECH
  rather than clearing an adjacent pane. Redraw changed content incrementally.
- Match alternate-screen, cursor, mouse, and termios setup with idempotent cleanup.
- Keep image/resource ownership valid across alternate screens, scrollback,
  selection changes, and asynchronous completion.
- Prefer retained regions for independently scrolling previews. HTML/PDF work
  must not block terminal input or ordered text presentation.

## Input and external commands

- Parse fragmented UTF-8, CSI/SGR mouse input, and framed replies with bounds and
  deadlines. Keep prompt state modal and reject stale response IDs.
- Sanitize filenames before ANSI output; shell quoting and DCS quoting differ.
  Use safe stable paths for deferred resource loading.
- Respect terminal cell widths for combining/wide characters and icons.
- Use backend metadata and capabilities for native operations and tab requests.
  Never infer display ownership from a filename or `DISPLAY`.
- Treat opener configuration as trusted code, passing filenames separately.
  In-terminal children need explicit screen/termios handoff and restoration.

## Verification

Use parser/hook tests for placement and reply invariants, PTYs for interactive
lifecycle, and backend integration tests for presentation. Cover resize, repeated
replacement, stale work, and exit cleanup. See `platform_neutral/BUILDING.md` for
module, native, and browser test targets.
