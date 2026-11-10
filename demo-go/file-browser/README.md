# File browsers

The [Bash browser](../../demo/file-browser.sh) and native Go browser use the same
terminal ANSI/OSC/DCS capabilities on X11, framebuffer, and web hosts. The
terminal owns graphics and native resources; neither browser opens its own
display connection. See the [terminal protocol reference](../../platform_neutral/stterminal/README.md).

Run these commands from the repository root:

```sh
./BINARIES/st -e ./demo/file-browser.sh
./BINARIES/st -e ./demo/file-browser.sh /some/path
./BINARIES/st -e ./demo/file-browser.sh --hidden /some/path

make file-browser
./BINARIES/st -e ./BINARIES/file-browser /some/path
```

## Navigation and prompts

The browser previews text, images, and PDFs in a fixed pane and uses SGR mouse
reporting in the alternate screen. Single-clicking or arrow keys select an entry
and update its metadata/preview. Double-click opens a file or enters a directory;
the wheel scrolls the list, or changes pages over a PDF preview.

Keys include arrows, Page Up/Down, Enter, Backspace, `[`/`]` for PDF pages, `.`
for hidden files, `r` to refresh, `/` for path entry, `:` for a shell command,
and `q` to quit. `:help` opens the paged manual in the preview: `]`/`[` or the
wheel change pages; `q`/Escape closes it.

**Ctrl+D** pages down with one overlapping row: the last visible entry becomes
the first entry and receives focus. **Ctrl+U** reverses this: the first visible
entry becomes the last and receives focus. Paging is independent of the current
cursor row and clamps at the directory boundaries. Page Up/Down are unchanged.

The path prompt accepts absolute paths and paths relative to the browsed
directory, including `file`, `./file`, and `../../file`. Backspace can remove
the initial `/`; Ctrl+A/Ctrl+E move to the beginning/end. Live matching shows a
popup; `*`, `?`, and bracket wildcards expand, and Up/Down selects a match.
**Tab fills the input with the first match**, appends `/` for directories, and
leaves the cursor at the end. Completion remains editable and never activates
the path: only Enter opens or enters it. Escape, Ctrl+C, or a mouse click cancels.

The `:` shell prompt uses the same modal input loop. The selected file is `$F`
and browsed directory is `$D`; commands run in that directory. Examples are
`:vim $F`, `:xdg-open $F`, and `:ls -l`. The browser leaves the alternate screen
while the command runs, then restores it.

A command beginning with `:s/` renames matching entries instead of running a
shell command. Vim-style `:s/old/new/` replaces the first occurrence in each
name; `:s/old/new/g` replaces all occurrences:

```text
:s/txt/md/       # report.txt -> report.md, notes.txt -> notes.md
:s/-/X/g         # a-b-c.txt -> aXbXc.txt
```

Affected entries are highlighted while typing so changes can be previewed.
Existing targets are skipped, and the parent entry is never renamed.

## Icons

File, directory, symlink, image, PDF, text, archive, audio, video, source,
configuration, and executable icons have built-in Unicode defaults. Override
them in a partial terminal config:

```json
{
  "file_browser": {
    "icons": {
      "directory": "D",
      "pdf": "P",
      "code": "C"
    }
  }
}
```

Resolved values are exported to the browser. One-shot environment settings such
as `ST_FILE_BROWSER_ICON_PDF=X` take precedence over JSON.

## Openers and backend policy

An external opener is trusted shell code, not a command-plus-one-argument.
Register it with `--open-for PLATFORM CODE`, or `ST_FILE_BROWSER_OPEN_X11`,
`ST_FILE_BROWSER_OPEN_FB`, `ST_FILE_BROWSER_OPEN_WEB`, or
`ST_FILE_BROWSER_OPEN_ALL`. Platforms are `x11`, `fb`, `web`, and the explicit
wildcard `all`. `FILE`, `NAME`, `BROWSER_DIR`, and `ST_GO_BACKEND` are exported
to `bash -c`, supporting functions and command sequences without interpolating
filenames into shell code.

By default, both browsers open `.pdf` with [zat](../zat/README.md), and text,
source, and configuration files with [govim](../govim/README.md), when the tool
is in `PATH`. This includes `.sh`, `.txt`, syntax-highlighted formats, dotfiles,
and extensionless scripts; extensions are case-insensitive. Tools run in the
**same terminal**; exiting restores the directory, selection, and preview.
Enter, `o`, double-click, and path entry share dispatch, without collapsing the
window for in-terminal tools.

Built-in terminal routes remain available on every backend. A matching custom
opener takes precedence, including an empty setting to disable opening.
Backend-specific environment settings beat `_ALL`, then legacy settings; the
last matching CLI option wins over environment settings. Registrations for
other backends are ignored and do not suppress native tools.

**Legacy `--open CODE` and `ST_FILE_BROWSER_OPEN` are X11-only.** The terminal
exports its actual host as `ST_GO_BACKEND=x11|fb|web`, replacing inherited
values. Empty, unset, or unknown identities do not enable legacy handlers or
desktop fallbacks. The browsers do not infer X11 from `DISPLAY` or
`WAYLAND_DISPLAY`; restart rebuilt terminals to receive the backend metadata.

On X11, missing in-terminal text tools fall back to the launching executable,
`$ST_GO_EXECUTABLE vim "$FILE"`. Other formats use `xdg-open` in Bash and
`bar external "$FILE"` in Go. Framebuffer/web report that no opener is registered
instead of launching desktop fallbacks. Browser-native directory/archive,
database, and audio handling is unchanged.

