# Go corpus browser

GoLS combines the file browser's terminal UI with immutable SQLite archives and
native LaTeX rendering. It opens corpus databases directly in read-only,
immutable mode, without an HTTP server, and navigates
**database → corpus → documents → PDF/TeX/Markdown contents**.

Run from the repository root:

```sh
make gols
./BINARIES/st -e ./BINARIES/gols /path/to/lectures.sqlite3
./BINARIES/st -e ./BINARIES/gols /path/to/corpus-directory  # recursively finds databases
./BINARIES/st -e ./BINARIES/gols                            # recent databases
```

TeX documents expand archived `\input`/`\include` records. Markdown, including
inline/display LaTeX math, uses the shared [`latexpdf`](../../latexpdf/README.md)
vector-PDF pipeline; other archived text is displayed as source. The final
level is an outline whose entries jump to exact rendered PDF pages. The preview
uses a **3.23×** font scale, compared with the file browser's **1.7×**.

## Navigation

The navigation pane starts focused, visible, and floating over a full-width
preview. Shift+Tab switches floating/pinned-right navigation for future paints
without repainting the current frame. Arrows/Enter descend the hierarchy;
Backspace/Left move up. Selecting a document renders a fit-height preview.
Enter keeps navigation visible and replaces the list with its TOC, whose
entries jump to rendered pages. Tab hides navigation or shows/focuses it.

`s` searches paths/titles/content across the corpus; `/` does the same when no
PDF is displayed. `[`/`]` change rendered pages, and `q` exits.

## Document search

`/query` + Enter searches the **displayed document**: all matches are yellow,
and the current one is orange. `n`/`N` move between occurrences, including on
the same page, wrapping at either end. `?` searches backward; empty `/`/`?`
repeats the previous query. Search works with navigation visible or hidden and
preserves fit-height layout and TOC. `:noh`, `:nohlsearch`, or Escape clears
highlights; `n` can restore the last search.

The same [Unicode shortcuts as zat](../zat/README.md#search-shortcuts) are
available: `/{alpha}` finds **α**, and `/caf\'e` finds **café**. Queries are literal,
case-insensitive, and limited to 1024 expanded bytes and 4096 occurrences.
Search uses embedded PDF text; vector-only math without text cannot be matched.

Fixed-page search uses the terminal's native Poppler matcher, not an external
converter. Its tokenized/chunked replies and digest-checked highlights are
documented with the [terminal protocols](../../platform_neutral/stterminal/README.md#fixed-page-pdf-search).
Use a rebuilt terminal that supports this protocol; corpus browsing stays
read-only.

## Caches and recent databases

Selection previews are cached and reused on entry. The opened page-size variant
stays pinned during window resizing; selecting or reopening at a different size
creates or reuses that variant. Active document search pins its preview until
cleared, so resizing or sidebar changes do not repaginate occurrences.

`r` reopens immutable databases. Changed record digests invalidate and remove
old session previews. Successfully opened databases are saved most-recently-used
first in `$HOME/.cache/gols.json`. Launching without a path displays that list;
selecting an entry moves it to the front for next time.
