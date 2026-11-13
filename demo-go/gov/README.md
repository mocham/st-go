# gov — MP4 player

`gov` is a small Go terminal client for the **full `st-fb`** build's bundled
FFmpeg decoder. Video is presented through the framebuffer compositor, so tabs,
terminal controls, screenshots, and screen recording continue to work during
playback. Audio uses the terminal's direct ALSA PCM output.

```sh
make st-fb gov
# Inside st-fb:
./BINARIES/gov /path/to/movie.mp4
./BINARIES/gov -mute /tmp/record.mp4
```

Or start both from a foreground Linux text VT:

```sh
./BINARIES/st-fb -e ./BINARIES/gov /path/to/movie.mp4
```

## Controls

| Key | Action |
| --- | --- |
| Space / k | Pause/resume; replay at the end |
| Left / h, Right / l | Seek backward/forward five seconds |
| 0 / Home | Return to the start |
| + / Up, − / Down | Adjust playback volume |
| m | Mute/unmute |
| q / Escape / Ctrl-C | Quit |

`-volume 0..100` sets initial playback volume. This is per-player attenuation;
the system-bar volume remains the hardware volume. If ALSA is unavailable, video
continues and the status line reports the audio error. Resizing fits the video
into the available area, preserving aspect ratio with black borders.

## Playback path

- Local MP4/MOV containers; H.264, HEVC, and MPEG-4 video; AAC and MP3 audio.
- Software decoding, with a source/output pixel budget of 4096 × 2160.
- `gov` is CGO-free. FFmpeg, x264, and audio resampling are linked into `st-fb`;
  neither `ffmpeg`, `ffprobe`, nor `mpv` is needed for playback.
- No DRM passthrough is needed: decoded frames use an ordinary retained terminal
  surface. Presentation follows the framebuffer backend's software clock.
- Controls and status use authenticated, bounded `video` DCS requests. Media
  bytes stay inside the terminal. Quit, alternate-screen exit, and terminal
  shutdown release the surface and cancel decoding.

## Tests

```sh
make test-gov
go test -race -tags ffmpeg_static ./platform_neutral/video
```

Native tests generate short MP4 fixtures with a system FFmpeg, then clear `PATH`
while testing decoding, audio conversion, pause/seek/replay, and terminal-surface
lifecycle. Device playback is exercised by running `gov` on the foreground VT.
