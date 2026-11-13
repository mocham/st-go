# gxw — fullscreen tabbed X11 session

`gxw` is a standalone, deliberately small window manager and session launcher.
Each ordinary window fills the screen between a top tab strip and a bottom
system bar. Select a tab with **Alt+1…9 / Alt+0**, **Alt+N**, or a mouse click.

The executable is **CGO-free**. XGB speaks the X11 protocol directly; the
native Go FreeType port rasterizes text into local pixel buffers. Monaco is
embedded, matching st's default font. Configured TrueType/TTC fonts and per-glyph
CJK fallbacks use the same rasterizer.

## Build and launch

```sh
make gxw st-min
# From a Linux text console, after logging in:
./BINARIES/gxw

# Choose the terminal, font size, or first application explicitly:
./BINARIES/gxw -terminal 'xterm -fa monospace -fs 14' -font-size 20
./BINARIES/gxw -- /usr/bin/firefox
```

Install **Xorg** and **xinit** for session startup. Run `gxw` directly; it invokes
`xinit` itself with explicit executable paths for both the WM and X server.
It refuses to launch when `DISPLAY`, `WAYLAND_DISPLAY`, or a graphical
`XDG_SESSION_TYPE` indicates an existing X11/Wayland session.

`gxw` does not read `.xinitrc`, `.xserverrc`, `.xprofile`, `.xsession`, or desktop
autostart/configuration files. It overrides `XINITRC`/`XSERVERRC`, creates a
temporary private Xauthority file, and removes it when the session ends.
Xorg retains its normal system hardware configuration. gxw reads st's shared
JSON configuration for fonts; session options are supplied on the command line.

By default, `gxw` selects the first available local display (`:0`, `:1`, …),
skipping existing X lock files, socket paths, and active abstract sockets.
`-display :1` explicitly selects a display instead.
The current console is used when available; `-vt 3` explicitly selects VT 3.
`-font /path/to/font.ttf` overrides the configured font. `-no-autostart` starts with
an empty desktop. The default terminal is a sibling `st`/`st-min`, then
`st`, `st-min`, `xterm`, or `x-terminal-emulator` on `PATH`.

## Resolution and fonts

When screen width exceeds **1200** or height exceeds **800**, top tabs use **2×**
font size and controls. `-font-size` sets their unscaled size (default 16px).
Top tabs have no extra text padding: height follows the font metrics directly.
Screen-size changes recompute sizing, client workareas and mouse targets;
returning to 1200×800 or smaller restores 1× tab sizing.

The bottom system bar uses the **same Monaco atlas, icons and sizing as st-fb**:
28px font in 21×35px cells with 8px padding (51px bar height), scaled to **80%**
when width ≤1280 and height ≤800 (16×28px cells, 6px padding, 40px height).
Its sizing is independent of the configured tab font and `-font-size`.

Tab-font configuration lookup is identical to st: `-config PATH`, otherwise `st.json` next
to the executable, then `config.json` there, then st's embedded defaults. Relative
font paths are resolved relative to that file. A sibling st and gxw therefore
read the same configuration. For example:

```sh
./BINARIES/gxw -config /path/to/st/config.json
```

The `font` field selects the primary face; bundled Monaco supplies missing Latin
glyphs. As in st, `fontawesome-solid.ttf` and `msyh.ttc` provide optional icon/CJK
fallbacks, searched in `~/.local/share/fonts` and the working directory. A CJK
TrueType font or TTC can also be selected directly in `font` or with `-font`.
Fallback advances and metrics are used for mixed-language titles and clipping.

## Controls

| Input | Action |
| --- | --- |
| Alt+1…9, Alt+0 | Select tab 1…10 |
| Alt+N / Alt+Tab / Alt+Right | Next tab |
| Alt+Shift+N / Alt+Shift+Tab / Alt+Left | Previous tab |
| Click a tab | Select it |
| `−` on a tab | Shrink it to a numbered window icon |
| Click a numbered icon | Restore its title and select it |
| Wheel over tabs / click `<` or `>` | Cycle tabs, including overflow |
| Alt+Enter / terminal-plus icon | New terminal |
| Alt+F4 | Close the focused window/dialog |
| Alt+Shift+Q | End the X session and return to the console |
| Print Screen / camera icon | Save `/tmp/gxw-screenshot.png` |
| Volume-down icon, percentage, volume-up icon | Lower volume, toggle mute, raise volume |
| Brightness-down / brightness-up icons | Lower/raise brightness |

Caps Lock and Num Lock do not interfere with shortcuts. Media volume and
brightness keys are supported. `-screenshot /path/to/file.png` changes the
screenshot destination; successful saves atomically replace the file.

Shrunk tabs keep their clients and attached services running. Shrinking the
active tab selects another expanded tab when available; if every tab is compact,
the current application stays visible. Keyboard/wheel selection also reaches
compact tabs without expanding them. Titles and numbering stay current, and the
compact state survives tab switches and resolution changes.

The system bar shares the framebuffer terminal's icons, dark blue/light green
colors, and **30-second status polling interval**. It includes a clock,
date, system battery, volume, brightness, screenshot, and terminal controls.
Volume calls the same `st-go/audio.SetVolume` direct ALSA implementation as
`st-fb`, reading the initial level from the selected hardware route.
`ST_GO_AUDIO_OUTPUT_CARD` selects the card using that module's existing rules.
Mute saves the current level, sets volume to zero, and restores that saved level.
Volume steps are −4/+3 percentage points.

Brightness uses the shared `st-go/brightness` implementation extracted from
`st-fb`: read raw sysfs values and call logind's `SetBrightness` through
`dbus-send`. Its display and −4/+5 steps use raw kernel units. Unavailable
hardware controls are omitted; failures appear in the bar. Hardware operations
run outside the X event loop, and controls refresh immediately after an action.

## Window policy

- An explicit `_NET_WM_WINDOW_TYPE_DIALOG` or a live `WM_TRANSIENT_FOR` parent
  is sufficient evidence of a dialog. Fixed-size hints alone are not.
- Dialogs stay centered and clamped to the usable screen, retaining their size
  hints. Attached dialogs share their parent's tab; nested/modal dialogs keep
  the appropriate focus. Standalone dialogs get their own tab.
- Menus, tooltips, and other override-redirect windows remain application-owned.
- Fullscreen/maximization always preserves the bars. Minimize requests select
  another tab. There are no overlapping normal windows or virtual desktops.
- Existing windows are adopted; title changes update tabs; screen-size changes
  update the layout. Multiple monitors form one X screen-wide workspace.
- ICCCM close/focus protocols and basic EWMH client lists, active-window,
  workarea, window states, and WM selection ownership are implemented.

## Development and checks

```sh
CGO_ENABLED=0 GOWORK=off go -C demo-go/gxw build -o ../../BINARIES/gxw .
make test-gxw
make test-gxw-x11   # Xvfb; xinit also enables the real session-launch test
```

The X11 target automatically uses persistent tools in `TEST-ASSETS/x11/bin`
when present, otherwise the system PATH. The bundle retains package archives,
libraries and XKB data. Run `python3 demo-go/gxw/prepare-x11-test-assets.py` from
the repository root to regenerate its relocatable wrappers from `sysroot/`.

Tests cover dialog classification, constrained geometry, overflow tabs, native
Go font rendering, TrueColor byte orders, graphical-session rejection,
argument/authentication setup, real xinit startup/shutdown, and live X11
window/focus/input lifecycles. The X11 tests always use isolated test servers.
The private `--gxw-session` mode is reserved for the child invoked by `xinit`.
