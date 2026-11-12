# Project-scoped AI writing

Build `make st govim opencode-tui` and restart st so the new terminal channel
protocol is available. Run both applications in **tabs of the same st instance**
(or the same st-web service). For example, in their respective terminal tabs:

```sh
/path/to/st-go/BINARIES/opencode-tui --server 127.0.0.1:4096 \
  --ai-project /local/project /server/project
/path/to/st-go/BINARIES/govim --ai-project /local/project /local/project/paper.md
```

The server must already be running. Existing `OPENCODE_TUI_*` authentication
settings apply. Add `--web` to both applications for st-web. Minimal terminal
profiles support the channel protocol; native PDF previews require a full build.

`--ai-project` is a **client-local** directory. The positional opencode-tui
directory is server-side. When omitted, the AI project is discovered from the
local document/work directory using `.govim`, `.git`, or `go.mod`. Both programs
must resolve the same root. `--ai-channel NAME` overrides the project-derived
channel. `:AIConnect` displays its name and responder identity.

## Writing loop

1. `:AINext [instruction]` places a UUID comment after the current paragraph,
   captures unsaved source/include snapshots, and requests the next paragraph.
   Continue editing elsewhere while it runs.
2. The single preview shows the projected document with a shaded pending box.
   Exposed provider reasoning appears while working. When the prediction arrives,
   the preview jumps to the box's rendered HTML/PDF anchor and collapses reasoning.
3. `:AIEdit [UUID]` edits the proposed source in the existing editor pane.
   Adaptations are periodically saved to project history and a private draft file.
   `:AIRefine instruction` requests a new generation using the current adaptation.
4. `:AIIns [UUID]` replaces that comment with the selected adaptation, as one
   journaled undo operation. Save the document normally. `u` restores the marker
   and makes the proposal available again.

Commands without a UUID select the marker under the cursor or the active draft.
Otherwise they open a proposal picker: **j/k** or arrows, **Enter**, **Esc**.
An unambiguous UUID prefix is accepted. Missing or duplicated markers prevent
insertion. A prediction never follows a displaced cursor.

Other commands:

| Command | Action |
| --- | --- |
| `:AIList` | List proposal IDs, documents and states |
| `:AIView [UUID]` | Focus a proposal in the document preview |
| `:AIReasoning [UUID]` | Expand/collapse exposed reasoning |
| `:AIRetry [UUID]` | New request at the same marker, using a fresh snapshot |
| `:AICancel [UUID]` | Cancel queued/running generation |
| `:AIDismiss [UUID]` | Hide proposal and cancel generation; retain its source marker/history |
| `:AIBack` | Return from the draft to the source and ordinary preview |
| `:AIConnect [responder-ID]` | Inspect/select a responder when more than one is present |

The first request forks the initially attached OpenCode session into a dedicated
document-writing conversation; subsequent requests reuse that conversation.
Switching the browsed session does not redirect writing requests. Its ID appears
in opencode-tui's writing status; select it in the session list to inspect live
reasoning, commentary, tools, or permissions. Up to two documents run concurrently;
requests for one document are serialized.

## Persistence and recovery

Project files are stored under `.govim/`:

- `ai.sqlite3`: requests, full assistant text, exposed reasoning, extracted
  predictions, author adaptation revisions, acceptance records, and lifecycle
  events. Final responses are immutable. Each acceptance identifies the exact
  adaptation revision and resulting document hash.
- `ai-snapshots/`: content-addressed UTF-8 source objects and immutable request
  manifests containing paths, hashes, monotonically increasing buffer revisions,
  and a zero-based rune cursor offset. Edits, undo and redo advance that revision.
- `ai-drafts/`: journaled, editable proposal source files.

Each request is a full, hash-verified snapshot synchronization boundary; unchanged
objects are reused by hash. Only local paths and request metadata travel to the
responder. The responder assembles nearby prose, following material, headings,
and declarations into approximately 6,000 additional tokens of source context.
Small documents fit whole. A remote server receives this focused text, not paths
it is expected to read on the client's machine.

The normal govim recovery journal remains responsible for source edits. AI history
does not save the author's document automatically. On application/channel recovery,
status checks reconcile saved OpenCode history, including reasoning. An ambiguous
prompt acknowledgement is never automatically resent. If no saved execution can
be identified, `:AIRetry` starts a new, explicit request.

The final prediction is accepted only when the completed assistant text contains
exactly one pair of request-specific delimiter lines:

```text
<<<GOVIM_PREDICTION_{requestID}_BEGIN>>>
paragraph source
<<<GOVIM_PREDICTION_{requestID}_END>>>
```

Only that block (or the author's selected adaptation) is inserted. Reasoning and
commentary remain in history and previews. Reasoning is available only when the
provider exposes it.
