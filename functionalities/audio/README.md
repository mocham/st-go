# Terminal-owned audio

`st-go/audio` is a session-owned host service. It preserves the original
Go MP3 decoder → 16-bit stereo PCM → direct-hardware ALSA playback chain,
without subprocess players, mixer changes, or PCM attenuation.

Only st-go imports this module. Clients use `st-go/tui/audio` and the
[authenticated terminal protocol](../../platform_neutral/stterminal/README.md#native-audio).
The file-browser's `q` closes its viewer and stops its matching playback job.
Terminal exit cancels and drains any remaining playback before cleanup.

There is one active job per terminal and no unbounded playback/PCM queue.
Opening/decoding/writing runs outside the parser/render lock. Completion is
reported only after device cleanup. Inputs must be regular files; malformed
or unsupported streams produce an error rather than replacing terminal state.

Ordinary `go build` / `go test` uses system ALSA development files through
pkg-config. The repository's Makefile instead builds the unchanged C wrapper
and static libasound under `third_party/`, selected with `alsa_static`.
The service does not override inherited ALSA configuration. Hardware playback
still requires an available sound device and permission to open it.
