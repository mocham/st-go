---
name: terminal-graphical-apps
description: Use when building terminal-based graphical applications with ANSI/VT sequences, mouse input, alternate screens, inline image/PDF protocols, incremental redraws, pane layouts, and external command integration.
---

# Terminal Graphical Applications

Use this workflow for full-screen terminal applications that combine text UI,
mouse interaction, inline graphics, previews, and window-control extensions.

In this repository, start with `demo/file-browser.sh`, `README.md`,
`term/escape.go`, `term/screen.go`, `images.go`, and `mouse.go`.

## Establish Protocol Boundaries

Separate the application into three layers:

- ANSI/VT control for cursor movement, colors, erasure, alternate screen, and
  terminal modes.
- An explicit graphics DSL for image/PDF/text placement and window operations.
- Application state for selection, viewport, click timing, opener policy, and
  redraw decisions.

Do not smuggle application state through cursor position. Explicit placement
commands should preserve the cursor.

## Terminal Lifecycle

For a full-screen application:

```sh
printf '\033[?1049h'  # alternate screen
printf '\033[?25l'    # hide cursor
printf '\033[?1000h'  # button reporting
printf '\033[?1006h'  # SGR mouse encoding
```

Always trap exit and restore in reverse order:

```sh
printf '\033[?1006l\033[?1000l\033[?25h\033[0m\033[?1049l'
```

Cleanup must be idempotent and run for normal exit, EOF, interruption, and
termination. Do not clear or invalidate graphics retained on the inactive
normal screen when entering or leaving the alternate screen.

## Layout In Cells

- Read live rows and columns from `stty size`.
- Treat ANSI cursor positions as one-based.
- Keep internal list indexes zero-based.
- Recompute pane geometry after `SIGWINCH`.
- Define a compact layout for very small terminal sizes rather than pretending
  the old dimensions still apply.
- Clamp all widths and heights before emitting erase or placement sequences.

For a two-pane browser, reserve explicit rows for header, metadata, list, and
status. Derive the preview rectangle from the remaining cells.

## Fixed-Rectangle Painting

A graphical terminal protocol should support placement independent of the
current cursor, for example:

```text
open PATH rect X Y W H fit-contain page N
```

Recommended semantics:

- X/Y are documented one-based terminal cells.
- W/H must be positive.
- Intersect the requested rectangle with the live terminal.
- Decode or classify first when preserving the old preview on failure is
  desired; clear first when failure must blank stale content. Choose and test
  one contract.
- Clear only the rectangle.
- Paint no cells outside it and never scroll.
- Preserve cursor position, attributes, and wrap state.
- Text truncates at W and stops after H rows.
- Native graphics clip to the rectangle.
- `fit-width` and `fit-height` use rectangle dimensions when supplied.
- `fit-contain` preserves aspect ratio within both dimensions and centers the
  result, leaving the rest of the cleared rectangle blank.
- PDF rendering should target the rectangle's pixel dimensions directly.

Keep legacy cursor-stream behavior unchanged when `rect` is omitted.

## Graphics Storage

In st-go, decoded graphics become normal terminal glyphs whose metadata points
to full cell-sized pixel blocks in an atlas. This makes graphics scroll and
erase like text, but creates lifecycle concerns:

- Repainting previews can append indefinitely unless blocks are reused or
  reclaimed.
- A global atlas reset invalidates glyphs retained on other screens.
- Clearing cells does not necessarily release pixel storage.
- Native and scaled cells need different source sampling.
- Fit calculations must account for cell pixel aspect (`cellWidth/cellHeight`),
  not just source-image aspect.
- When clipping a fit-height or fit-width result, sample against the full
  logical scaled dimensions rather than rescaling the clipped portion.

For long-running applications, prefer per-placement ownership or atlas reuse
over issuing a global clear.

## Incremental Redraw

Classify operations by redraw cost:

- Selection movement with unchanged viewport: old row, new row, metadata, and
  preview rectangle only.
- Viewport scroll: visible list pane plus metadata and preview.
- Directory change or resize: complete application layout.
- Image/PDF page change: preview rectangle only when the protocol supports
  fixed rectangles.

Use `CUP` plus `ECH` to erase a bounded row segment. `EL` erases to the end of
the line and can destroy an adjacent pane.

Do not repaint all chrome merely because a graphics decoder previously needed
full-screen fit behavior. Extend the protocol with bounded placement instead.

