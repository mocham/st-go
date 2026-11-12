# Govim

A Go modal terminal editor with Vim-style editing, syntax highlighting, document
previews, Pinyin input, and crash-recovery journals. It runs in st-go or another
ANSI/SGR-mouse terminal and does not require Vim.

```sh
make govim
./BINARIES/govim document.tex
./BINARIES/govim +42 document.tex
./BINARIES/st-web -e ./BINARIES/govim --web notes.md
```

## Live document preview

LaTeX and Markdown automatically open a **40% editor / 60% preview** split in
st-go. Native previews use selectable PDF text and vector math; `--web` uses
browser HTML/SVG. No TeX installation is needed for previews.

- Save to refresh; unsaved typing and external file changes alone do not refresh.
  `:e!` reloads, and saves of included files refresh their parent document.
- Local `\input`/`\include` and `%main=...` roots are supported.
- The first `%cursor` marker controls the save-time jump; otherwise the saved
  editor cursor does. Navigation is proportional to source lines, not SyncTeX.
- `:wnomove[!] [FILE]` saves/refreshes while preserving preview scroll.
- `:toggle` previews disk content at the cursor; `:toggle!` previews unsaved named
  buffers without saving. `:Preview [LINE]` and `:PreviewScroll COUNT` navigate.
- Shift+Tab in Normal mode toggles a clickable source table of contents. Tab
  remains jump-forward. The TOC includes unsaved edits.
- Wheel over the preview scrolls locally, independently of source rendering.

One background render runs at a time and rapid saves coalesce. Changed previews
are at least **2.1 seconds apart** by default; `--preview-gap` changes this.
Failed/stale work preserves the last successful preview. Native text uses a
0.6848× scale on a 140 mm page, also the file-browser size reference.

Options: `--preview-width PERCENT`, `--preview-bg '#RRGGBB'`, `--preview-gap 500ms`,
and `--no-preview`. Rendering uses the [latexpdf subset](../../functionalities/latexpdf/README.md).

### Browser-native previews (`--web`)

Use `--web` inside st-web for HTML/SVG previews with native selection/scrolling.
HTML/XHTML renders directly; CSS/JSON shows source. Scripts are disabled; Esc
returns focus to the editor. Split, save hooks, source navigation, and TOC remain
available. Large documents use atomic chunked transfers, up to 64 MiB.
X11/framebuffer discard HTML, so omit `--web` on those hosts.

## Recovery and saving

Edits, undo, and redo are journaled before changing the buffer. Journals are
private and locked, normally `.FILENAME.govim.swp` beside the file, with
`$XDG_CACHE_HOME/govim/swap` as fallback. `--swap-dir DIR` selects another location.

- An existing swap opens the disk file read-only (`[RO]`) with recovery guidance,
  including when another editor owns the swap. Viewing and quitting leave that
  journal untouched. Use `govim -r FILE` to recover after its owner has exited.
  Read-only buffers reject edits and ordinary saves; `:w!` explicitly permits a
  disk overwrite, as with `-R`.
- Journals sync after one second or 64 KiB of edits. `:preserve` forces a sync;
  `--sync-interval` / `--sync-bytes` adjust the policy. Power loss can lose edits
  since the last successful sync.
- Saves use synced atomic replacement, preserve Unix permissions and supported
  encoding/line endings, and detect external changes. `:w!` permits overwrite.
  Atomic replacement does not preserve hard-link identity, ACLs, or xattrs.
- Normal exits remove swaps; forced quit discards unsaved edits. Crashes,
  interruption, and input failures retain recovery files.
- Recovery restores undo history and preserves a damaged journal before repair.
  Journals contain plaintext; they are not Vim's swap format.

```sh
./BINARIES/govim -r document.tex
./BINARIES/govim -r                         # list recovery files
./BINARIES/govim --swap-dir /saved/swaps -r document.tex
```

