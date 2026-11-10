package main

// manualText is the user manual shown by :help in the preview pane. It matches
// the manual embedded in demo/file-browser.sh.
const manualText = `ST-GO FILE BROWSER  USER MANUAL

NAVIGATION
  Up/Down or click      select an entry
  PageUp/PageDown       jump by a screen
  Ctrl+D               old last visible entry to top, select it
  Ctrl+U               old first visible entry to bottom, select it
  Home/End              first/last entry
  Enter                 open file or enter directory
  Backspace             go to parent directory
  Middle click          go to parent directory
  Enter on a zip, rar, cbz, or cbr archive browses its extracted
  contents (from /tmp); Backspace leaves the archive.
  Enter on a SQLite database browses its tables and records read-only.
  Record lists are loaded in pages of 100; Enter follows next/previous pages.

PREVIEW
  The preview displays the selected entry: text, images, PDFs, TeX, and
  Markdown. The navigation sidebar floats over the full-width preview by
  default. Tab shows or hides it; Shift+Tab toggles floating and pinned-right
  modes for newly painted previews. TeX and Markdown are rendered locally to
  paginated vector PDFs by latexpdf; Markdown supports inline and display
  LaTeX math through the same pipeline.
  DOCX and XLSX have preliminary text-only previews extracted from their
  ZIP bundles. DOCX shows paragraphs and table text; XLSX shows sheet names,
  cell references and values (saved formula results, without recalculation).
  Office formatting, images and date/number styles are not reproduced.
  Legacy DOC previews read unencrypted Word 97–2003 binary files directly,
  including compatible later saves. Main-body text and saved field results
  are shown; headers, footnotes, images and formatting are omitted. Older,
  encrypted, and renamed RTF/HTML files are not supported. No Office tools
  are needed.
  Animated WebP files play once in the preview pane (progress shown in
  the rect); a visible sidebar reserves its space so redraws do not stop
  playback. The terminal does all image/animation decoding.
  MP3 metadata is decoded in-browser; the terminal owns audio playback
  and its device. Tags, duration, and sample rate are shown. Enter or
  o opens the mp3 viewer, q stops playback and closes it.
  Wheel over a PDF or rendered document preview changes pages; [ and ] do the same.
  A text preview stops at the last visible row and never scrolls.

COMMAND PROMPT  (:)
  Press : to open a shell command prompt. The selected file is
  exported as $F and the current directory as $D, and the command
  runs with the current directory as its working directory:
    :ls -l
    :vim $F
    :xdg-open $F
  Edit with Left/Right, Home/End, Ctrl+A/Ctrl+E, Backspace and
  Delete. Escape, Ctrl+C, or a mouse click cancels. Enter runs.

RENAME  (:s/old/new/)
  A command beginning with :s/ is a rename, not a shell command:
    :s/txt/md/       rename entries: first txt -> md
    :s/txt/md/g      rename entries: every txt -> md
  While you type, every entry whose name would change is painted
  yellow in the list so you can preview the change before Enter.
  Press Enter to apply. Entries whose new name already exists are
  skipped. The parent entry is never renamed.

MANUAL  (:help)
  Press :help to open this manual in the preview pane. Navigate it
  like a PDF: ] next page, [ previous page, wheel to flip. Press q
  or Escape to close.

PATH PROMPT  (/)
  Press / to enter a path. Absolute paths and paths relative to the
  browser directory work, including wildcards:
    /home/user/file.txt
    sub/*.go
  A popup lists live matches; Up/Down selects one before Enter.
  Tab fills the input with the first match; directories get a trailing /.
  Completion stays in input mode. Only Enter opens or enters the path.
  Backspace removes the leading / to type relative paths.

OTHER KEYS
  Tab              show/hide the navigation sidebar
  Shift+Tab        toggle floating/pinned-right navigation
  .                toggle hidden files
  r                refresh the listing
  q                quit

MOUSE
  Click          select
  Double-click   open file or enter directory
  Wheel          scroll the list (or flip PDF pages over the preview)
  Middle click   go to parent

HINTS
  With the default opener, .pdf uses zat and text/source/config files use govim
  when available in PATH. They run in this terminal; exit the tool
  to return to the same browser location on every backend. Desktop
  fallbacks are X11-only. Register custom code with --open-for PLATFORM CODE
  or ST_FILE_BROWSER_OPEN_X11 / _FB / _WEB / _ALL. Matching empty values
  disable opening. Legacy --open and ST_FILE_BROWSER_OPEN are X11-only.
  Framebuffer videos hand the real VT to mpv --vo=drm via the native helper.
  Video defaults to alsa/plughw:1,0; ALSA_PCM_CARD / ALSA_PCM_DEVICE
  override video. ST_FILE_BROWSER_VOLUME sets mpv's volume (default 33, 0..100).
  MP3's working PCM output/config and global mixer levels are left unchanged.
  X11/web have no default video player. Custom platform handlers still win.
  Double-clicking an externally opened file collapses the terminal
  to a bottom strip; clicking the strip restores it. Filenames are sanitized before
  drawing, so names with spaces, quotes, or control characters are
  handled safely. Override openers and icons with --open-for,
  ST_FILE_BROWSER_OPEN, ST_FILE_BROWSER_ICON_*, and the terminal
  config file_browser section.`
