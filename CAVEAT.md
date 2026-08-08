# CAVEAT.md

Design choices, traps and pitfalls encountered while porting suckless `st` to Go
(`github.com/BurntSushi/xgb` + CGO FreeType2/poppler). Read this before touching
the X code or the terminal core.

---

## 1. xgb library traps

### 1.1 Checked vs unchecked requests — the CreateNotify race

`CreateWindowChecked` does a **round trip** (waits for the reply). A window
manager (gowinmgr) selects `SubstructureNotify` on the root and processes
`CreateNotify` **as soon as the window is created**. If you round-trip between
`CreateWindow` and the `WM_NAME`/`WM_CLASS` property writes, the WM reads an
**empty title** and the window is never tiled.

**Rule:** batch `CreateWindow` + all identity properties (`WM_NAME`, `WM_CLASS`,
`_NET_WM_PID`, ...) as **unchecked** requests with one `xc.Sync()` at the end —
matching st's `XCreateWindow` + `XStoreName` + `XFlush`. See `x11.go:NewTerminalOpts`.

### 1.2 `Sync()` is a round trip

`xc.Sync()` sends a GetInputFocus request and waits for the reply (see xgb's
`sync.go`). It is NOT `XFlush`. Avoid it in hot paths. xgb writes each request
straight to the socket (`writeBuffer`), so requests are flushed without `Sync`;
ordering within one connection is preserved. Reserve `Sync()` for points where
you must *see* the result (e.g. after a resize before drawing).

### 1.3 PutImage depth / byte layout

The screen is depth 24. `PutImage` with `depth=24` and **4 bytes per pixel**
(BGRA, `bytes_per_line = width*4`) succeeds; **3 bytes per pixel fails with
BadLength** (rows must be padded to 32-bit). Depth 32 fails with BadMatch.
Do not send `uint32ToBytes` of an ARGB framebuffer as if it were packed 24-bit —
the byte order must match the visual masks (`R=0xFF0000 G=0xFF00 B=0xFF`).

### 1.4 GetImage is unreliable for verification on shared/headless servers

On a shared / nested / non-composited X server, `GetImage` (and `import
-window`) often returns all black even for the root window. Do **not** trust
readback to verify rendering. Use `import -window root`, direct term-core
assertions, or a debug `log.Printf` in the code path instead.

### 1.5 Event masks

- **`PropertyChangeMask` is required** for INCR clipboard transfers: the chunks
  arrive as `PropertyNotify`. Without it, large pastes hang forever.
- Focus events: ignore `NotifyGrab`/`NotifyUngrab` (`Mode == 1 || Mode == 2`),
  and emit `\033[I` / `\033[O` focus reports when `DECSET 1004` (ModeFocus) is set.
- `StructureNotify` gives `ConfigureNotify` for resize handling.

### 1.6 SelectionNotify property

When answering a `SelectionRequest` with `property == None`, you must resolve
`property = target` and advertise the **resolved** property in the
`SelectionNotifyEvent`. Advertising `None` makes the requestor see a failed
transfer even though the data was written.

### 1.7 WM_DELETE_WINDOW

`Close()` destroys the window and closes the connection. The event loop must
then **stop** (`WaitForEvent` returns an error on a closed connection). Use an
atomic `isClosed` flag and return from the loop; otherwise it spins forever
logging "x: <nil>".

### 1.8 `#cgo LDFLAGS` forbids `-Wl,...`

Go rejects `-Wl,--gc-sections` in `#cgo LDFLAGS` (`invalid flag in #cgo LDFLAGS`).
Pass linker flags via `-ldflags "-linkmode external -extldflags '...'"` in the
Makefile instead.

### 1.9 Keyboard: `match()` and keysyms

- `match(mask, state)` is **exact** equality, or `mask == XKAnyMod` (0xFFFFFFFF);
  `XKNoMod` is 0. Callers must pre-mask `ignoremod` (numlock/Mod2).
- Resolve keysyms with the standard column rule (`col = 1` when Shift or Lock),
  falling back to column 0 on `NoSymbol`. `keysymsPer` can be > 2.
- Clipboard shortcuts (`Ctrl+Shift+C/V`) require the config to ship the
  shortcuts in the defaults — see §3.3.

### 1.10 GC created on the pixmap, used for PutImage on the window

A GC is depth-tied, not drawable-tied; a GC created on the backing pixmap works
for `PutImage`/`CopyArea` on the window of the same depth. The backing pixmap
is currently unused for blitting (we `PutImage` straight to the window).

