# Project-scoped AI writing

Build `make st govim opencode-tui` and restart st so the new terminal channel
protocol is available. Run both applications in **tabs of the same st instance**
(or the same st-web service). For example, in their respective terminal tabs:

```sh
# In each terminal tab, use the same local working directory:
cd /local/project

# First tab:
/path/to/st-go/BINARIES/opencode-tui --server 127.0.0.1:4096

# Second tab:
/path/to/st-go/BINARIES/govim paper.md
```

The server must already be running. Existing `OPENCODE_TUI_*` authentication
settings apply. Add `--web` to both applications for st-web. Minimal terminal
profiles support the channel protocol; native PDF previews require a full build.

Select the desired **normal OpenCode session** in opencode-tui (use `/sessions`
to reopen an existing session), then enter:

```text
opencode-tui:  /subscribe paper
govim:         :Subscribe paper
```

`/Subscribe` is also accepted. The channel is explicit and case-sensitive; use
the same name in both applications. `/subscribe` binds the **currently selected
session**. If a sidebar selection is still loading, the command waits for that
selection to succeed and confirms the bound session ID. Cancelling or changing
the selection cancels the queued subscription. Background writing updates keep
your selected session; click the subscribed-session activity row to return to it.
The project is each application's canonical local working directory at
subscription time, without searching parent directories. Govim creates
`$PWD/.govim/ai.sqlite3` automatically if missing and loads existing history.
The optional positional opencode-tui directory remains a server-side path.

Both interfaces show connection status. Missing responders, different working
directories, and unsupported terminals produce visible diagnostics. Govim requires
a connected responder before creating a placeholder. Use a separate channel for
each responder. Repeating `/subscribe` confirms the bound session; the writing
activity panel shows its connection and workdir.

## Writing loop

1. Use `:AICheckpoint` at the start of the source you want to send. It inserts a
   standalone comment before the current line:
   `<!-- govim-ai:checkpoint -->` in Markdown or `% govim-ai:checkpoint` in LaTeX.
   This is an ordinary undoable source edit and works before subscribing.
2. Put the cursor just after the context you want included (for example, on the
   blank line after a paragraph), then `:AINext [instruction]`. The prompt contains
   only source **after the last checkpoint and before that cursor**. The command
   captures this range before moving to its UUID placeholder after the paragraph.
   Continue editing elsewhere while it runs.
3. The single preview shows the projected document with a shaded pending box.
   Exposed provider reasoning appears while working. When the prediction arrives,
   the preview jumps to the box's rendered HTML/PDF anchor and collapses reasoning.
4. `:AIEdit [UUID]` edits the proposed source in the existing editor pane.
   Adaptations are periodically saved to project history and a private draft file.
   `:AIRefine instruction` requests a new generation using the current adaptation.
5. `:AIIns [UUID]` replaces that comment with the selected adaptation, as one
   journaled undo operation. Save the document normally. `u` restores the marker
    and makes the proposal available again.

Checkpoints are manual and remain in the source as comments. Add another when
you want subsequent requests to start later; the existing OpenCode conversation
retains earlier context. Without a checkpoint, the range begins at the start of
the document. A range longer than 24,000 Unicode characters asks for a closer
checkpoint. Instructions and the current adaptation are also included when used.
Retry/refinement uses the source placeholder as its endpoint, including when
invoked from a draft or the proposal picker.

Commands without a UUID select the marker under the cursor or the active draft.
Otherwise they open a proposal picker: **j/k** or arrows, **Enter**, **Esc**.
An unambiguous UUID prefix is accepted. Missing or duplicated markers prevent
insertion. A prediction never follows a displaced cursor.

Other commands:

| Command | Action |
| --- | --- |
| `:AICheckpoint` | Insert a commented context boundary before the current line |
| `:AIList` | List proposal IDs, documents and states |
| `:AIView [UUID]` | Focus a proposal in the document preview |
| `:AIReasoning [UUID]` | Expand/collapse exposed reasoning |
| `:AIRetry [UUID]` | New request at the same marker, using a fresh snapshot |
| `:AICancel [UUID]` | Cancel queued/running generation |
| `:AIDismiss [UUID]` | Hide proposal and cancel generation; retain its source marker/history |
| `:AIBack` | Return from the draft to the source and ordinary preview |
| `:Subscribe Channel` | Connect using the current working directory; inspect an existing subscription |

Requests are ordinary prompts in the session selected by `/subscribe`. Its live
response, exposed reasoning, tools, and permissions appear in the normal
opencode-tui interface. `/models` and `/variants` change that session's settings,
just as for typed prompts. The writing activity panel shows the channel, local
workdir, subscribed session, request state, and errors; click to open the session.

Browsing another session does not move the subscription. To bind a different
session, select it and run `/subscribe paper` again after active writing requests
finish or are cancelled. Requests in the subscribed session run sequentially.

## Persistence and recovery

Project files are stored under `.govim/`:

- `ai.sqlite3`: requests, full assistant text, exposed reasoning, extracted
  predictions, author adaptation revisions, acceptance records, and lifecycle
  events. Final responses are immutable. Each acceptance identifies the exact
  adaptation revision and resulting document hash.
- `ai-snapshots/`: content-addressed UTF-8 source objects and immutable request
   manifests containing paths, hashes, monotonically increasing buffer revisions,
   a zero-based rune cursor offset, and the selected context range. Edits, undo
   and redo advance that revision.
- `ai-drafts/`: journaled, editable proposal source files.

Each request is a full, hash-verified snapshot synchronization boundary; unchanged
objects are reused by hash. Only local paths and request metadata travel to the
responder. Full snapshots remain local for consistency and history. The responder
sends only the recorded checkpoint-to-cursor excerpt to OpenCode; it does not add
following text, earlier outlines, or definitions from other buffers. The range
is frozen with the snapshot, so edits during generation cannot expand the prompt.

The normal govim recovery journal remains responsible for source edits. AI history
does not save the author's document automatically. If opencode-tui exits or crashes,
reopen the same normal session with `/sessions` (or `--session ID`) and run
`/subscribe paper` again. If govim restarts, run `:Subscribe paper` again too.
Status checks recover the saved response, including reasoning. An ambiguous prompt
acknowledgement is never automatically resent. Unacknowledged requests become
`unknown` after 15 seconds instead of staying queued indefinitely. If no saved
execution can be identified, `:AIRetry` starts a new, explicit request.

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