```sh
./BINARIES/st -e ./demo/file-browser.sh --open-for x11 '
  open_file() {
    case $FILE in
      *.pdf) zathura "$FILE" ;;
      *.txt) xterm -e less "$FILE" ;;
      *)     xdg-open "$FILE" ;;
    esac
  }
  open_file
' /some/path

ST_FILE_BROWSER_OPEN_FB='my-fb-handler "$FILE"' ./BINARIES/st-fb -e ./BINARIES/file-browser
./BINARIES/st-web -e ./demo/file-browser.sh --open-for all 'printf "%s\n" "$FILE" >> "$HOME/opened-files.log"'
```

## Framebuffer video

Framebuffer sessions default to foreground playback of MP4, MKV, WebM, and
other video formats using:

```sh
mpv --vo=drm --hwdec=auto-copy --ao=alsa --audio-device=alsa/plughw:1,0 --volume=33 -- "$FILE"
```

Both browsers use the Go `file-browser --native-video FILE` client helper
(`make file-browser`). Bash finds it beside the terminal executable or in
`PATH`. The helper communicates only through terminal text. The framebuffer
host hands its detected **real VT**, not the browser's PTY, to mpv and restores
the terminal after playback. See [handoff and recovery](../../platform_dependent/fb/README.md#native-video-handoff)
and the [wire protocol](../../platform_neutral/stterminal/README.md#native-video-handoff).
Restart rebuilt terminals to receive the native-video capability. There is no
fallback to launching DRM mpv directly on a PTY.

Audio defaults directly to **`alsa/plughw:1,0`** (card 1, device 0), without
probing or opening the MP3 player's PCM. mpv/ALSA handle format and rate
conversion; no sample format, channel count, or sample rate is forced. Override
video with `ALSA_PCM_CARD` (or `ALSA_CARD`) and `ALSA_PCM_DEVICE`, or an explicit
`ST_FILE_BROWSER_ALSA_DEVICE=plughw:Generic_1,0`. These settings do not alter the
MP3 open/write path or global mixer levels. The helper respects inherited ALSA
configuration or ALSA's default config path; embedded `alsa.conf` is only a
missing-system-config fallback in the separate helper process.

`auto-copy` probes available hardware decoders and copies decoded frames back
for software-frame DRM output; unsupported hardware/codecs fall back to software
decoding. mpv needs DRM/ALSA support and access to the corresponding devices.
No packages or permissions are changed.

Video uses mpv's own **33%** volume by default; `ST_FILE_BROWSER_VOLUME=0..100`
overrides it without writing ALSA Master/PCM controls. The built-in MP3 player
and arbitrary custom handlers retain their existing volume behavior.
**No default X11 or web video player is launched**, including desktop fallbacks.
Matching platform-specific custom openers still take precedence.

## Preview drawing and geometry

Selection changes redraw only the fixed preview rectangle, metadata, and
changed list rows. On X11, opening an external file can remember the full
geometry and collapse the terminal to a mapped bottom-left strip; clicking it
restores the saved geometry. Other backends do not perform desktop placement.

tmux consumes DCS by default; graphical previews need passthrough or a direct
st-go session. See the [protocol notes](../../platform_neutral/stterminal/README.md#multiplexers).

## Go browser previews

The Go browser's navigation sidebar starts visible and floating over a
full-width preview. Tab toggles visibility; Shift+Tab switches floating and
pinned-right mode without repainting the current preview. The next selected
file/page uses the new geometry.

TeX and Markdown share the native [`latexpdf`](../../latexpdf/README.md)
vector-PDF pipeline. Markdown supports inline `$...$`/`\(...\)` and display
`$$...$$`/`\[...\]` mathematics. HTML, XHTML, and EPUB use sanitized semantic
markup through the separate native `semhtml` PDF renderer; EPUB resolves its
package spine first. Headings, paragraphs, lists, emphasis, code, links, table
text, and image alt text are preserved; CSS and publication fonts are ignored.

Preliminary `.docx` and `.xlsx` previews extract text from ZIP parts into the
paged PDF viewer. DOCX includes paragraphs and table text. XLSX shows workbook
sheet order, cell references/values, shared/inline strings, and saved formula
results. Formatting, images, date/number styles, and formula recalculation are
unsupported. ZIP/XML reads and extracted text are bounded; external relationships
are never fetched.

Legacy `.doc` previews are self-contained and text-only for unencrypted Word
97–2003 binary documents and compatible later saves. A bounded OLE reader
follows the Word piece table, decoding compressed/UTF-16 text in document order.
It reads only header, piece-table, and text ranges rather than picture-heavy
Word/Table streams, tolerating unallocated trailers and missing unused sector
padding. Missing referenced bytes still fail; metadata/text budgets and the
256 MiB container limit remain enforced. Saved field results are shown without
instructions. Formatting, images, headers, footnotes, and embedded documents
are omitted. Encrypted/older Word files and RTF/HTML renamed to `.doc` are not
supported. No `antiword`, `catdoc`, or Office installation is required.

LaTeX/Markdown previews match govim's default 60%-width preview's on-screen text
size: both are **80% of govim's previous size**. The browser compensates for its
pane-sized PDF pages, keeping text size unchanged when its sidebar is pinned or
hidden. Govim uses a **0.64×** scale on a 140 mm fit-width page. GoLS (**3.23×**)
and Vimtex (**0.8×**) are unchanged.

Generated previews are session-temporary. A changed source digest deletes all
variants of the superseded revision. Each current revision retains at most four
pane-size variants; least-recently-used previews are removed after 32 generated
variants across all sources.
