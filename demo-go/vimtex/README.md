# vimtex

Real Vim on the left, a fit-width LaTeX source viewport on the right, inside st-go.

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
./vimtex -width 50 -gap 3s document.tex -u NONE -n
```

## Updating and navigation

- The first preview renders immediately in a background worker.
- **Save in Vim (`:w`) to update.** The file and local `\input`/`\include`
  dependencies are checked every 250 ms, including atomic rename-style saves.
- Preview redraws are at least **2.1 seconds apart** by default.
  `-gap` must be strictly greater than two seconds. Rapid saves coalesce;
  unchanged contents are not rendered again. Only one render runs at a time.
- **Wheel over the right pane** to move a virtual cursor backward/forward by one
  source block: a paragraph, heading, equation, or environment. Nearby content
  is rendered around that cursor, with whole math blocks and global macro
  definitions preserved. Navigation clamps at the ends and coalesces under the
  same redraw gap. Saving retains the approximate source position.
- Preview content is **fit to the pane width**, with vertical overflow clipped.
  The window is approximate, not an attempt to reproduce final PDF pagination.
- Window resizing preserves the split and updates Vim's PTY dimensions.
- Exiting Vim restores the full-width terminal and removes temporary PDFs.

Rendering uses the repository's `latexpdf` engine, with a tall preview canvas and its
documented LaTeX subset; no external TeX installation is needed. The preview
reflects saved files, not unsaved Vim buffers. Failed renders retain the last
successful preview. Diagnostics go to `/tmp/st-go-<pid>-errors-*.log`.

## Vimscript navigation

The demo installs two commands, without automatic cursor-following or key maps:

```vim
:VimtexPreview       " seek to Vim's current line
:VimtexPreview 120   " seek to source line 120
:VimtexScroll 3      " advance three source blocks
:VimtexScroll -1     " go back one source block
```

`seek` lines refer to the saved root file opened by vimtex; local includes are
accounted for when locating that line in the expanded source. For a mapping:

```vim
nnoremap <silent> <leader>lp :VimtexPreview<CR>
```

Scripts can also emit the protocol directly:

```vim
call echoraw("\<Esc>Pregion secondary seek " . line('.') . "\<Esc>\\")
call echoraw("\<Esc>Pregion secondary scroll -1\<Esc>\\")
```

The demo enables `region secondary scroll report`. st-go then returns framed
input events `DCS region secondary event scroll N ST` (wheel or explicit scroll)
and `DCS region secondary event seek N ST`. The wrapper consumes these events;
ordinary keyboard input goes to Vim. `region secondary scroll pages` restores
st-go's default local PDF-page navigation for other applications.

## Implementation

Vim runs on a child PTY. The wrapper relays its bytes without interpreting or
replacing editor behavior. One output loop inserts preview DCS commands only
between complete escape sequences and UTF-8 characters, avoiding corruption
when Vim output arrives in fragments. The main terminal's narrowed window size
is propagated to the child on `SIGWINCH`.

The viewport PDF is atomically replaced at one private session path. Updates use
`region secondary open PATH fit-width page 1`; the demo owns the source cursor
instead of tracking PDF page numbers. Renderer
caches are released per revision, source/PDF budgets use `st-go/limits`, and
temporary disk storage is bounded to the current PDF plus its replacement.
