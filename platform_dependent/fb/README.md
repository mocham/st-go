# Linux framebuffer backend

```sh
make st-fb
# From a foreground Linux text VT with access to the relevant devices:
./BINARIES/st-fb -dir /path/to/project
./BINARIES/st-fb -fb /dev/fb0 -tty /dev/tty -mouse auto -e /bin/bash
```

The executable is `BINARIES/st-fb`, selected by `backend_fb` at build time.
The driver itself builds with `CGO_ENABLED=0` and adds no Go modules or C
libraries. The executable keeps the existing static FreeType, image/PDF/WebP
decoders and embedded fonts. All clients still use terminal ANSI/OSC/DCS text;
there is no framebuffer-specific client channel or renderer rewrite.

## Devices and permissions

* `-fb /dev/fb0`: existing fbdev mode; packed **truecolor 16/24/32 bpp** only.
  Channel bitfields, native byte order, line padding and visible offsets are
  honored. Invalid/overlapping formats, unsupported palettes/planar modes,
  oversized mappings and grids fail before taking over the console. Screen,
  retained pixels and saved mappings obey the existing 64 MiB per-buffer limit.
  No DRM/KMS modesetting or resolution changes are attempted.
* `-tty /dev/tty`: a real, currently active, numbered Linux text VT. PTYs and
  already-owned graphics/process-controlled consoles are refused. The selected
  console must belong to the launcher's foreground process group (no background
  jobs or direct SSH takeover of another VT). The selected
  framebuffer must back that console (`FBIOGET_CON2FBMAP`). `-g`/`-i` cannot
  override the physical screen dimensions.
* `-mouse auto`: scan up to 16 accessible evdev mice/touchpads, rescan every two
  seconds, and recover after unplug/reconnect. Explicit comma-separated paths
  fail early if unavailable at startup; `-mouse none` disables discovery.
  Stable `/dev/input/by-id/*-event-mouse` paths are preferable to event numbers.
* `-mouse-grab`: optional exclusive mouse grabs. Grabs are released on VT
  release and file close. No keyboard evdev grab or external mouse daemon is
  required; keyboard events arrive through the foreground console.

Device permissions must already permit access. No chmod, setuid, service
changes or automatic elevation is performed. Avoid granting blanket access to
all input devices: that can expose other users' keystrokes. This first backend
does not integrate logind/seatd device leasing; permissions and foreground VT
ownership must be managed by the launcher/system. Do not run GPM or another
display owner against the same active framebuffer.

## Rendering and input

Uniform `api.Painter` damage spans copy into retained, cursor-free terminal
pixels. Preview surfaces are retained separately. A second presentation layer
holds the text caret, system bar, and software mouse/touchpad pointer; moving or
hiding an overlay restores its old rectangle from retained content without
rerasterizing glyphs. Unchanged overlays are reapplied only where content damage
intersects them. Clean rows are not bridged. Composition uses rectangle
intersections and row spans, with a fast converter for common 32-bit layouts,
rather than checking every layer at every content pixel. No second full-screen
overlay framebuffer is allocated.

The software mouse/touchpad pointer uses a **2× scale** (18×32 pixel bounds),
with its hotspot at the unchanged top-left tip.

A backend-owned bar overlays the **top-right corner**, above terminal and preview
drawing and below the mouse pointer. It displays local `YYYY-MM-DD HH:MM:SS` and,
when readable, `BAT N%` from the first present system battery's `capacity` under
`/sys/class/power_supply`. It refreshes every **30 seconds**, repaints only its
old/new area and is restored on VT return. Missing batteries are silently omitted.
It uses anti-aliased **28 px Monaco** text in soft light green (`#C4DFB5`) on a
muted dark blue-purple background (`#2D2B46`). The tiny glyph atlas is generated
from the bundled `Monaco_Linux.ttf` using the existing FreeType build and kept as
generated Go source; `make st-fb` regenerates it when its inputs change. Drawing
remains pure Go and needs no system fonts or packages. No terminal rows are
reserved, and the bar does not change application input or `BAR_DATA`.

Console input uses **K_MEDIUMRAW**, with snapshots of the kernel's normal,
Shift, AltGr and Shift+AltGr maps and Unicode accent table. Common modifiers,
Caps/Num Lock, function/navigation keys, UTF-8 characters, basic dead-key/Compose
input and terminal shortcuts are supported. Advanced sticky/locking layout
groups, arbitrary console function-string macros and full IME/XKB semantics
are not implemented; keypad digits use Num Lock/navigation rather than a full
application-keypad emulation. Lock LEDs are not changed. Restart after changing
the system console keymap.

Evdev packets commit at `SYN_REPORT`, combine relative X/Y, track per-device
button state and aggregate held buttons across mice. High-resolution wheel
events accumulate 120 units per detent and are not double-counted with legacy
wheel reports. `SYN_DROPPED` discards incomplete packets and queries current
button state. Shift/Ctrl/Alt come from the console keyboard state. Pointer events
go through the neutral frontend's selection, region hit-testing and terminal
mouse encoder, not directly to applications.

### Touchpads

Absolute evdev touchpads with `BTN_TOOL_FINGER`/`BTN_TOUCH` are discovered by the
same `-mouse auto` or explicit-device option. Legacy `ABS_X/Y` and multitouch
protocol-B slots are supported (up to 32 slots). Direct touchscreens/tablets
are not treated as touchpads.

