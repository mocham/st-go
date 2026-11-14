# Built-in paragraph writing

With the normal local OpenCode service already running, start govim:

```sh
make govim
./BINARIES/govim paper.tex
```

The document can set `%server=`, `%model=`, and `%variant=` (see below).
`--ai-server` and `--ai-model provider/model[#variant]` override those directives;
`GOVIM_AI_SERVER` and `GOVIM_AI_MODEL` provide fallbacks when the directives are absent.
Without a selected model, the server's default applies.
When no server URL is configured, govim discovers the registered local service.
For a specific server, use `--ai-server http://127.0.0.1:4096` or `%server=`.
An explicit URL is honored even if the normal service uses another port.
`--ai-instruction '...'` replaces the built-in paragraph instruction.

The editor calls OpenCode's experimental **stateless** text endpoint directly.
There is no subscription setup or chat-session context. It works in an ordinary
terminal; graphical previews use st/st-web as usual.

### Subscription and server authentication

OpenCode's existing provider login, including a ChatGPT subscription, handles the
model request. Govim does not need a provider API key. It authenticates to the
**OpenCode server**, which is a separate step:

- The normal local service URL and password are read from
  `$XDG_STATE_HOME/opencode/service.json` (default `~/.local/state/opencode/service.json`).
  This also works with an explicit URL matching the registered service, including
  `localhost`/`127.0.0.1` aliases on the same port.
- For a separately launched/password-protected server, `OPENCODE_PASSWORD` sets
  its Basic-auth password (username `opencode`).
- Existing opencode-tui settings also work: `OPENCODE_TUI_AUTHORIZATION` supplies
  a complete header, or `OPENCODE_TUI_PASSWORD` and optional
  `OPENCODE_TUI_USERNAME` supply Basic authentication (default username `opencode`).
- `GOVIM_AI_AUTHORIZATION` overrides automatic authentication with a complete
  server Authorization header. No authentication values are stored in the project database.

Authentication precedence is: `GOVIM_AI_AUTHORIZATION`,
`OPENCODE_TUI_AUTHORIZATION`, matching local-service registration,
`OPENCODE_PASSWORD`, then `OPENCODE_TUI_PASSWORD`. Variables must be exported in
the process that launches govim; an already-running terminal does not inherit
later changes made in another shell. Server credentials are not provider keys.

Govim reads `/openapi.json` to select the stateless route supported by the server:
`/api/experimental/generate/text`, or `/api/experimental/generate` on older V2
releases such as 2.0.32. Generation is never retried automatically.

For HTTP 401, check `opencode service status` and `opencode api get /api/info`.
If the normal service has a different port than `%server=`, remove that directive
to use automatic discovery, or correct it. After fixing authentication, `:AI`
explicitly retries failed slots.

## Document directives

Put these optional, whole-line directives in the document, including Markdown:

```text
%project=history/book.db
%context=writing-notes.txt
%server=http://127.0.0.1:4096
%model=provider/model
%variant=high
```

- `%project=` selects the SQLite project database. It and its parent directories
  are created on first AI use if missing. Several documents can share a database.
- Without `%project=`, the database is the complete document filename plus `.db`:
  `paper.tex.db` or `notes.md.db`.
- `%context=` reads one UTF-8 text file literally. Relative paths in both
  directives resolve beside the document, independent of `:cd` or the shell's
  working directory. A missing context file reports an error.
- `%server=` selects the OpenCode V2 server when `--ai-server` is absent.
- `%model=provider/model` selects the generation model; `%variant=NAME` selects
  its variant. `%model=provider/model#variant` also works, with `%variant=` taking
  precedence. A variant needs a model from `%model` or `GOVIM_AI_MODEL`.
  An explicit `--ai-model` overrides both model and variant directives.
- Each request freezes **instruction + context file contents + the entire current
  unsaved document**, identifying the UUID to fill. Later edits cannot change that
  request. Combined inputs are limited to 4 MiB.
