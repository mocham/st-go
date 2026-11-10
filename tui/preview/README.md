# Saved-document preview

`preview` provides backend-neutral saved-source preparation and the existing
pure-Go `latexpdf` PDF pipeline for govim. It does not own a display connection,
spawn an editor/typesetter, watch files, or read terminal input.

`Load(path, reader)` accepts a bounded/encoding-aware reader supplied by the
application. It expands local LaTeX `\input`/`\include`, resolves fragment
`%main=...` directives, records dependencies and source-line mappings, and finds
the first `%cursor` comment. Markdown markers outside literal code are omitted
from rendered output. Treat the returned snapshot as immutable.

`Position(path, line)` and `ExpandedPosition(line)` return millionths of the
preview's scroll range. These are proportional source-line estimates, not
SyncTeX coordinates. Included-file mappings refer to their first occurrence.

`Render(snapshot)` uses a compact 140 mm / **0.64×** font preview layout (80% of
govim's previous 0.8× scale). `FontScaleForPane` maps that size to pane-sized
file-browser PDFs, relative to govim's default 60%-width preview on the same
terminal. Text remains native selectable PDF text; math uses vector paths. The
existing renderer's supported LaTeX/Markdown subset applies. No TeX installation
or system font packages are needed.

Applications own save hooks, render coalescing, stale-result rejection, private
atomic PDF publication, retained-region protocol output, and cleanup. Source and
expanded-output budgets are 64 MiB, with bounded include depth/file/line counts.
