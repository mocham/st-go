# Govim

A native Go modal terminal editor with Vim-style editing and a write-ahead
recovery journal. It does **not** embed, launch, or require Vim. The supplied
Vim sources/documentation were used as behavior references, not copied into
this implementation. This is an initial implementation, **not complete Vim 7
compatibility**; the supported commands and limits are listed below.

```sh
make govim                 # pure Go; no native-library builds
./BINARIES/govim document.tex
./BINARIES/govim +42 document.tex
./BINARIES/govim -r document.tex    # recover after a crash
```

It runs inside st-go or another ANSI/SGR-mouse terminal. No new system packages,
fonts, Xlib, or external clipboard programs are required. Syntax highlighting
uses the pure-Go [Chroma v2](https://github.com/alecthomas/chroma) module.
`pdflatex`/`bibtex` are needed only when
you explicitly request TeX compilation.

## Live document preview

Inside st-go, opening a LaTeX (`.tex`, `.latex`, `.ltx`) or Markdown
(`.md`, `.markdown`, `.mdown`, `.mkd`) file automatically opens the retained
preview panel: **40% editor / 60% preview**, warm off-white paper, fit-width
continuous scrolling, selectable PDF text, and vector mathematics. Govim stays
the editor; no Vim, child editor, external typesetter, or extra font package is
launched for previewing.

```sh
make st govim
./BINARIES/st -e ./BINARIES/govim document.tex
./BINARIES/st -e ./BINARIES/govim notes.md
./BINARIES/govim --preview-width 50 --preview-bg '#ffffff' notes.md
./BINARIES/govim --no-preview document.tex
```

* The initial preview loads when the terminal confirms region support. Saves
  trigger refresh through an editor hook; **there is no file-change monitor**.
  Unsaved typing and external filesystem changes alone do not refresh it.
  Explicit `:e!` reloads do. Saves of open included files also refresh their
  active parent document. `\input`/`\include` and fragment `%main=...` directives
  resolve relative to their files, with recursion/resource limits.
* On initial display and normal saves, the first **`%cursor`** comment in the
  expanded source takes precedence. With no marker, the preview seeks to the
  **govim cursor line captured at save time**, including lines in included files.
  This is approximate proportional source navigation, not SyncTeX.
* **`:wnomove[!] [FILE]`** saves and refreshes **without an automatic jump**,
  suppressing both marker and cursor following and preserving the viewer's scroll
  offset (clamped if the new document is shorter). It does not change the editor
  cursor. Ordinary `:w` follows again on the next save.
* Wheel over the preview to scroll locally without re-rendering. Manual
  `:Preview [LINE]` and `:PreviewScroll COUNT` commands are also available
  (`:VimtexPreview` / `:VimtexScroll` aliases are accepted).
* **Shift+Tab in Normal mode** switches that same panel to **Table of contents**;
  press it again to restore the PDF and its scroll position. Tab/Ctrl-I remains
  jump-forward. The TOC reads the active buffer, including unsaved edits, and
  lists `\chapter`, `\section`, `\subsection`, `\subsubsection`, and
  `\subsubsubsection`; `\label`, `\paragraph`, and `\subparagraph` share the
  lowest level. Starred/short titles are supported; comments, verbatim regions,
  and common macro definitions are ignored. Markdown ATX headings work too.
  Wheel scrolling is locally pixel-animated; clicking an item moves the main
  editor to its exact source location and adds a jump-history entry. No save,
  compilation, editor resize, or main-pane repaint is needed to switch panels.
  Source-offset changes alone do not resend the list. TOCs are bounded to 4096
  entries and a bounded protocol payload. While visible, preview publication
  waits so a save cannot unexpectedly replace the TOC. `:toggle[!]` returns to
  the preview explicitly. Requires updated st-go; `--no-preview` disables both.
* **`:toggle`** explicitly previews the on-disk document at the current editor
  cursor, ignoring `%cursor`. **`:toggle!`** instead renders immutable snapshots
  of the current named buffers, including unsaved `%main` roots/includes, and
  jumps to that cursor. Neither command writes files, marks buffers saved, or
  changes undo history. Explicit toggles bypass the automatic refresh gap;
  subsequent saves return to normal saved-document/marker behavior.
* One background render runs at a time; rapid saves coalesce to the newest saved
  revision. Changed PDFs are at least **2.1 seconds apart** by default; override
  with `--preview-gap 500ms` (or `0`). Unchanged saves can navigate without
  regenerating or reopening the PDF. Failed/stale renders never replace the last
  successful preview. PDF publication does not repaint the editor document.
* Switching to a non-preview buffer or exiting restores the full-width terminal.
  Temporary PDFs are private and cleaned up. On terminals without the st-go
  region protocol, preview negotiation times out without blocking editing;
  `--no-preview` skips negotiation entirely.

Rendering reuses the existing `latexpdf` subset through the backend-neutral
[`tui/preview`](../../tui/preview) component, not a complete TeX distribution or
browser-based Markdown renderer. The panel itself is owned by the terminal
frontend; govim communicates using DCS region commands and never assumes X11.

## Recovery and saving

The status bar retains the filepath. The bottom command/message row normally
shows **Last saved YYYY-MM-DD HH:MM:SS TZ**, initialized from the file's modification
time and advanced only by successful saves. New files show **Not saved yet**;
unsaved edits and failed saves do not advance it. Prompts and messages/errors
take precedence over this timestamp.

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
./BINARIES/govim -r document.tex
./BINARIES/govim -r                              # list local/cache swaps
./BINARIES/govim -r /path/to/untitled-123.govim.swp
./BINARIES/govim --swap-dir /persistent/swaps -r document.tex
```

Review the recovered buffer, then `:w`. Undo history is reconstructed too.
Recovery stops at the first damaged or incomplete record. Before repairing a
tail, it durably preserves the **entire original journal** as
`*.govim.swp.damaged-*`. It does not skip damaged middle records or guess edits.
Vim's binary `.swp` format is separate and is neither read nor overwritten.
Copy irreplaceable files/backups before trying a new editor on them.

## CJK input

In **Insert mode**, type **`[[[`** to start Google Pinyin composition. The bottom
command row displays five numbered candidates; **`+` / `-`** changes candidate
pages and **`1`–`5`** replaces the current spelling with the selected candidate.
For example, **`[[[nh++3` → `[[[浓厚`** while composing. Selections can be followed
immediately by more spelling. Backspace edits the current spelling. **`]]]`**
ends CJK mode normally and removes **both** delimiter triples, so
**`[[[nh++3]]]` → `浓厚`**. Escape, navigation, crashes, and other forced or
accidental exits preserve the delimiters as a recovery trace. Unselected
spelling remains literal on punctuation, exit, or failure; no save is performed.
Escape/control/navigation exits CJK mode; paste, registers, and literal input
do not activate it. Accepted Unicode is journaled and participates in undo,
recovery, insert repeats, and macros. Candidate-only updates repaint only the
command row. The saved timestamp returns when composition finishes.

`make govim` builds the separate static **`govim-pinyind`** helper. It is spawned
**lazily**, only on a live Insert-mode `[[[` trigger, never at editor startup.
CJK mode does not activate if the helper cannot be found or spawned; all
subsequent input then stays ordinary text. Native initialization is asynchronous.
Its native decoder and generated dictionary never run inside govim or st-go. A native
crash, timeout, or malformed reply cancels CJK mode, preserves typed text, and
does not kill either application; type a new `[[[` to retry. Inputs/queues are
bounded (32-byte spelling limit), and helpers are canceled/reaped on exit.
The helper uses private abstract Unix sockets and anonymous in-memory
dictionaries on Linux. No SCIM desktop service or extra font package is needed.
See [`pinyind`](../pinyind/README.md) for optional dictionary bundling, generated
assets, source provenance, license notices, and packaging both executables.

## Fixed preferences

The applicable preferences from `TEST-ASSETS/vimrc0` are built in: line numbers,
ruler, cursor-line/search highlighting, desert-style colors, underlined gray
matching brackets, four-column tabs/indentation, expanded inserted tabs,
auto/smart indentation, unrestricted insert backspace, mouse selection, UTF-8
with lossless GB18030 fallback, and double-width ambiguous characters.
UTF-8 BOMs, uniform CRLF, and missing final newlines are retained.

Syntax coloring uses **Chroma's language lexers and style inheritance**, plus a
restartable TeX scanner. Filename patterns (including names such as `Makefile`)
and supported shebangs choose the language. The embedded
[`syntax/desert.json`](syntax/desert.json) theme maps semantic groups to the
supplied Vim91 desert colors; it is data, not executable Vimscript. Comments,
strings, commands, types, and identifiers have distinct foregrounds. TeX math
bodies differ from prose, commands/delimiters, comments, and verbatim; inline and
display delimiters, common starred/nested AMS environments, and `\text{...}`
retain state across lines. See [the group mapping](syntax/README.md). Cursor-line,
search, selection, and bracket backgrounds remain editor overlays.

Highlighting is enabled automatically for Chroma's recognized filename patterns,
including `.vim`, `.tex`, `.md`, `.sh`, `.py`, `.go`, `.c`, `.cc`/`.cpp`, `.json`,
`.xml`, `.html`, `.css`, `.js`, `.php`, `.toml`, `.yaml`/`.yml`, `.csv`, and `.ini`.
Common extensionless/configuration names are covered too: `Dockerfile`
(`Dockerfile.*`), `Containerfile`, `Makefile`, `.bashrc`, `.bash_profile`,
`.bash_logout`, `.profile`, `.zshrc`, `.zprofile`, `.zshenv`, `.vimrc`,
`.editorconfig`, and `.gitconfig`. Unknown files remain plain text; supported
shebangs can identify scripts without a recognized filename. Regression tests
check that these examples select a lexer **and produce colored spans**.

Highlighting is **lazy**, driven by visible lines plus a 40-line margin, not a
whole-buffer job after every keystroke. A background worker reads immutable
rune snapshots without a whole-buffer UTF-8 copy. Restartable Chroma regex
lexers cache safe lexical-stack checkpoints; edits invalidate from a preceding
line/checkpoint and re-lex forward only until state convergence or the requested
viewport. TeX has explicit line/state checkpoints. Scrolling extends demand,
and split-window demands for the same buffer are combined. Stale results are
rejected using an edit serial as well as the buffer version (including undo).

**There is no uncolored document pass.** Until the edited viewport's colors are
ready, its previous committed cells remain on screen; prompts/status can still
update. Text and final colors are then committed together. ANSI output compares
cells and sends only changed spans, including color-only changes; identical
frames emit nothing. Cursor hide/show is not toggled on every frame.

Worker slices normally stop at safe token/line boundaries after 20 ms or 65,536
tokens. This is a cooperative budget, not a hard deadline: a single multiline
regex match or embedded lexer can run beyond a viewport/slice boundary. Chroma
does not provide an incremental-edit API; this adapter is not Vim's complete
syntax engine. Delegating/non-regex lexers without restartable state use a
conservative prefix-through-demand fallback (limited to 1 MiB and a one-second
between-token budget), not fake independent-line parsing. Lexing failures are
presentation-only and never change text/recovery or invoke external programs.
Worker caches retain at most eight document identities, thin checkpoints after
8,192 entries, and refuse color caches beyond 262,144 spans rather than growing
memory without limit.

`:set` can show/accept the fixed preferences; there
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
| Files | `:w[!]`, `:wnomove[!]`, `:wq`, `:x`, `:q[!]`, `:qa[!]`, `:wa`, `:wqa`, `:e[!]`, `:enew`, `:read`, `:saveas`, `:file`, `:cd`, `:pwd` |
| Buffers/arguments | `:args`, `:argadd`, `:next`, `:prev`, `:first`, `:last`, `:ls`, `:b`, `:bn`, `:bp`, `:bd` |
| Windows/tabs | `:split`, `:vsplit`, `:new`, `:vnew`, `:close`, `:only`, `:tabnew`, `:tabnext`, `:tabprevious`, `:tabclose`, Ctrl-W navigation/splits/resize |
| Ex edits | ranged `:delete`, `:yank`, `:Y`, `:D`, `:put`, `:join`, `:copy`, `:move`, `:sort`, `:retab`, `:normal`, global/inverse-global delete/print |
| Other | `:marks`, `:registers`, `:jumps`, `:changes`, `:undo`, `:redo`, `:preserve`, `:swapname`, `:! COMMAND`, `:Gn[!] [FILE]` |
| Preview | automatic LaTeX/Markdown panel, `%cursor` / editor-cursor save navigation, `:wnomove`, `:toggle[!]`, `:Preview [LINE]`, `:PreviewScroll COUNT` |

Ex ranges support line numbers, `.`, `$`, marks, offsets, `%`, comma/semicolon,
and visual marks. Mouse supports click/drag, double-click word selection,
triple-click line selection, wheel scrolling, and middle-click PRIMARY put.

### Command-line editing

The `:`, `/`, and `?` prompts use the shared backend-neutral `tui/readline`
component and support readline-style editing independently of
the document cursor:

* Left/Right or Ctrl-B/F move; Home/End or Ctrl-A/E go to the start/end.
* Backspace removes the preceding character; Delete/Ctrl-D removes the next.
* Ctrl-U/K kill to the beginning/end, Ctrl-W kills the preceding word, and
  Ctrl-Y reinserts the most recently killed prompt text.
* Ctrl-Left/Right or Alt-B/F move by words; Alt-Backspace/Alt-D delete words.
* Up/Down or Ctrl-P/N browse same-prefix history, restoring your unfinished
  input and its cursor position when returning to the current entry.

Insertion, deletion, and bracketed paste work at the actual prompt cursor.
Unicode positions are rune-safe, long input scrolls horizontally with the
prefix pinned, and prompts are bounded to 16 KiB. Pasted newlines/tabs become
spaces; paste never submits a command by itself. Escape/Ctrl-C cancel.

### Clipboard

The preferred `yy`/`dd` (including counts), visual `y`/`d`, and ranged `:Y`/`:D`
also queue an **OSC 52** copy request to the hosting terminal. Govim does not
connect to a display server or spawn a selection-owner helper. Native selection
ownership, large transfers and persistence are the terminal's responsibility.

Internal registers are separate and immediate: ordinary `p/P`, `"0p`, named
registers, undo and editing never query or wait for the host clipboard. The
copy request is flushed separately from register storage. Only explicit `"+`
(clipboard) or `"*` (primary selection) reads wait asynchronously for an OSC 52
reply; requests can be cancelled with Escape and time out after two seconds.
External replies do not replace the unnamed/internal register.

In st-go, `allowclipboardwrite` defaults to true; clipboard **reads** require
`"allowclipboardread": true` in the terminal configuration for privacy. Both
permissions are independent of window operations. Other terminals must support
OSC 52 and permit the requested operation. Unsupported reads leave internal
registers/document text intact. There is no X11 assumption or clipboard tool
inside govim, so another frontend can implement the same terminal protocol.

### TeX insert mappings

In TeX files (`.tex`, `.ltx`, `.latex`, `.sty`, `.cls`), Insert-mode mappings
include `` `a `` → `\alpha`, `` `b `` → `\beta`, `` `D `` → `\Delta`, and the
other Greek entries in [`imap/tex.json`](imap/tex.json). Quotation/accent prefixes
such as double backticks and `\` followed by a backtick remain literal. Pasted
text, inserted register text, and non-TeX buffers are not expanded.

With a standalone name on the current line, **F5** (Insert or Normal mode):

* `cases` produces `\begin{cases}`, a blank body line with the insertion cursor,
  and `\end{cases}<++>`. The line's indentation is retained; starred/custom
  environment names work too.
* In a preamble after `\documentclass` and before `\begin{document}`, `amsart`
  produces `\usepackage{amsart}` with the cursor immediately after `}`.
* **Ctrl-J** moves to and removes the next empty `<++>` stop, wrapping among
  remaining stops. In Normal mode it enters Insert at the stop. With no stop it
  does nothing; **Enter** still inserts an ordinary newline.

Mappings and template bodies are embedded JSON, not hardcoded Greek handlers.
The independent [`tui/imap`](../../tui/imap) component supplies longest-suffix
matching, prefix guards, literal variable substitution, and rune-safe cursor
stops. Editing the JSON and rebuilding changes the table. All document changes
use normal insert undo groups and the recovery journal. This is a native subset,
not a Vimscript/LaTeX-suite plugin loader or the suite's interactive wizards.

### TeX compilation

`:Gn [FILE]` (or Ctrl-F5) saves current edits and runs `pdflatex`, resolving a
fragment's `%main=...` relative to the fragment. Missing `.tex` suffixes are
accepted; redirect cycles are rejected. `:Gn!` uses nonstop mode, runs `bibtex`
when an `.aux` exists, and runs three additional `pdflatex` passes, matching the
preferred command. F5 in TeX files is reserved for expansion. Filenames are passed as argv,
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
```

Tests use synthetic text and cover command composition, optional comparison
against an installed Vim **as a test oracle only**, randomized editing/history,
journal corruption/locking/write-sync failures, file conflicts, encodings,
rendering/parser bounds, single colored edit commits (including delayed workers),
changed-cell ANSI output, incremental/full-lexer equivalence after randomized
edits, lazy viewport/convergence work bounds, TeX math state, split demand,
PTY resize/paste/mouse/termios cleanup, and SIGKILL
recovery in insert mode for both flush triggers. Fuzz targets cover the parser
and editor state machine. No private reference-document text is copied.