## Mouse Input

SGR reports have the form:

```text
ESC [ < Cb ; X ; Y M   press, motion, or wheel
ESC [ < Cb ; X ; Y m   release
```

Decode:

- `Cb & 3`: base button.
- `Cb & 32`: motion.
- `Cb & 64`: wheel.
- Added modifier bits: Shift 4, Alt 8, Ctrl 16.
- Coordinates are one-based cells.

Parser requirements:

- Bound sequence length.
- Use a short timeout for incomplete CSI reports.
- Ignore releases when selection is press-driven.
- Do not let a malformed sequence consume input forever.
- Reset pending double-click state after keyboard navigation, wheel movement,
  refresh, resize, or directory change.

Single click should select and preview. Double click should require the same
entry within a bounded interval and then activate it. Directory activation and
file opening should be separate operations.

## Window Collapse And Restore

For a click-restorable “minimized” application, keep the terminal mapped and
move/resize it to a small screen-edge rectangle:

```text
window remember browser-tag
window place bottom-left 0px 0px 24% 8% restore browser-tag
```

The frontend, not the application, must consume the restore click before mouse
reporting. It must also consume the matching motion/release. A real X11
iconified window cannot receive clicks in its own client area.

Applications should:

- Use a bounded/reusable tag rather than leaking one tag per launch.
- Forget temporary tags during cleanup.
- Render a compact “click to restore” state after the collapse resize.
- Redraw normal layout after the restore `SIGWINCH`.
- Handle disabled window operations without becoming permanently collapsed.

Geometry operations should accept explicit units (`px`, ratio, or `%`) and
document corner/boundary anchor offset directions.

## File-Type Icons And Width

Provide built-in symbols for common classes such as parent, directory,
symlink, image, PDF, text, archive, audio, video, source code, config,
executable, and default file.

Configuration rules:

- Parse JSON in the host application, not Bash.
- Export resolved scalar values to the child environment.
- Keep a separate user override namespace so one-shot environment overrides
  can beat JSON.
- Distinguish unset from explicitly empty.
- Classify symlinks before following target type.
- Use MIME/shebang fallback for extensionless files when practical.

Terminal display width is not string length. Combining characters, CJK text,
emoji, and multi-codepoint icons can occupy a different number of cells than
Bash `${#value}` reports. Either restrict icons to one printable one-cell rune
or use a width-aware helper and truncate by cells.

Never emit raw filenames as control text. Quote or sanitize ESC, C0 controls,
newlines, and carriage returns before drawing.

## External Openers

Treat opener configuration as trusted shell code, but never interpolate a
filename into that code. Export values separately:

```sh
FILE=$path NAME=$name BROWSER_DIR=$dir bash -c "$OPEN_CODE"
```

Redirect opener stdin/stdout/stderr away from the terminal UI unless the user
explicitly requests an in-terminal command. Run long-lived GUI openers in a
background shell so the input loop remains responsive.

## DSL Parser Caveats

- Split statements and quoted arguments deliberately. If semicolons are split
  before quote parsing, paths containing semicolons cannot be represented.
- Define whether quotes support escapes; do not imply shell quoting semantics
  if the tokenizer does not implement them.
- Validate every numeric operand before mutating the screen or window.
- Bound dimensions before multiplying cells by pixel size.
- Keep the protocol one-way unless replies are explicitly framed and tested.
- Gate clipboard and window operations independently where possible. A single
  broad security switch can accidentally enable unrelated OSC capabilities.
- DCS protocols generally do not pass transparently through tmux without an
  explicit passthrough mechanism.

## Testing

Use PTYs for shell-application tests and terminal-core tests for protocol
semantics.

Cover:

- Rectangle text truncation, clearing, cursor preservation, and outside-cell
  invariants.
- Raster/PDF fit dimensions and pixel sampling.
- Failed preview behavior.
- Single click, wheel, double click, and incomplete SGR input.
- Complex opener functions and filenames containing spaces/control bytes.
- Icon defaults, partial JSON overlays, explicit empty values, and environment
  precedence.
- Narrow terminals and restore-triggered resize.
- Geometry remember/place/restore and consumed mouse press/release.
- Repeated image changes for atlas growth.
- Alternate-screen entry/exit with graphics on the normal screen.

In this repository run `bash -n`, `shellcheck` when available, `git diff
--check`, and `make test`. Plain `go test ./...` lacks the required static
native linker objects.
