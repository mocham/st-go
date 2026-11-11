# File browsers

The Go browser uses st-go's terminal protocols for file navigation, previews,
and external openers.

```sh
make file-browser
./BINARIES/st -e ./BINARIES/file-browser /some/path
./BINARIES/st-web -e ./BINARIES/file-browser --web /some/path
```

## Navigation and prompts

| Key/action | Result |
| --- | --- |
| Arrows / single click | Select and preview |
| Enter / `o` / double click | Open file or enter directory |
| Backspace | Parent directory |
| Page Up/Down, Ctrl-U/D | Page the file list |
| `[` / `]`, wheel over PDF | Previous/next page |
| `.` / `r` | Toggle hidden files / refresh |
| `/` | Editable path with live completion; Tab fills a match, Enter activates |
| `:` | Shell command; `$F` is selected file, `$D` is current directory |
| `:help` | Paged manual; `q`/Escape closes it |
| `q` | Quit |

Paths may be absolute or relative and support glob matching. Escape/Ctrl-C cancels
prompts. `:s/old/new/` renames matching entries; add `g` for all occurrences.
Existing targets and the parent entry are skipped.

The Go navigation panel starts floating: Tab hides/shows it, Shift+Tab toggles
pinned-right mode for subsequent previews. In **`--web`**, it is always pinned
while visible; Tab expands/restores the preview and Shift+Tab is disabled.

## Previews

Text, images, and PDFs use a fixed preview rectangle. The Go browser additionally
supports archives, SQLite views, audio metadata, and generated document previews:

- LaTeX/Markdown: selectable-text PDFs with vector math using
  [`latexpdf`](../../functionalities/latexpdf/README.md), including local TeX inputs.
- HTML/XHTML/EPUB: semantic content converted to PDF; CSS/fonts are ignored.
- DOCX/XLSX: text, tables or sheet/cell values; no original formatting or formula
  recalculation.
- Word 97–2003 `.doc`: bounded text extraction; encrypted files are unsupported.

**`--web`** renders LaTeX/Markdown/HTML as browser-native HTML and uses browser
image decoding/PDF.js. HTML conversion is asynchronous and stale selections are
discarded. Scroll HTML with the mouse or `[`/`]`; focused previews still forward
Tab to the browser application. Geometry/content follow resize and reconnect.

Native LaTeX/Markdown preview text matches govim's default on-screen size and
stays stable when navigation is pinned or hidden. Generated PDFs are temporary
and cached by revision/pane size, with bounded eviction.

## Openers and backend policy

Put `BINARIES/` on `PATH` to enable built-in tools:

- Images: **goiv** (Go browser).
- PDF: **zat**; DjVu: **zat-djvu**.
- Text/source/configuration: **govim**, with `--web` on web hosts.

The Go browser opens these in focused terminal tabs when supported. Otherwise
it runs them in the same terminal and restores the browser on exit.
Archive edits are not written back into the
archive; edited terminal-owned copies are retained for recovery.

Custom openers take precedence:

```sh
./BINARIES/st -e ./BINARIES/file-browser --open-for x11 'my-viewer "$FILE"' /some/path
ST_FILE_BROWSER_OPEN_FB='my-handler "$FILE"' ./BINARIES/st-fb -e ./BINARIES/file-browser
```

`--open-for x11|fb|web|all CODE` runs trusted Bash code with `FILE`, `NAME`,
`BROWSER_DIR`, and `ST_GO_BACKEND` exported. Environment equivalents are
`ST_FILE_BROWSER_OPEN_X11`, `_FB`, `_WEB`, and `_ALL`; an empty setting disables
opening. Backend-specific settings beat `_ALL`; the last matching CLI option wins.
`--open` / `ST_FILE_BROWSER_OPEN` apply only to X11.

On X11, missing text tools can fall back to `$ST_GO_EXECUTABLE vim`; other files
use `bar external`. Framebuffer/web require an explicit
opener when no built-in route applies. External X11 openers can collapse the
browser to a click-to-restore strip.

## Icons

Set `file_browser.icons` in terminal configuration, or override individual icons
with variables such as `ST_FILE_BROWSER_ICON_PDF=P`. Explicit empty values hide
an icon. See [default configuration](../../functionalities/config/config.json).

## Audio and framebuffer video

The Go browser reads MP3 metadata locally and requests playback from st-go;
`q` stops its job. Audio uses the terminal server's sound device, including on web.

### Framebuffer video

The browser uses `file-browser --native-video FILE` to request mpv playback on
st-fb's real VT. Defaults are DRM output, `auto-copy` hardware decoding, ALSA
`plughw:1,0`, and 33% mpv volume. st-fb restores the terminal afterward.

- `ST_FILE_BROWSER_ALSA_DEVICE`: explicit device, e.g. `plughw:Generic_1,0`.
- `ALSA_PCM_CARD` (or `ALSA_CARD`) and `ALSA_PCM_DEVICE`: card/device overrides.
- `ST_FILE_BROWSER_VOLUME=0..100`: mpv volume.

These settings do not change MP3 playback or global mixer levels. mpv requires
DRM/ALSA support and device access. No default X11/web video player is launched.
See [VT handoff](../../platform_dependent/fb/README.md#native-video-handoff).

## Build and test

The Go browser is CGO-free, using `modernc.org/sqlite` and terminal-owned audio.

```sh
GOWORK=off go -C demo-go/file-browser test ./...
make test-web-browser
```