Review recovery, then `:w`. The status row shows the last successful save time.
Undo history is session-local after a clean exit; persistent undo is not implemented.

## Commands

Counts compose with operators (`2d3w`). `:help` shows the command reference.

| Area | Supported examples |
| --- | --- |
| Movement | `h/j/k/l`, arrows, word/line/paragraph motions, `gg/G`, `f/F/t/T`, `%`, wrapped-row motions |
| Scrolling | Ctrl-F/B/D/U/E/Y, PageUp/Down, `zt/zz/zb` |
| Locations | marks, Ctrl-O/I, `g;`/`g,`, `gf`, Ctrl-^ |
| Editing | insert/replace, delete/change/yank operators, paste, join, indentation, case, formatting, text objects |
| Visual | character, line, and block selection; block insert/append |
| History | undo/redo, `.`, registers, macros |
| Search | `/`, `?`, `n/N`, word search, highlights, `:noh` |
| Substitute | ranged `:s`, `gicn` flags, captures, confirmation |
| Files | `:w[!]`, `:wq`, `:q[!]`, `:qa[!]`, `:e[!]`, `:read`, `:saveas` |
| Buffers/windows | arguments, buffer switching, splits, tabs, Ctrl-W navigation |
| Ex | ranged edits, sort, retab, normal, global/inverse-global delete/print, `:! COMMAND` |

Search supports basic Vim patterns plus `\v` (RE2) and `\V` (literal), not Vim's
full regex engine. General Vimscript/options/plugins, folds, tags, quickfix,
arbitrary Ex filters, and branching undo are absent. See [planned work](ROADMAP.md).

### Command-line editing and clipboard

Prompts support cursor editing, Home/End, Ctrl-A/E, kill/yank, history, and
bracketed paste through [readline](../../functionalities/tui/readline/README.md).

Yank/delete queues OSC 52 copies to the terminal. Ordinary registers and `p/P`
stay local; only explicit `"+` / `"*` reads request the host clipboard. st-go
clipboard reads require `allowclipboardread`; writes are enabled by default.

## CJK input

Type `[[[` in Insert mode or a prompt to start Pinyin. `1`–`5` selects a candidate,
`+`/`-` pages, and Backspace edits spelling. `]]]` ends composition and removes
both delimiters. Escape preserves unfinished delimiters/text. Accepted input
participates in undo and recovery. Lookup runs asynchronously using the bundled
[pure-Go decoder](../../functionalities/googlepinyin/README.md).

## Syntax and preferences

Built-in preferences include line numbers, desert-style syntax colors,
cursor/search/bracket highlighting, four-column expanded tabs, indentation,
mouse selection, UTF-8 with lossless GB18030 fallback, and double-width ambiguous
characters. BOMs, uniform CRLF, and missing final newlines are preserved.

Chroma selects languages by filename/shebang; TeX uses a restartable scanner.
Highlighting runs on visible lines plus a margin, caching state and rejecting
stale edits. Text and colors commit together; unchanged cells emit no output.
This keeps large-file scrolling and input responsive without whole-buffer lexing
on every key. Unicode editing indexes runes, not full grapheme clusters.

## TeX insert mappings and compilation

In TeX files, backtick mappings include `` `a `` → `\alpha` and `` `D `` → `\Delta`.
F5 expands an environment name, or a package name in the preamble. Ctrl-J jumps
through `<++>` placeholders. Rules live in [embedded JSON](imap/README.md).

`:Gn [FILE]` / Ctrl-F5 saves and runs `pdflatex`; `:Gn!` also runs BibTeX and
additional passes. These explicit compilation commands require installed TeX
tools; previewing does not.

## Limits and tests

Buffers allow 16 MiB / one million lines each, 64 MiB total open text, and 64 MiB
undo payload per buffer. Registers/clipboard are limited to 8 MiB. Journals grow
with session edits.

```sh
GOWORK=off go -C demo-go/govim test ./...
GOWORK=off go -C demo-go/govim test -race ./...
```
