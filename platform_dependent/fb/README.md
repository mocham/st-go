# Linux framebuffer backend

```sh
make st-fb
./BINARIES/st-fb -fb /dev/fb0 -tty /dev/tty -mouse auto -e /bin/bash
```

For the minimal profile, build `make st-fb-min` and run `BINARIES/st-fb-min`
with the same options. It uses the Go FreeType port and Go raster codecs, including
static WebP, and omits PDF rendering and animated WebP. This target and its status
font generator are CGO-free and require no C compiler or native libraries.

Run from an active foreground Linux text VT with permission to use its framebuffer
and input devices. PTYs and already-owned graphics consoles are rejected. The
driver is pure Go; the executable uses the shared font/media rendering frontend.

## Devices and permissions

- `-fb`: packed truecolor 16/24/32-bit fbdev output. Existing resolution is used;
  st-go does not perform modesetting.
- `-tty`: the active numbered text VT, normally `/dev/tty`.
- `-mouse auto`: discover mice/touchpads and recover from hotplug. Comma-separated
  device paths select specific devices; `none` disables them.
- `-mouse-grab`: exclusively grab selected mouse devices while the VT is active.

Device permissions are managed by the system; there is no logind/seatd leasing.
Avoid another display owner such as GPM on the same framebuffer.

## Rendering and input

Terminal content, previews, caret, system bar, and pointer are retained separately.
An independent presenter coalesces damage on a **60 Hz software clock** and copies
only changed rows from a completed RAM buffer to scanout. Input and PTY processing
can continue during hardware copies. This reduces redundant painting but does
not guarantee tear-free output: fbdev provides no portable synchronized page flips.

Keyboard input uses the Linux console keymap, including common modifiers,
navigation keys, Unicode, and basic dead-key/Compose input. Restart after changing
the console keymap. Full XKB/IME behavior is not implemented.

With tabs enabled, **Alt+N** (number keys 1–9) selects the N-th PTY/tab, starting
with Alt+1 for the first. If N exceeds the open tab count, one new shell tab is
appended and focused, subject to the eight-tab limit.

**Alt+0** focuses the system bar and highlights its selected button. **Tab** cycles
through visible buttons, **Shift+Tab** cycles backward, and **Enter** presses the
selected button. **Escape** returns to the previous terminal; Alt+1…Alt+9 selects
or opens a terminal tab as above.

### Touchpads

Supported gestures: one-finger motion, tap/click/drag, and two-finger scrolling.
Legacy absolute axes and protocol-B multitouch slots are supported. Palm rejection,
inertia, pinch/rotate, and multi-finger shortcuts are not implemented.

## System bar

The bottom-right bar shows time, battery, audio output/volume, and available
brightness controls. It also offers:

- **Terminal-plus:** open a focused shell tab when tabs are enabled.
- **Camera:** select two opposite corners and save a crop to `/tmp/screenshot.png`.
- **S:** toggle shared clipboard synchronization; `?` means storage is unavailable.

Volume buttons adjust hardware playback volume; clicking the output selector
switches Speaker/Headphone routes. `system_bar.initial_volume` defaults to 33
and accepts values up to 50. `ST_GO_AUDIO_OUTPUT_CARD` overrides card selection.
Brightness controls use login1 through `dbus-send` when available.

Tabs share the bottom band with the bar, or sit above it on narrow screens.
Clipboard/PRIMARY is process-local unless synchronization is enabled. Desktop
window placement/collapse requests are unsupported.

## VT ownership and shutdown

### Idle and activity scheduling

The input loop blocks in `poll` on keyboard/mouse descriptors and an `eventfd`
for queued work, signals and shutdown. Device hotplug uses inotify, with a
30-second rescan fallback. System-bar information is polled together every
30 seconds; UI changes reuse that snapshot. Maintenance pauses on inactive VTs
and refreshes on return.

Pending terminal updates use 16, 33, 100, 250, then 1000 ms coalescing intervals
after 0, 0.5, 2, 5, and 15 seconds without user input. Idle terminals have no
refresh tick. Known animations use their frame deadlines. Hidden tabs with
pending text/animation work have a two-second grace period (33/100/250 ms), then
suspend until the tab is shown; static hidden tabs sleep immediately. PTY reads and protocol replies
always continue.

Applications can request a temporary maximum update interval, renewing as needed:

```sh
printf '\033Prefresh 33 120000\033\\'  # up to 30 FPS for two minutes
printf '\033Prefresh 0 0\033\\'        # cancel
```

Intervals must be 16–1000 ms and leases 1–120000 ms. Requests affect pending
framebuffer text updates, not keyboard readiness or system-bar polling; they
never create periodic redraws on a static screen or prevent hidden suspension.
Expiry is automatic, including after application exit. Other backends retain
their own presentation policy.

### Console lifecycle

Ctrl-Alt-F1…F12 switches VTs; Ctrl-Alt-Backspace closes st-fb. VT release pauses
input/scanout and return repaints retained content. Normal exit and handled
signals restore console modes. Do not change framebuffer modes while running.

After a crash, use another console or SSH to restore the affected VT:

```sh
kbd_mode -a -C /dev/ttyN
stty -F /dev/ttyN sane
```

If it remains in graphics/process-controlled mode, also restore `KD_TEXT` and
`VT_AUTO`; a terminal escape reset alone cannot do that.

### Native video handoff

The file browser requests video playback through the terminal. st-fb hands its
real VT to mpv, pauses its own input/scanout, and restores ownership afterward.
It does not launch DRM playback on the browser's PTY. Defaults use DRM output,
`auto-copy` hardware decoding, ALSA `plughw:1,0`, and 33% mpv volume.
See [video configuration](../../demo-go/file-browser/README.md#framebuffer-video).

## Testing

```sh
CGO_ENABLED=0 go test ./platform_dependent/fb
go test -race ./platform_dependent/fb
```

These tests use simulated devices. Real device behavior requires a foreground-VT
smoke test. See [building](../../platform_neutral/BUILDING.md) and
[shared configuration](../../platform_neutral/frontend/README.md#configuration).
