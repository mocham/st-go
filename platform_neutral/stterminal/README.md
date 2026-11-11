# Terminal engine and protocols

`stterminal` owns ANSI/VT parsing, terminal cells, colors, cursor state, alternate
screens, selection, mouse reporting, and bracketed paste. Backend-neutral hooks
connect it to the frontend; `st-go/term` is a compatibility import.

For DCS syntax and behavior, use the implementation and adjacent tests:

| Capability | Implementation |
| --- | --- |
| Image/text placement and X11 window geometry | [`escape.go`](escape.go) |
| Retained preview regions, navigation, and document queries | [`display_regions.go`](display_regions.go) |
| Browser HTML previews and chunked transfers | [`region_html.go`](region_html.go) |
| Retained navigation lists | [`region_list.go`](region_list.go) |
| Fixed-page PDF search | [`pdf_search.go`](pdf_search.go) |
| Terminal tabs | [`tab_spawn.go`](tab_spawn.go) |
| Shared clipboard | [`clipboard_sync.go`](clipboard_sync.go) |
| Audio / framebuffer video | [`native_audio.go`](native_audio.go), [`native_video.go`](native_video.go) |
| OSC clipboard policy | [`clipboard.go`](clipboard.go) |

Applications can reuse the clients in [`st-go/tui`](../../functionalities/tui/).
See [frontend usage](../frontend/README.md) and
[backend ownership](../README.md#dependencies-and-ownership).
