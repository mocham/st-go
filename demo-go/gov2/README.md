# gov2 — direct DRM/KMS MP4 player

A fullscreen DRM/KMS player and the default video launcher from st-fb's file
browsers. It was forked from [`gov`](../gov/README.md). Its own
FFmpeg bridge and Go playback controls run in **gov2 itself**. Scaling
and status drawing use a reusable, cacheable RAM buffer. C uploads each completed
frame to one of two DRM dumb buffers and submits vblank-synchronized page flips.
Rendering can overlap a pending flip; completion is collected before uploading
to the next scanout buffer. Video pixels do not pass through the terminal, a
retained surface, or the framebuffer compositor.

The playback engine is a private fork in `internal/playback`, including its C
decoder bridge. It does not import `st-go/platform_neutral/video` or link
`third_party/video_ffmpeg.o`. The terminal integration is exclusively the real
TTY handoff and restoration. Keep gov2's DRM, rendering and playback experiments
inside this module; st-fb's original playback/rendering engine is independent.
Both executables reuse the prebuilt FFmpeg/x264 library archives.

## Dependencies and build

- Linux, Go 1.26+, a C compiler with static libc, `pkg-config`, **Meson and Ninja**.
- Make downloads checksum-pinned **libdrm 2.4.133** and builds its core KMS API as
  a static archive, alongside the existing bundled FFmpeg/x264 archives. Native
  build tools are listed in [building](../../platform_neutral/BUILDING.md).
- At runtime: a KMS-capable `/dev/dri/cardN` supporting dumb buffers and page
  flips, permission to open it and acquire DRM master, and a foreground Linux
  text VT. Direct ALSA audio uses `/dev/snd`. Mouse/touchpad input needs read
  access to `/dev/input/event*`.

No mpv, external ffmpeg, libav shared libraries, libasound, EGL, GBM, OpenGL,
X11, Wayland, or libseat dependency is added. **`make gov2` produces a fully
statically linked executable**, including libdrm, FFmpeg and libc; no shared
libraries or installed system libdrm headers are required. `make all` includes it.

```sh
make gov2 st-fb

# Inside the rebuilt st-fb (gov2 must sit beside the st-fb executable):
./BINARIES/gov2 /path/to/movie.mp4

# Or directly on a foreground Linux text VT:
./BINARIES/gov2 -volume 50 /path/to/movie.mp4
./BINARIES/gov2 -mute -drm-device /dev/dri/card1 -connector 42 movie.mp4
```

Inside st-fb, the launching PTY sends an authenticated `native-video` request.
The host suspends input/scanout, restores the physical VT, and launches its
**sibling gov2** in the foreground group on that VT. When playback exits, the
host reclaims the console and repaints the terminal. This also works with a
rebuilt `st-fb-min`; the decoder is in gov2. An ordinary PTY, SSH session, or
desktop terminal cannot supply DRM/VT ownership.

## Controls

Space/k pauses or resumes (replays at EOF); Left/h and Right/l seek five seconds;
0/Home restarts; [ and ] change speed from 0.25× to 4× with pitch-preserving audio;
+/Up and −/Down change volume; m mutes; q/Escape/Ctrl-C exits.
Status, progress, and controls are drawn into the RAM frame with a small bitmap
font. Mouse/touchpad controls:

- Click or tap the bottom buttons to pause/play, seek ±5 seconds, change speed
  or volume, mute, or quit. Click and drag the timeline to seek.
- Scroll over the timeline to seek; scroll over speed or volume buttons to adjust.
- Touchpads support one-finger movement, tap-to-click, double-tap-and-drag,
  physical click-drag, and two-finger scrolling (legacy ABS and type-B multitouch).
- A visible software cursor updates even while paused or at EOF. Its updates
  reuse the RAM picture rather than decoding/scaling it again.

Devices are discovered automatically, including hotplug. Ctrl-Alt-Fn releases
input grabs and DRM master and pauses playback; returning discovers devices
again and resumes while preserving the user's pause state. Keyboard controls
work when no pointer device is accessible.

## Scope and performance

- Software H.264/HEVC/MPEG-4 decoding and RGB scaling, using the same MP4/MOV and
  AAC/MP3 support as gov. DRM output alone does **not** enable hardware decoding
  or DMA-BUF/YUV-plane zero-copy presentation.
- Uses an already-active connector's current mode, preserving its CRTC and
  cloned connector set for restoration. No modesetting to another resolution.
  The source/output pixel budget remains 4096 × 2160; native output is currently
  little-endian XRGB8888. Hotplug/mode replacement during playback ends the
  session on device errors rather than selecting a different output.
- Fullscreen ownership means st-fb tabs/system-bar input is unavailable during
  playback; terminal screen recording holds its last frame. They return on exit.
- This removes retained-surface copies and the terminal compositor's scheduling
  stage. Decode, audio submission, and playback timing were forked from gov and
  now evolve only in this module. Direct DRM is not a guaranteed speedup.
- CPU rendering into mapped DRM memory was slower than gov in initial use.
  Dumb-buffer mappings can be uncached/write-combined, making scaling and
  especially read-modify-write text blending expensive. The current path uses
  cacheable RAM plus a sequential, write-only upload instead. Its speedup still
  needs measurement on the target hardware.

## Verification

```sh
make test-gov2
CGO_ENABLED=0 GOWORK=off go -C demo-go/gov2 test ./...
```

Tests cover RAM-only rendering, pitched write-only uploads, the C bridge's buffer retirement and VT
transitions with simulated DRM events, page-flip/VT serialization, console
rollback, fragmented PTY handoff, command selection, playback suspension, and
output-error shutdown, evdev mouse/touchpad gestures, dropped-event recovery,
status-bar hit testing, dragging, and cursor restoration. A real-device smoke test is required for
DRM driver behavior: compare gov/gov2 on the same clip, test pause/seek/speed and pointer controls,
Ctrl-Alt-Fn away/back, quit, and confirm st-fb repaints and accepts input afterward.