- No project-directory scanning, `AGENTS.md`, earlier chat, or generated memory
  notes are added. The context file is the author's explicit, editable memory.

## Insert-mode workflow

In a named Markdown or LaTeX document, type `{{{` in Insert mode. Govim expands it
into a UUID-tagged **Initial** slot without requesting a paragraph:

```text
Opening text. {{{%a4878296-bd43-43b5-b0ce-88a84e1cd2c3
<++>}}}
```

The cursor stays after the closed block so you can continue writing and add more
slots. Pasted, register-inserted, and Ctrl-V-quoted braces remain literal.

Run **`:AI`** to request every unfilled Initial slot in the current buffer, or
`:AI UUID` to request one specific slot. Each request targets its own UUID using
the same frozen document/context snapshot. Processing, Ready, and Inserted slots
are skipped, so repeating `:AI` does not duplicate their requests. Failed requests
return to Initial and may be retried explicitly with `:AI`.

**Ctrl+J**, in Normal or Insert mode, jumps to the next `<++>` stop, wrapping at
the end of the buffer:

- At a ready AI stop, it inserts that UUID's paragraph as one undo operation,
  enters Insert mode, and triggers the equivalent of `:toggle!` to preview the
  unsaved document. The UUID and braces remain in source for later tracking.
- At a running AI stop, it leaves the slot intact. Press Ctrl+J again when ready.
- At an Initial AI stop, it navigates there and leaves it intact; use `:AI` to request.
- Ordinary snippet stops retain their usual removal behavior, also in Markdown.
- Missing, edited, or duplicated AI anchors are not used for automatic insertion.

`:UnIns` discards the block under the cursor, or the last active AI block in the
current buffer. `:UnIns UUID` selects one explicitly. It restores `<++>`, cancels
local in-flight work for that version, and refreshes the unsaved preview. The next
`:AI` requests a **new version of the same UUID** using fresh inputs. Late results
cannot replace a discarded version.

`u` undoes acceptance or removal as an ordinary source edit. `:w` saves normally.
Generation, acceptance, and preview never implicitly save the document. Previews
hide UUID/bracket bookkeeping and all five AI directives.

## Preview slot status

The preview footer shows UUID prefixes in document order, updating independently
of saving or rendering the document:

| State | Color | Meaning |
| --- | --- | --- |
| Initial | Gray | Created, discarded, or failed; awaiting `:AI` |
| Processing | Yellow | Queued or generating |
| Ready | Green | Generated paragraph available for Ctrl+J |
| Inserted | White | Paragraph present inside the source block |

Deleting a UUID block removes its footer entry immediately; its database history
remains. Undoing acceptance returns the slot to Ready. Entries wrap within the
preview width; excess entries show an overflow count in a small pane. The footer
requires a rebuilt st/st-web with the retained status protocol (`make st` or
`make st-web`), in addition to `make govim`.

## Dictionary and recovery

The project database contains:

- `govim_ai_versions`: UUID, monotonically increasing version, document path,
  instruction, exact context/document snapshots, requested model/variant, rendered
  prompt, result, status, and timestamps.
- `govim_ai_events`: accepted/discarded text and the associated UUID/version.

Previous generated paragraphs survive discard and regeneration. Completed results
can be inserted after restarting govim, even without a configured server.
Normal source edits retain govim's existing swap/undo recovery.

The stateless endpoint returns final text, so this workflow displays a running
status rather than streamed reasoning. Requests are bounded to 16 outstanding
jobs per open project harness and dispatched one at a time. A larger batch reports
how many were queued; run `:AI` again as capacity becomes available. There is no
automatic generation retry. A request left Processing by another editor or a
crash must be discarded with `:UnIns` before `:AI` can retry it. Closing the editor
cancels its HTTP requests; cancellation
does not promise that the remote provider stops computing.

The separate, session-backed workflow remains documented in [AI-WRITING.md](AI-WRITING.md).
