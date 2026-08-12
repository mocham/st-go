---
name: st-go-file-browser
description: Use when implementing, debugging, or testing demo/file-browser.sh together with the st-go terminal, including DCS graphics, SGR mouse input, path prompts, external openers, geometry collapse/restore, icons, PTY tests, and X11 integration.
---

# st-go File Browser

Use this skill for changes to `demo/file-browser.sh` or any st-go behavior the
browser depends on.

## Read These Files First

- `demo/file-browser.sh`: browser state, layout, rendering, input, completion,
  opener dispatch, and compact mode.
- `term/escape.go`: DCS parser, fixed-rectangle `open`, window commands, and
  geometry capability validation.
- `images.go`: raster/PDF grid sizing, scaling, image-cell atlas allocation,
  and framebuffer image-cell drawing.
- `mouse.go`: X11 mouse events and SGR terminal reports.
- `window_geometry.go`: tagged geometry, anchor resolution, EWMH/raw requests,
  and click-to-restore.
- `event.go`: X event loop, `ConfigureNotify`, redraw, and PTY resize.
- `shell.go`: exported `ST_GO_*` environment variables.
- `config/config.go` and `config/config.json`: browser icon defaults and
  geometry permissions.
- `regression_test.go`: browser PTY and live-X integration tests.
- `term/regression_test.go`: DCS parser and rectangle invariants.

Also load the `golang-x11` and `terminal-graphical-apps` skills when changing
the frontend or general terminal protocol.

## Architecture

The browser is a Bash application running inside the terminal it controls.
There are three protocol layers:

1. ANSI/VT sequences draw text chrome, erase bounded regions, position the
   cursor, use colors, and enter the alternate screen.
2. DCS commands ask st-go to paint text/images/PDFs in fixed cell rectangles
   and request tagged window geometry.
3. SGR mouse reports and keyboard CSI sequences flow from st-go back through
   the PTY to the Bash input loop.

Do not debug only one side. A missing preview can be a Bash command-generation
bug, DCS parser bug, decoder failure, screen-model placement bug, atlas issue,
or framebuffer drawing issue.

## Browser State And Modes

Normal browser state includes:

- Current physical directory, entry array, selected index, and viewport top.
- Terminal rows/columns and derived pane geometry.
- Current PDF page.
- Last-click index/time.
- Graphic-preview and geometry-tag state.

Path entry is modal. While active, normal shortcuts must not run. It owns:

- Editable path buffer and logical cursor.
- Match array, popup selection, and popup viewport.
- Abort and resize state.

When adding another mode, use a nested modal input loop or explicit mode
dispatch. Do not let the normal `q`, arrows, mouse handlers, or opener path run
through accidentally.

## Drawing Rules

- ANSI cursor positions and DCS rectangle X/Y are one-based cells.
- Bash list indexes are zero-based.
- Use `CUP` plus `ECH` to erase a bounded pane row. Do not use `EL` when a pane
  exists to the right.
- Text, image, and PDF previews belong inside the computed left rectangle.
- Explicit rectangle painting must not move the terminal cursor or scroll.
- On selection movement, redraw the old/new rows, metadata, status, and preview
  only. Redraw the full layout after directory change or resize.
- Popup overlays may cover browser content temporarily; call `render_all` once
  when leaving the mode.
- Sanitize every filename/path before ANSI output. The browser uses `printf %q`
  so control characters cannot become terminal commands.
- Bash string length is not terminal display width. Restrict configurable icons
  to one-cell characters or introduce a width-aware helper before supporting
  emoji/CJK icon sequences.

## DCS Commands Used By The Browser

The protocol frame is:

```text
ESC P statement; statement ESC \
```

Fixed preview placement:

```text
open '/safe/symlink' rect X Y W H
open '/safe/symlink' rect X Y W H fit-contain
open '/safe/symlink' rect X Y W H fit-contain page N
```

The browser creates a safe temporary symlink and sends that path rather than a
raw user filename. This avoids DCS quoting/semicolon limitations.

Geometry:

```text
window auth TOKEN remember TAG
window auth TOKEN place bottom-left 0px 0px 24% 8% restore TAG
window auth TOKEN forget TAG
```

`ST_GO_GEOMETRY_TOKEN` is a per-terminal capability exported to the child.
Commands without the matching token are ignored. If changing this protocol,
update parser tests, child environment tests, browser output tests, and README
examples together.

## Input Parsing

### Normal Mode

- Arrow keys arrive as CSI sequences.
- SGR mouse reports are `ESC [ < Cb ; X ; Y M/m`.
- Bound sequence length and use short timeouts so malformed input cannot block
  the application forever.
