# Govim

A native Go modal terminal editor with Vim-style editing and a write-ahead
recovery journal. It does **not** embed, launch, or require Vim. The supplied
Vim sources/documentation were used as behavior references, not copied into
this implementation. This is an initial implementation, **not complete Vim 7
compatibility**; the supported commands and limits are listed below.

```sh
make govim                 # pure Go; no native-library builds
./govim document.tex
./govim +42 document.tex
./govim -r document.tex    # recover after a crash
```

It runs inside st-go or another ANSI/SGR-mouse terminal. No new system packages,
fonts, Xlib, or external clipboard programs are required. Building uses the
repository's existing Go dependencies. `pdflatex`/`bibtex` are needed only when
you explicitly request TeX compilation.

## Recovery and saving

* Every text edit, undo, and redo is appended to a swap **before** changing the
  live buffer. The initial snapshot is self-contained, so recovery does not
  require an intact original file. Each record has a sequence number, length,
  and CRC32C checksum.
* The default swap is `.FILENAME.govim.swp` beside the original. If creation is
  unavailable there, govim tries `$XDG_CACHE_HOME/govim/swap` (normally
  `~/.cache/govim/swap`). Untitled buffers also use that directory.
  `--swap-dir DIR` explicitly selects another persistent directory.
* Swaps are private (`0600`) and exclusively locked. An existing swap is never
  overwritten, and unsafe symlinks, ownership, permissions, or hard links on
  swap files are refused. Use the same swap-directory policy in concurrent
  sessions. Locks are local advisory filesystem locks, not distributed locks.
* Changed journals are `fsync`ed after **1 second or 64 KiB of additional
  journal data**, whichever comes first, including during continuous typing.
  `:preserve` forces a flush. Configure with `--sync-interval 250ms` and
  `--sync-bytes 16384`; `--sync-bytes 1` flushes every record.
* Complete records already written usually survive a process kill. A machine
  crash or power loss can lose records since the last successful `fsync`;
  this is not a promise of zero loss between flushes. Disk/filesystem hardware
  must also honor durability requests. Swaps contain plaintext document text.
* Partial write failures stop further editing rather than accepting changes
  without a journal. A failed `fsync` is shown as an error; `:preserve` retries
  it. The original file is not touched until you request a write.
* `:w` syncs the journal, encodes the document, writes/syncs a same-directory
  temporary file, renames it, and syncs the directory. New-file saves refuse a
  racing creator rather than replacing its file. Changes to an existing file
  on disk are detected before saving; overwriting requires an explicit `:w!`.
  Existing Unix permission bits and supported text encoding/line endings are
  preserved. Atomic replacement changes the inode: hard-link identity, ACLs,
  extended attributes, and ownership other than the editor's are not preserved.
  Like most editors, detection cannot eliminate all races with unrelated
  writers that ignore locks.
* Normal clean exits remove swaps. `:q!` / `:qa!` deliberately discard unsaved
  edits when exiting; interruption, EOF, and internal errors retain recovery
  files. Hidden modified buffers are checked before an unforced final exit.

Recover with:

```sh
./govim -r document.tex
./govim -r                              # list local/cache swaps
./govim -r /path/to/untitled-123.govim.swp
./govim --swap-dir /persistent/swaps -r document.tex
```

Review the recovered buffer, then `:w`. Undo history is reconstructed too.
Recovery stops at the first damaged or incomplete record. Before repairing a
tail, it durably preserves the **entire original journal** as
`*.govim.swp.damaged-*`. It does not skip damaged middle records or guess edits.
Vim's binary `.swp` format is separate and is neither read nor overwritten.
Copy irreplaceable files/backups before trying a new editor on them.

## Fixed preferences

The applicable preferences from `TEST-ASSETS/vimrc0` are built in: line numbers,
ruler, cursor-line/search highlighting, desert-style colors, underlined gray
matching brackets, four-column tabs/indentation, expanded inserted tabs,
auto/smart indentation, unrestricted insert backspace, mouse selection, UTF-8
with lossless GB18030 fallback, and double-width ambiguous characters.
UTF-8 BOMs, uniform CRLF, and missing final newlines are retained.

Syntax coloring is a builtin lexer for common source/text/TeX constructs, not
Vim runtime syntax files. `:set` can show/accept the fixed preferences; there
is no general options interpreter, Vimscript, plugin loader, or modeline
execution. Indentation is a builtin heuristic, not Vim's full filetype engine.

## Commands

Counts and operator/motion counts multiply (`2d3w`). `:help` shows a compact
reference; unsupported commands produce an error rather than calling Vim.

