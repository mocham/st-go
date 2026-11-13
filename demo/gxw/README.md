# gxw — fullscreen tabbed X11 session

`gxw` is a standalone, deliberately small window manager and session launcher.
Each ordinary window fills the screen between a top tab strip and a bottom
system bar. Select a tab with **Alt+1…9 / Alt+0**, **Alt+N**, or a mouse click.

The executable is **CGO-free**. XGB speaks the X11 protocol directly; the
native Go FreeType port rasterizes text into local pixel buffers. Go Mono is
embedded, so no installed fonts, X fonts, Xft, or font server are needed.

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
Xorg retains its normal system hardware configuration. There is no gxw config
file; options are supplied on the command line.

By default, `gxw` selects the first available local display (`:0`, `:1`, …),
skipping existing X lock files, socket paths, and active abstract sockets.
`-display :1` explicitly selects a display instead.
The current console is used when available; `-vt 3` explicitly selects VT 3.
`-font /path/to/font.ttf` selects a TrueType font. `-no-autostart` starts with
an empty desktop. The default terminal is a sibling `st`/`st-min`, then
`st`, `st-min`, `xterm`, or `x-terminal-emulator` on `PATH`.

## Controls

| Input | Action |
| --- | --- |
| Alt+1…9, Alt+0 | Select tab 1…10 |
| Alt+N / Alt+Tab / Alt+Right | Next tab |
| Alt+Shift+N / Alt+Shift+Tab / Alt+Left | Previous tab |
| Click a tab | Select it |
| Wheel over tabs / click `<` or `>` | Cycle tabs, including overflow |
| Alt+Enter / `+ Term` | New terminal |
| Alt+F4 | Close the focused window/dialog |
| Alt+Shift+Q | End the X session and return to the console |
| Print Screen / `Shot` | Save `/tmp/gxw-screenshot.png` |
| `V-`, volume percentage, `V+` | Lower volume, toggle mute, raise volume |
| `B-`, `B+` | Lower/raise brightness |

Caps Lock and Num Lock do not interfere with shortcuts. Media volume and
brightness keys are supported. `-screenshot /path/to/file.png` changes the
screenshot destination; successful saves atomically replace the file.

The system bar uses the framebuffer terminal's dark blue/light green styling,
with a clock, date, system battery, volume, brightness, screenshot, and terminal
controls. Volume uses the first working `wpctl`, `pactl`, or `amixer` backend.
Brightness uses Linux backlight sysfs, falling back to `brightnessctl` when
direct writes are unavailable. Unavailable hardware controls are omitted;
command failures appear in the bar. System commands run outside the X event
loop.

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
CGO_ENABLED=0 GOWORK=off go -C demo/gxw build -o ../../BINARIES/gxw .
make test-gxw
make test-gxw-x11   # Xvfb; xinit also enables the real session-launch test
```

Tests cover dialog classification, constrained geometry, overflow tabs, native
Go font rendering, TrueColor byte orders, graphical-session rejection,
argument/authentication setup, real xinit startup/shutdown, and live X11
window/focus/input lifecycles. The X11 tests always use isolated test servers.
The private `--gxw-session` mode is reserved for the child invoked by `xinit`.
