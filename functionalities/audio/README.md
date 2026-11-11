# Terminal-owned audio

`st-go/audio` is a session-owned host service using the
Go MP3 decoder → 16-bit stereo PCM → direct-hardware ALSA playback chain,
without subprocess players or PCM attenuation. The framebuffer system bar is
the one mixer client: it switches output and adjusts the selected route's
hardware playback volume.

Only st-go imports this module. Clients use `st-go/tui/audio` and the
[authenticated terminal protocol](../../platform_neutral/stterminal/native_audio.go).
The file-browser's `q` closes its viewer and stops its matching playback job.
Normal completion drains the finite hardware buffer; cancellation drops it so
terminal shutdown cannot wait on queued audio.

There is one active job per terminal and no unbounded playback/PCM queue.
Opening/decoding/writing runs outside the parser/render lock. Completion is
reported only after device cleanup. Inputs must be regular files; malformed
or unsupported streams produce an error rather than replacing terminal state.

The ALSA frontend is pure Go and talks directly to Linux `/dev/snd` PCM and
control devices through the stable kernel UAPI. Builds need neither cgo,
libasound, ALSA development headers, nor an ALSA configuration. The deliberately
narrow frontend supports direct hardware rather than `default`, `plug`, `dmix`,
software resampling, or dynamically loaded ALSA plugins. Hardware playback still
requires an available sound device and permission to open it.

`SelectedOutput` inspects ALSA controls read-only for the framebuffer system-bar
indicator (0 speaker, 1 earphone, otherwise unknown). Clicking the indicator
calls `SetOutput` to mute one and unmute the other using the `Speaker` and
`Headphone` playback switches; it does not
open or reconfigure a PCM stream. The highest of the first two discovered cards
is used unless `ST_GO_AUDIO_OUTPUT_CARD` supplies an explicit card number.
`SetVolume` adjusts the selected raw `Headphone` or `Speaker` playback control
in the ALSA 0..100 percentage range without reopening or reconfiguring PCM.