---

## 2. Terminal emulation caveats

### 2.1 SGR 38/48 — always advance the parse index

`tdefcolor` advances the parameter index even when the color is rejected
(C: `tdefcolor(attr, &i, l)` mutates `i` unconditionally). The Go port must do
the same: `i = npar` **outside** the `if idx >= 0` block, or an invalid
`ESC[38;5;300m` re-parses `5` as blink and `300` as an unknown attr.

### 2.2 Mouse coordinates must be clamped before dividing

st's `evcol`/`evrow` do `LIMIT(x, 0, tw-1)` **before** `/ cw`. Without the
clamp, a click near/outside the bottom edge yields a row index beyond the
screen → `t.line[row]` panics (`index out of range`) and the window disappears.
Clamp to `[0, cols*cw-1]` / `[0, rows*ch-1]` first.

### 2.3 Never put the pty slave in raw mode yourself

The terminal reads from the **master**, which is always raw — it never needs the
slave in raw mode. If you set the slave to raw (ECHO/ICANON/OPOST off) before
spawning the shell, bash inherits it and **fails to re-enable its line
discipline** (a `SetRaw` here caused a full "no prompt / no echo / no newline"
regression). Let the shell configure its own termios. The child process setup is:
`setsid()` + `dup2(slave, 0/1/2)` + `ioctl(slave, TIOCSCTTY)` (Go: `Setsid:
true, Setctty: true, Ctty: 0`); bash handles the foreground pgrp itself.

### 2.4 `\n` vs `\r` in pty tests

In canonical mode a line is submitted by **Enter = `\r`**, not `\n`. Testing
`echo HI\n` only echoes the characters; `echo HI\r` actually runs the command.
Use `\r` when driving bash in a test.

### 2.5 pty size must match the real window (post-tiling)

st sizes the pty to the actual mapped window (`run()` waits for MapNotify, then
`cresize(w,h)` → `ttyresize`). If you size the pty from `cfg.Rows/Cols` while
the WM tiles the window to a different size, the child's TIOCGWINSZ is stale and
**vim lays out with a row offset** (line 11 at top, nothing after line 60).
Query the live window geometry (`actualRowsCols`) and set the pty before spawn.

### 2.6 Unset COLUMNS/LINES/TERMCAP in the child env

st's `execsh` does `unsetenv("COLUMNS"); unsetenv("LINES"); unsetenv("TERMCAP")`
so vim/shells use TIOCGWINSZ, not a stale inherited size. The Go child-env
builder must do the same (plus set `TERM`, `HOME`, `USER`, `LOGNAME`).

### 2.7 Focus cursor: redraw on FocusIn/FocusOut

st's event loop calls `draw()` after every event. On `FocusOut`, `MODE_FOCUSED`
is cleared and the cursor must immediately repaint as the 1px outline box.
Without an explicit `Redraw()` in the focus handlers, the cursor stays in the
focused shape until the blink timer fires (or never). Same for `DECSET 5`
(MODE_REVERSE) — redraw when reverse changes.

### 2.8 Keyboard lock & other mode gates

`DECSET 2` (ModeKbdLock) must make `kpress` return early. Several DECSET/DECRST
modes are only gated in the term core; the frontend hooks must mirror them.

### 2.9 Image DSL: paths, globs, pages

- The terminal resolves `open '<path>'` **relative to its own CWD**, not the
  shell's. Scripts must first send `setpwd '<dir>'` (the shell's `$PWD`), then
  relative paths work. `~` is expanded.
- Wildcards (`./*.png`) are expanded by the DSL too (like the shell would).
- PDF page navigation uses **modular arithmetic**: the script only sends `±1`
  counters (`page N`), and the terminal wraps `N mod pages` (negatives wrap to
  the end). This avoids any terminal→script response protocol.
- **Never have the terminal emit responses** (e.g. writing a page count back to
  the pty). It is fragile (echo/raw-mode interactions) and races the script's
  read. Keep the protocol one-way: script → terminal.

### 2.10 Draw/framebuffer

- `drawGlyphAt` must clamp the blit to the framebuffer edge (a wide glyph
  orphaned on the last column after a shrink would slice out of range).
