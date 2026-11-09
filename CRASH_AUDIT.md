# Crash audit — 2026-11-06

## Summary

Five independent runtime panics were reproduced and fixed. Two involve
selection logic also present in upstream suckless st. That supports investigating
shared terminal-core behavior, but does not establish the cause of the reported
intermittent crashes without a traceback from an affected session.

## Reproduced and fixed

| Trigger | Failure before the fix | Location / regression test |
| --- | --- | --- |
| Rectangular selection across blank leading columns of lines containing text | Trimming spaces makes `last == -1`; copying the selection indexes `line[-1]`. | `term/screen.go:GetSel`; `TestSelectionBlankRectangle` |
| Shrink the window while selecting, then continue/release the selection | Selection endpoints still reference removed rows/columns. | `term/resize.go:tresize`; `TestSelectionAfterShrink` |
| Delete the first cell of a wide character, then overwrite or redraw it | The remaining wide-character continuation at column zero makes character/cursor handling index column `-1`. | `term/screen.go:tsetchar`, `Draw`; `TestDeleteWideCharacterAtLeftEdge` |
| Shrink the window during animated-image playback | The next frame writes through the animation's obsolete rectangle into removed cells. | `term/resize.go:tresize`; `TestAnimationAfterShrink` |
| Open an image taller than the terminal, natively or with `fit-width` | The atlas allocates at most one screen, while the decoder writes the entire taller grid past its allocation. | `images.go:imageGrid`; `TestTallImageDecode` |

Grid-size changes now cancel selections and animations. Native/fit-width image
placements are cropped to the available screen height; `fit-contain` and
`fit-height` can display the whole image at a smaller scale.

### Relationship to suckless st

The [upstream st.c](https://git.suckless.org/st/file/st.c.html) inspected during
this audit has the same blank-selection access in `getsel()` and retains
selection endpoints across shrinking in `tresize()`. In C these accesses are
undefined behavior rather than Go bounds-check panics, so their manifestations
can differ. The user's installed C build was not reproduced or examined.

Current upstream already guards column zero in `tsetchar()` and `draw()`;
those guards were missing from this Go port and have been restored.

## Additional hardening

- **Glyph-cache growth:** every distinct rune/foreground/background combination
  previously stayed in memory indefinitely. True-color output could therefore
  grow memory throughout a session. Cached pixels are now bounded to 32 MiB,
  with at most 16,384 entries. Tests exercise both limits. Cache keys also include
  raster geometry, preventing undersized cached cells from being reused at a
  different size. No OOM kill was deliberately induced.
- **Interrupted PTY reads:** `main.go` previously exited immediately on any
  `unix.Read` error, including `EINTR`. Reads now use `os.File.Read`, which retries
  interrupted reads, and interrupted polls are retried. `POLLHUP` is allowed to
  drain through the read path. A signal-triggered premature exit was not
  reproduced during this audit.

## Resource-limit follow-up

The file/bitmap/control-string/clipboard risks below were subsequently addressed
with the centralized budgets in `limits/limits.go`; see README's resource-limit
table. Oversize rejections are recorded in a bounded private log under `/tmp/`.
Native image dimensions are checked before pixel decoding, PDF output uses a
bounded viewport, and retained animation/cache buffers are capped. Clipboard
INCR now recognizes its empty terminating chunk and filters the active property.
Grid/framebuffer allocations are bounded, and zero tab spacing falls back to 8.

## Remaining risks found by inspection

These require follow-up; the fixes above do not remove every possible failure.

1. **Native decoder internals:** input and output budgets do not impose a total
   process memory ceiling. Native working allocations (including decompression
   inside PDFs) and native memory-safety defects remain outside those budgets.
2. **Configuration validation:** allocation dimensions and tab spacing are now
   guarded, but other extreme settings such as timer durations need validation.
3. **Headless/debug and shutdown paths:** `dumpShellText` uses real X hooks
   without a connection and reads terminal state concurrently with its PTY
   reader. OSC title/clipboard requests can dereference a nil X connection.
   The X event loop also lacks a closed-state check on its `(nil, nil)` event
   branch, and `Close` leaves the secondary X connection open. These deserve
   separate lifecycle tests.

There are also intentional window exits on child termination and PTY EOF/errors.
Several use `os.Exit(0)`, so a disappearing window or exit status zero alone does
not identify a terminal panic. Shared X-server/session termination, external
signals, and OOM kills remain possible explanations when both implementations
show similar symptoms.

## Verification

- Each of the five panic cases failed before its fix and passed afterward.
- `make test` passed with the full native-library linker configuration.
- `go test -race ./term ./ptyutil` passed.
- Targeted frontend image/cache tests passed under `-race`.
- A 30-second structured edit/resize/selection fuzz run passed with 51,892
  executions. Its harness is retained as `FuzzTerminalEdits`.

## Capture an intermittent failure

Build with `make st`, then launch from another terminal:

```sh
GOTRACEBACK=all ./BINARIES/st 2>"${TMPDIR:-/tmp}/st-go-crash.log"
```

After that window disappears, run `echo $?` immediately in the launching shell.
Keep the log, exit status, and the action just before disappearance (selection,
resize, Unicode editing, preview, or idle). Go panics should include a traceback;
a signal kill may leave no traceback. Status 137 means SIGKILL and is not, by
itself, proof of an OOM kill.
