# vimtex

Real Vim on the left, a smoothly scrolling fit-width LaTeX preview on the right,
inside st-go.

```sh
make st vimtex
./st -e ./vimtex /absolute/path/to/document.tex
# Try the bundled source:
./st -e ./vimtex demo-go/vimtex/example.tex
# Or, from a shell already inside st-go:
./vimtex document.tex
```

The source must already exist and be a readable regular file. Vim is launched
with your usual configuration; extra arguments after the filename go to Vim:

```sh
./vimtex -split 1:1 -gap 3s document.tex -u NONE -n
```

The default **Vim:preview split is 4:6** (40% Vim, 60% preview). Use
`-split VIM:PREVIEW` with positive integer weights, for example `-split 3:2`
for 60% Vim and 40% preview. Ratios are rounded to whole percentages, with
1–99% available for the preview. `-width PERCENT` remains available as an
alternative; use one of these options at a time.

## Updating and navigation

- The first preview renders immediately in a background worker.
- **Save in Vim (`:w`) to update.** The file and local `\input`/`\include`
  dependencies are checked every 250 ms, including atomic rename-style saves.
- Saved-document renders are at least **2.1 seconds apart** by default.
  `-gap` must be strictly greater than two seconds. Rapid saves coalesce;
  unchanged contents are not rendered again. Only one render runs at a time.
- **Wheel over the right pane** for immediate, animated pixel scrolling through
  a continuous document. Scrolling uses cached pixels and is independent of the
  save-refresh gap; it does not recompile LaTeX. Navigation clamps at the ends.
  Saving retains the scroll position, clamped if the new document is shorter.
- Preview content is **fit to the pane width**, with narrow padding and unused
  page-bottom whitespace cropped. A **three-pixel divider** separates it from Vim.
- Window resizing preserves the split and updates Vim's PTY dimensions.
- Exiting Vim restores the full-width terminal and removes temporary PDFs.

Rendering uses the repository's `latexpdf` engine, with compact preview pages and its
documented LaTeX subset; no external TeX installation is needed. The preview
reflects saved files, not unsaved Vim buffers. Failed renders retain the last
successful preview. Diagnostics go to `/tmp/st-go-<pid>-errors-*.log`.

## Vimscript navigation

The demo installs two commands, without automatic cursor-following or key maps:

```vim
:VimtexPreview       " seek to Vim's current line
:VimtexPreview 120   " seek to source line 120
:VimtexScroll 3      " scroll down three wheel steps
:VimtexScroll -1     " scroll up one wheel step
```

`seek` lines refer to the saved root file opened by vimtex; local includes are
accounted for when estimating that line's proportional document position.
This is an approximate source-to-preview jump, not exact typesetting synchronization.
For a mapping:

```vim
nnoremap <silent> <leader>lp :VimtexPreview<CR>
```

Scripts can also emit the protocol directly:

```vim
call echoraw("\<Esc>Pregion secondary seek " . line('.') . "\<Esc>\\")
call echoraw("\<Esc>Pregion secondary scroll -1\<Esc>\\")
```

The demo enables `region secondary scroll report` for framed `seek` events.
In continuous mode st-go handles wheel input and `scroll N` locally. The wrapper
converts `DCS region secondary event seek N ST` into
`region secondary position N`, where position is measured in millionths of the
scroll range (0 = start, 1000000 = end). Ordinary keyboard input goes to Vim.

## Implementation

Vim runs on a child PTY. The wrapper relays its bytes without interpreting or
replacing editor behavior. One output loop inserts preview DCS commands only
between complete escape sequences and UTF-8 characters, avoiding corruption
when Vim output arrives in fragments. The main terminal's narrowed window size
is propagated to the child on `SIGWINCH`.

The saved-document PDF is atomically replaced at one private session path.
Updates use `region secondary open PATH fit-width continuous`. st-go retains the
decoded PDF on its worker and prefetches a bounded strip around the visible area;
scroll frames copy those pixels at roughly 60 Hz. Save-time LaTeX renderer caches
are released per revision. Source/PDF/strip budgets use `st-go/limits`, and
temporary disk storage is bounded to the current PDF plus its replacement.