- Wheel events use `Cb & 64`; motion uses `Cb & 32`.
- Double click requires the same entry within the timeout. Reset pending click
  state after keyboard movement, wheel movement, refresh, resize, or directory
  change.

### Path Prompt

- `/` starts with a buffer containing `/`.
- Backspace can delete that slash, switching naturally to relative paths.
- `Ctrl+A` and Home move to offset zero; `Ctrl+E` and End move to the end.
- Left/Right move by Bash characters.
- `read -n 1` returns an empty string for newline. Treat `''` as Enter in
  addition to CR/LF.
- ESC, `Ctrl+C`, and complete mouse reports abort the mode.
- A `SIGWINCH` trap only marks state. Use a short read timeout in modal mode so
  resize can be processed even when no key is typed.

Path resolution rules:

- Leading `/` is absolute.
- Other values are relative to browser `DIR`, including `file`, `./file`, and
  `../../file`.
- `*`, `?`, and bracket expressions are Bash pathname patterns.
- Do not use `eval`, command substitution, variable expansion, tilde expansion,
  or shell quote parsing on user text.
- Perform intentional unquoted glob expansion only with `IFS` empty,
  `nullglob` enabled, and inherited glob options controlled/restored.
- Preserve matches in arrays so spaces and newlines remain one path.
- Exact existing paths win over prefix popup matches.
- With multiple wildcard matches, keep the prompt open until Up/Down chooses a
  candidate.

## Icons And Openers

Resolved icon config is exported as `ST_GO_FILE_BROWSER_ICON_*`. Explicit
`ST_FILE_BROWSER_ICON_*` variables override JSON. Preserve explicitly empty
icons; do not replace them with defaults.

Classify in this order:

1. Synthetic parent.
2. Symlink.
3. Directory.
4. Specialized file extension/MIME class.
5. Executable.
6. Default file.

The default opener sends text/source/config formats through:

```sh
"$ST_GO_EXECUTABLE" vim "$FILE"
```

Other files use `xdg-open`. Custom `--open`/`ST_FILE_BROWSER_OPEN` code receives
`FILE`, `NAME`, and `BROWSER_DIR`. Never interpolate a filename into shell code;
pass it through environment variables.

## Fast Static Checks

Run these before integration tests:

```sh
bash -n demo/file-browser.sh
shellcheck demo/file-browser.sh
git diff --check
```

If ShellCheck flags the intentional pathname-expansion array, keep a narrow
`shellcheck disable=SC2206` comment on that exact line and explain why `IFS` and
glob options make it safe. Do not disable the warning globally.

## Running The Browser Live

Build the full terminal because the minimal variants omit one or more graphic
formats:

```sh
make st
./st -e ./demo/file-browser.sh /path/to/files
```

Useful variants:

```sh
./st -e ./demo/file-browser.sh --hidden /path
ST_FILE_BROWSER_ICON_PDF=P ./st -e ./demo/file-browser.sh /path
./st -e ./demo/file-browser.sh --open '
  printf "%s\n" "$FILE" >> /tmp/browser-open.log
' /path
```

Do not test graphics inside tmux unless a DCS passthrough mechanism is enabled.
tmux normally consumes the DCS framing.

## Capturing Raw Browser Output

To debug generated escape sequences independently of st-go, run the script
under a PTY and save stdout. A plain pipe is useful for deterministic key input
but does not reproduce signals/mouse perfectly:

```sh
tmp=$(mktemp -d)
touch "$tmp/a.txt"
printf 'q' | bash demo/file-browser.sh "$tmp" > /tmp/browser.raw
od -An -tx1c /tmp/browser.raw
```

Search the capture for:

- `ESC P open` rectangle commands.
- `window auth ... remember/place/forget`.
- `ESC[?1000h` and `ESC[?1006h` enablement.
- Unexpected `DCS clear`, which can invalidate an image atlas retained by
  another screen.
- Full-screen erase sequences during operations expected to be incremental.

For interactive reproduction, use a real PTY (`script`, the Go `ptyutil`
helper, or the regression-test harness). Piped stdin does not create a
controlling terminal, so `Ctrl+C` behaves differently.

## PTY Test Pattern

Browser tests belong in `regression_test.go`. The standard pattern is:

1. Create a temporary filesystem fixture.
2. Open a PTY with `ptyutil.Open`.
3. Set deterministic dimensions with `ptyutil.SetWinSize`.
4. Start `bash demo/file-browser.sh ...` with the slave as stdin/stdout.
5. Drain the master continuously; otherwise output backpressure can block the
   browser.