- After a resize, `clearFramebuffer()` (st's `xresize` → `xclear`) so borders /
  skipped cells are background, not stale black.

---

## 3. Build / packaging caveats

### 3.1 Static linking

Fully static build: `go build -ldflags '-linkmode external -extldflags
"-static -lstdc++"'` (needs `-lstdc++` for the C++ poppler bridge). glibc emits
harmless NSS warnings (`getaddrinfo`, `getpwnam_r`) under `-static` — cosmetic.

### 3.2 Tiered builds: st-min / st-stb / st-pdf / st

Four Makefile targets link an increasing set of third-party libraries, all with
the SAME `.go` sources — the third-party libs are **not** in the cgo files'
`#cgo LDFLAGS`; they are passed via `-extldflags`:

| target | libs                      | extra objects/libs                        |
|--------|---------------------------|-------------------------------------------|
| `st-min`| freetype only            | dummy-stb.o dummy-webp.o dummy-pdf.o      |
| `st-stb`| + stb_image              | stb_image.o dummy-webp.o dummy-pdf.o      |
| `st-pdf`| + poppler                | stb_image.o pdf_bridge.o poppler.. dummy-webp.o |
| `st`   | all (+ libwebp)          | stb_image.o pdf_bridge.o poppler.. libwebp.a |

**Dummy objects.** When a library is dropped, its C symbols are provided by a
no-op `dummy-*.o` (from `third_party_wrapper/dummy-{stb,webp,pdf}.c`) that
returns failure. Because every cgo path already checks for NULL/0 and degrades
to "show nothing", the reduced binaries never crash — they just don't render
that format. Verified: `st-min`'s `decodeImage`/`decodeWebP`/`pdfPageCount`
return error/false/0 gracefully.

**pdf_bridge.cpp is NOT in the package dir** (it is in `third_party_wrapper/`)
so cgo does not auto-compile it; the Makefile compiles it to `pdf_bridge.o`
(only for st-pdf/st) and links the poppler libs via `-extldflags`.

**Traps:**
- cgo rejects `-Wl,...` in `#cgo LDFLAGS` (security); link flags belong in the
  Makefile's `-extldflags`.
- Order matters for static archives: put the object that *uses* a symbol before
  the `-l...` that provides it, and repeat `-lm` where `pow`/`floor` are needed
  (stb_image uses `pow`, libpng uses `floor`). poppler needs freetype again.
- `go test ./...` also needs the libs linked: use `make test` (which passes the
  full EXTRA flags) instead of a bare `go test`.

### 3.2 Minimal poppler (bloat removal)

poppler's **glib API** drags in glib/gobject/ffi/cairo/pixman/lcms/openjpeg/
turbojpeg. Use the **C++ API** (`poppler::page_renderer::render_page()` → raw
BGRA) instead — it only needs freetype + zlib (+ libpng for embedded images).
Build flags: `-DENABLE_GLIB=OFF -DENABLE_QT5=OFF -DENABLE_QT6=OFF
-DENABLE_CAIRO=OFF -DENABLE_LCMS=OFF -DENABLE_GPGME=OFF -DFONT_CONFIGURATION=generic
-DENABLE_LIBOPENJPEG=none -DENABLE_DCTDECODER=none`.

### 3.3 Config must be self-contained

`config.json` is embedded with `go:embed`; `config.Default()` unmarshals it.
The binary is **standalone** — no external `config.json` is required (a file
next to the executable is honored if present). The **clipboard shortcuts live
only in the config**, so if the defaults ever lack `Shortcuts`, `Ctrl+Shift+C/V`
silently stop working. Keep the embedded config complete.

### 3.4 Makefile third-party rebuilds

`third_party/` is gitignored and rebuilt on demand (freetype, stb, zlib, libpng,
poppler). Pitfalls seen:
- Use `tar --strip-components=1` (GNU) — `--transform` is not portable.
- After copying libs around, `make` can spuriously rebuild due to timestamp
  races (lib copied in the same second as its dependency). `touch` the outputs.
- `make distclean` removes all of `third_party/`; a full rebuild takes a while
  (downloads + compiles poppler).

---

## 4. Verification guidance

- **Do not** verify pixels with `GetImage`/screenshots on this shared server
  (§1.4). Assert on the term core (`LineText`, `LineAt`, glyph `Fg`/atlas), or
  the framebuffer.
- Test the pty/shell path directly: open a pty, `ptyutil.Start(bash -i)`, write
  `echo HI\r`, expect `echo HI` + `HI` + a fresh prompt.
- For PDF page wrap: `ImageDecode(data, fit, page)` with `page = 2` on a 2-page
  PDF must equal `page = 0`'s atlas content; `page = -1` equals the last page.
