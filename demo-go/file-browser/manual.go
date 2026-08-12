package main

// manualText is the user manual shown by :help in the preview pane. It matches
// the manual embedded in demo/file-browser.sh.
const manualText = `ST-GO FILE BROWSER  USER MANUAL

NAVIGATION
  Up/Down or click      select an entry
  PageUp/PageDown       jump by a screen
  Home/End              first/last entry
  Enter                 open file or enter directory
  Backspace             go to parent directory
  Middle click          go to parent directory
  Enter on a zip, rar, cbz, or cbr archive browses its extracted
  contents (from /tmp); Backspace leaves the archive.

PREVIEW
  The left pane previews the selected entry: text, images, and PDFs.
  Animated WebP files play once in the preview pane (progress shown in
  the rect); the terminal does all image/animation decoding.
  MP3 files are decoded in-browser and played through the ALSA
  device; their tags, duration, and sample rate are shown. Enter or
  o opens the mp3 viewer, q stops playback and closes it.
  Wheel over a PDF preview changes pages; [ and ] do the same.
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
  Backspace removes the leading / to type relative paths.

OTHER KEYS
  .                toggle hidden files
  r                refresh the listing
  q                quit

MOUSE
  Click          select
  Double-click   open file or enter directory
  Wheel          scroll the list (or flip PDF pages over the preview)
  Middle click   go to parent

HINTS
  Double-clicking a file collapses the terminal to a bottom strip;
  clicking the strip restores it. All filenames are sanitized before
  drawing, so names with spaces, quotes, or control characters are
  handled safely. Override openers and icons with --open,
  ST_FILE_BROWSER_OPEN, ST_FILE_BROWSER_ICON_*, and the terminal
  config file_browser section.`