6. Send keyboard or SGR mouse bytes through the master.
7. Observe output under a mutex or verify an opener marker file.
8. Use deadlines rather than assuming one fixed sleep is always enough.
9. Kill/reap the child and close the PTY in cleanup.

Example input bytes:

```go
master.Write([]byte("\x1b[B"))          // Down
master.Write([]byte("\x1b[<0;60;9M"))  // left press
master.Write([]byte("/\x7fsub/*.go"))   // prompt, delete '/', relative glob
master.Write([]byte("\x01"))            // Ctrl+A
master.Write([]byte("\x05"))            // Ctrl+E
master.Write([]byte("\r"))              // Enter
```

When testing `Ctrl+C`, use a controlling PTY and distinguish an interrupted
`read` from EOF. To prove abort returned to normal mode, send `r` and wait for a
new `Refreshed` output token instead of relying on `Signal(0)`, which also
succeeds for zombies.

## Terminal-Core Tests

Put protocol invariants in `term/regression_test.go` with mock hooks:

- Rectangle parser operands and one-based-to-zero-based conversion.
- Text clipping, remainder clearing, and cursor preservation.
- Image options and centered placement.
- Window geometry parsing and capability-token rejection.

Use frontend tests for actual raster/PDF decoding and image atlas offsets.
Prefer screen-model/framebuffer assertions over X framebuffer readback.

## Live X11 Tests

Use live-X tests only for behavior that cannot be proven through hooks:

- Geometry query/request and `ConfigureNotify`.
- PTY resize after WM-adjusted geometry.
- Restore-click press/release suppression.
- Window title/class/property behavior.

Under a WM, geometry requests are policy requests. EWMH support, decorations,
tiling rules, and resize increments may alter them. Under Xvfb without a WM,
the raw `ConfigureWindow` fallback should be testable directly.

If a live test hangs, check whether it is waiting for an X event that the
current environment/WM never generates. Always use bounded waits.

## Symptom-Driven Debugging

### Preview Is Blank

1. Capture output and verify `open ... rect ...` was emitted.
2. Confirm the temporary preview symlink exists and points to a readable file.
3. Check st-go logs for `dsl: failed to decode`.
4. Verify the full `st` target includes the relevant decoder.
5. Inspect `Term` cells for `ImageRune`.
6. Verify atlas offsets and framebuffer dimensions.

### Preview Erases The File List

- Check for legacy `fit-height` without `rect`.
- Check for `EL` (`CSI K`) emitted from the preview pane.
- Verify rectangle width does not cross `LIST_X`.

### Mouse Does Nothing

- Verify mouse modes were enabled.
- Capture the exact SGR report.
- Confirm coordinates use one-based cells and land inside list bounds.
- Check Shift is not forcing local terminal selection.
- Check geometry restore is not intentionally consuming the first click.

### Restore Click Leaks To Browser

- Frontend restore interception must run before mouse reporting.
- Consume press, motion, and matching release.
- Confirm only the next independent click reaches the PTY.

### Path Prompt Does Not Submit

- Remember that Bash `read -n 1` reports newline as an empty string.
- Check exact path versus completion pattern behavior.
- Confirm the initial slash was deleted when testing a relative path.
- Capture popup matches and selected index.
- Ensure incomplete CSI parsing did not swallow Enter.

### Wildcards Split Filenames

- Ensure `IFS` is empty during pathname expansion.
- Keep matches in an array.
- Do not use command substitution or line-oriented `compgen` output.

### Browser Freezes

- Ensure the test drains PTY output.
- Bound every CSI/mouse read by length and timeout.
- Use a timed read in modal mode so resize flags are processed.
- Look for an opener accidentally running in the foreground.

### Memory Grows While Browsing Images

- `imageAtlas` is append-only until reset/reclamation.
- Count atlas growth per preview/page.
- Do not solve this by globally clearing an atlas while inactive screens retain
  image glyphs. Implement ownership/reuse or screen-safe reclamation.

## Required Verification

The root package needs native static objects, so plain `go test ./...` may fail
at link time. Use:

```sh
go test ./config ./ptyutil ./term
make test
make st
bash -n demo/file-browser.sh
shellcheck demo/file-browser.sh
git diff --check
```

`make test` is the authoritative suite for the integrated root package. The
static glibc linker warnings are expected; undefined image/PDF/WebP symbols are
not.

After editing this skill, restart OpenCode so it reloads project skills.