| Area | Implemented commands |
| --- | --- |
| Movement | `h j k l`, arrows, `w W b B e E ge gE`, `0 ^ $ g_ \|`, `gg G H M L`, `+ - _`, `f F t T ; ,`, `%`, sentences/paragraphs, section motions, wrapped-row `gj/gk/g0/g^/g$`, `gm/gM` |
| Scrolling | Ctrl-F/B/D/U/E/Y, PageUp/Down, `zt zz zb`, Ctrl-L |
| Locations | `m{mark}`, `'`/backtick, Ctrl-O/I, `g; g,`, `gf`, Ctrl-^ |
| Editing | `i I a A o O R r`, `d c y` plus motions/objects, `dd cc yy`, `x X s S D C Y`, `p P`, `J gJ`, `> < =`, `g~ gu gU gq`, `~`, Ctrl-A/X |
| Objects | `iw/aw`, `iW/aW`, sentences/paragraphs, paired parentheses/brackets/braces/angles and quotes |
| Visual | `v V` / Ctrl-V, motions, `o`, yank/delete/change/replace/case/shift/join/put, block `I/A`, `gv` |
| Insert | Escape, arrows, Home/End, Backspace/Delete, indentation/newline, Tab, Ctrl-W/U/T/D/R/V/O/N/P, literal bracketed paste |
| History | `u`, Ctrl-R, line `U`, `.`, registers, `q{reg}` recording, `@{reg}`, `@@` |
| Search | `/ ? n N * # g* g#`, wrapping, per-occurrence highlights, `:noh` |
| Substitute | `:[range]s/pattern/replacement/gicn`, `&`, `\1`…`\9`, `\r`, confirmation `y/n/a/q/l` |
| Files | `:w[!]`, `:wq`, `:x`, `:q[!]`, `:qa[!]`, `:wa`, `:wqa`, `:e[!]`, `:enew`, `:read`, `:saveas`, `:file`, `:cd`, `:pwd` |
| Buffers/arguments | `:args`, `:argadd`, `:next`, `:prev`, `:first`, `:last`, `:ls`, `:b`, `:bn`, `:bp`, `:bd` |
| Windows/tabs | `:split`, `:vsplit`, `:new`, `:vnew`, `:close`, `:only`, `:tabnew`, `:tabnext`, `:tabprevious`, `:tabclose`, Ctrl-W navigation/splits/resize |
| Ex edits | ranged `:delete`, `:yank`, `:Y`, `:D`, `:put`, `:join`, `:copy`, `:move`, `:sort`, `:retab`, `:normal`, global/inverse-global delete/print |
| Other | `:marks`, `:registers`, `:jumps`, `:changes`, `:undo`, `:redo`, `:preserve`, `:swapname`, `:! COMMAND`, `:Gn[!] [FILE]` |

Ex ranges support line numbers, `.`, `$`, marks, offsets, `%`, comma/semicolon,
and visual marks. Mouse supports click/drag, double-click word selection,
triple-click line selection, wheel scrolling, and middle-click PRIMARY put.

### Clipboard

The preferred `yy`/`dd` (including counts), visual `y`/`d`, and ranged `:Y`/`:D`
copy affected text directly to the **X11 CLIPBOARD**. `"+` uses CLIPBOARD and
`"*` uses PRIMARY. Transfers support UTF-8, Latin-1 fallback, and large X11 INCR
requests. A small instance of **the same govim binary** keeps owned selections
alive after exit until another application replaces them (up to 24 hours).
There is no `xclip`, `xsel`, shell clipboard command, or font dependency.

Without a reachable X display, internal registers still work and copying
reports the missing system clipboard. Native Wayland and OSC52 clipboard
transport are not implemented.

### TeX compilation

`:Gn [FILE]` (or F5) saves current edits and runs `pdflatex`, resolving a
fragment's `%main=...` relative to the fragment. Missing `.tex` suffixes are
accepted; redirect cycles are rejected. `:Gn!` uses nonstop mode, runs `bibtex`
when an `.aux` exists, and runs three additional `pdflatex` passes, matching the
preferred command. Ctrl-F5 is also recognized. Filenames are passed as argv,
not interpolated into shell commands. This does not replace or alter the
separate vimtex live-preview demo.

## Current boundaries and tests

This is not yet a drop-in Vim replacement. In particular it lacks arbitrary
Vimscript/options, folds, tags, a full quickfix engine, arbitrary Ex global
bodies/filters, tag objects, and the complete Vim regular-expression engine.
Search supports basic Vim magic plus `\v` for RE2 syntax and `\V` for literals;
RE2 cannot provide backtracking/lookaround or Vim pattern backreferences.
Some corner-case motions, block operations, syntax, and indentation differ.
Unicode indexing is by rune, not full grapheme cluster.

Safety bounds: 16 MiB decoded text / 1,000,000 lines per buffer, 64 MiB total
open text, 64 MiB undo payload per buffer, 8 MiB per register/clipboard value,
65,536 search matches/patches per bulk operation, bounded counts/macros/windows.
Oversized operations are refused. Journals are append-only for the session;
disk usage includes original/deleted text and all journaled edits.

```sh
go test ./demo-go/govim
go test -race ./demo-go/govim
make test
# Optional; use an already-running, isolated X server (no packages downloaded):
GOVIM_TEST_X11=1 DISPLAY=:99 go test ./demo-go/govim -run NativeClipboardX11
```

Tests use synthetic text and cover command composition, optional comparison
against an installed Vim **as a test oracle only**, randomized editing/history,
journal corruption/locking/write-sync failures, file conflicts, encodings,
rendering/parser bounds, PTY resize/paste/mouse/termios cleanup, and SIGKILL
recovery in insert mode for both flush triggers. Fuzz targets cover the parser
and editor state machine. No private reference-document text is copied.