- One finger moves the pointer relatively; landing/lifting/replacing fingers
  establishes a new baseline rather than jumping to an absolute screen location.
- Physical buttons support clicking and dragging. A short single-finger tap
  produces a left click; physical clicks and larger movements suppress the tap.
- Two-finger motion scrolls vertically/horizontally without moving the pointer.
- Physical axis resolution controls sensitivity, with a range-based fallback;
  fractional motion/wheel deltas accumulate across input packets.
- Dropped packets cancel gestures and resynchronize physical buttons. VT switches,
  native-video handoff and device removal clear gesture/button state.

This is basic touchpad handling, not libinput feature parity. Acceleration,
tap-and-drag, palm rejection, inertia, pinch/rotate, and multi-finger shortcuts
are not implemented. Devices need legacy axes or protocol-B slots; protocol-A
devices without legacy pointer emulation are unsupported. No new system library,
input daemon, or package is required.

Clipboard/PRIMARY text is local to this process; terminal authorization and
bracketed paste remain unchanged. There is no desktop clipboard service.
Titles, icon titles, `BAR_DATA` and desktop geometry/collapse requests are no-ops.

## VT ownership and shutdown

**Ctrl-Alt-F1…F12** requests a switch; **Ctrl-Alt-Backspace** closes the host.
The backend saves termios, keyboard mode and VT/display modes before entry.
Startup errors roll back even a partial takeover. Shell exit, input failure and
SIGINT/SIGTERM/SIGHUP/SIGQUIT return through cleanup. All restorations are
attempted even if one ioctl fails. Saved visible pixels are restored only when
the owned VT is active and its framebuffer layout remains valid.

VT_PROCESS release/acquire signals stop scanout writes while another VT owns
the display, clear held input, release mouse grabs, and repaint retained output
on return. A changed framebuffer layout on return closes safely rather than
using stale geometry/mappings. Do not change modes with `fbset` while running.

### Native video handoff

The browser's authenticated
[`native-video` DCS request](../../platform_neutral/stterminal/README.md#native-video-handoff)
uses a narrow optional neutral API, not a client-owned X/VT connection or a
general shell-exec channel.
Only fixed mpv DRM/decoder options, regular media/config paths, the selected ALSA
PCM and a bounded software-volume value are accepted. The host derives
`/dev/ttyN` from the kernel-validated console number and checks foreground/active
ownership again before handing it over.

During playback st-fb stops console/evdev reads and scanout writes, releases grabs,
restores translated keyboard/termios and `VT_AUTO`, and launches mpv with that
real controlling VT and its own foreground group. mpv can install its VT switcher
and manage DRM master while switching consoles. Exit, startup failure, stopped
players and host shutdown all return through foreground/mode recovery. A changed
fbdev mode fails closed; returning while another VT is active retains output
without writing it, and repaint waits for reacquisition. Keyboard/mouse state is
flushed/reset before resuming. No permissions are changed and no new VT is taken.

The [browser guide](../../demo-go/file-browser/README.md#framebuffer-video) covers
helper discovery, audio-device overrides, and volume. The default command is
`mpv --vo=drm --hwdec=auto-copy --ao=alsa --audio-device=alsa/plughw:1,0 --volume=33 -- FILE`;
it does not probe the MP3 player or force sample format/rate/channels.

SIGKILL, a native crash and power loss cannot guarantee restoration. Keep another
console/SSH recovery path available when first testing hardware. To recover a
stuck console, restore its keyboard mode with `kbd_mode -a -C /dev/ttyN` or
`kbd_mode -u -C /dev/ttyN`, then restore termios with `stty -F /dev/ttyN sane`.
If it remains in graphics/process-controlled mode, explicitly restore `KD_TEXT`
and `VT_AUTO`; a terminal escape reset alone is not sufficient. For example,
from another console/SSH session, with permission to control the affected VT
and Python 3 already available (replace `/dev/ttyN` with the correct device):

```python
import fcntl, os
fd = os.open("/dev/ttyN", os.O_RDWR | os.O_NOCTTY)
fcntl.ioctl(fd, 0x4B3A, 0)        # KDSETMODE: KD_TEXT
fcntl.ioctl(fd, 0x5602, bytes(8)) # VT_SETMODE: VT_AUTO
os.close(fd)
```

This is manual crash recovery, not a Python dependency of the backend. No shell
command is automatically invoked by the backend.

## Testing

```sh
CGO_ENABLED=0 go test ./platform_dependent/fb
go test -race ./platform_dependent/fb
make test
```

Default tests never open a real framebuffer, change a real VT or grab real input.
They use byte-backed scanout and injected console operations for format/stride
and bounds checks, dirty rows, preview retention, pointer restoration, keymap
translation, evdev framing/scroll/drop recovery, multi-mouse button ownership,
VT transitions and partial-startup cleanup. Shared frontend process tests check
cleanup on shell exit/spawn failure and the child's initial physical PTY size.
Real device/driver behavior still requires a foreground-VT hardware smoke test.

See [shared configuration and limits](../../platform_neutral/frontend/README.md)
and the [build guide](../../platform_neutral/BUILDING.md).
