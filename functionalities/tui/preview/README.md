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

`Render(snapshot)` uses a compact 140 mm / **0.6848×** font preview layout (7%
larger than the shared 0.64× setting). `FontScaleForPane` maps that size to pane-sized
file-browser PDFs, relative to govim's default 60%-width preview on the same
terminal. Text remains native selectable PDF text; math uses vector paths. The
existing renderer's supported LaTeX/Markdown subset applies. No TeX installation
or system font packages are needed.

`LoadWeb` additionally accepts HTML, CSS and JSON source files. `RenderHTML`
returns semantic HTML and inline SVG math directly, bypassing PDF generation;
`HTMLStyle` supplies the default document stylesheet. HTML input is preserved,
while CSS/JSON source is escaped. The browser host owns isolated presentation.

`EncodeHTML` prepares a bounded HTML/CSS JSON document without unnecessary HTML
escaping. `SendHTML` emits the single-command protocol for small documents and
begin/chunk/commit commands for larger ones, using the application's DCS writer.
The full JSON document may occupy up to 64 MiB; each control message stays below
1 MiB. The terminal publishes only complete documents.

Applications own save hooks, render coalescing, stale-result rejection, private
atomic PDF publication, retained-region protocol output, and cleanup. Source and
expanded-output budgets are 64 MiB, with bounded include depth/file/line counts.
