# OpenCode TUI

An OpenCode V2 client with a session sidebar and rich Markdown/math responses.
OpenCode's server owns agents, tools, models, and permissions; this client owns
the terminal interface.

## Build and connect

```sh
make st opencode-tui
./BINARIES/st -e ./BINARIES/opencode-tui --server http://127.0.0.1:4096 /path/on/server
```

Use the running server's actual URL. `DIRECTORY` is a server-side path; use an
absolute path for remote projects. `--session ses_...` resumes a session in its
existing location. The OpenCode executable is not required on the client machine.
Exiting detaches; server execution continues independently.

Environment settings:

- `OPENCODE_TUI_SERVER`: default endpoint; otherwise `--server` is required.
- `OPENCODE_TUI_PASSWORD`: HTTP Basic password, with username `opencode` by default.
- `OPENCODE_TUI_USERNAME`: override the Basic username.
- `OPENCODE_TUI_AUTHORIZATION`: complete authorization header; overrides Basic settings.

Use the server operator's authentication settings. A `401` means credentials are
needed; EOF before a response usually indicates an address, port, or HTTP/HTTPS
mismatch. Browser sign-in is not inherited by this client. For containers, use
a reachable server address rather than another container's loopback address.

## Rich responses

Native previews use selectable-text PDFs and vector math. Browser mode skips
PDF generation and sends HTML/SVG to st-web:

```sh
make st-web opencode-tui
./BINARIES/st-web -e ./BINARIES/opencode-tui --web --server http://127.0.0.1:4096 /project
```

Markdown pipe tables render with column alignment, wrapped cells, and inline
formatting/math in both modes. PDF tables repeat their header across pages;
wide browser tables scroll horizontally within the preview.

Responses update while streaming. F2 toggles rich/text display; Esc returns focus
from browser previews to the composer. `--no-preview` uses text only and takes
precedence over `--web`. Unsupported terminals automatically fall back to text.

Tool calls appear inline in message order: status, the command being executed,
and referenced filenames. Saved tool output is exposed as a named message `.txt`
file. Live events update these summaries; history restores them on reconnection.

Press **Tab** to replace the left list with **files from the selected prompt's
messages**. Click a file to preview it on the right. Tab returns to the prompt list
and response, preserving unfinished input and list scroll. The list includes tool
file arguments, patch paths, changed files, local Markdown links, message
attachments, and saved tool output. Streaming does not steal a selected preview.

Source files are fetched from the OpenCode server on click (current saved content,
not a historical version). Attachments and tool output use their saved message
content. Markdown renders with math; other text/source files display as code.
PDFs and common images use the native rich pane. **F3** reloads the selected file.
Downloads are bounded at 16 MiB; text previews display up to 256 KiB. Deleted or
unavailable files show an error in the preview.

## Navigation and controls

The sidebar groups prompts by session, showing workdir and title. Click a session
to expand it, a prompt to read its response, or **More** for older prompts.
Click the already-selected prompt again to jump to the end of its response.
Every session is boxed. The selected/connecting session is highlighted; sessions
with work in progress show **Awaiting response**, including folded sessions.
Inactive sessions use muted borders.
**+ New session** creates one; **Delete** opens a confirmation for the named
session and its children. Type exactly **DELETE** and press Enter to delete,
or press Esc/Ctrl-C to cancel. Your unfinished prompt is preserved.
The header displays model, variant, and context usage.

| Key | Action |
| --- | --- |
| Enter | Send the single-line prompt |
| Up/Down | Prompt history |
| Tab | Switch prompts / message files |
| Click a message file | Preview its contents on the right |
| More / F3 | Load 10 older prompts; in file mode, reload selected file |
| PgUp/PgDn | Scroll session/prompt or file list |
| Alt-Up/Down | Select newer/older prompt, or previous/next file |
| Alt-PgUp/PgDn | Scroll response |
| Ctrl-U / Ctrl-D | Move preview up / down one full screen (also in message-file previews) |
| Mouse wheel | Scroll pane under pointer |
| F2 | Toggle rich/text response |
| Ctrl-C | Interrupt active work; quit while idle |
| Ctrl-Q | Quit |
| `y` / `a` / `n` | Allow once / always / reject permission request |

`/sessions`, `/models`, and `/variants` open searchable pickers. Type to filter,
use arrows to select, Enter to apply, and Esc to cancel. Ctrl-R reloads.
Model/variant selections persist on the server. Bracketed paste is supported;
multiline pasted text is normalized to the single-line composer.

## Export to GoLS

Click **Export** on a session, or enter **`/export`** for the current session.
Edit the proposed filepath and press Enter to save; Esc/Ctrl-C cancels. The
default is `<workdir>/<first three words of the session title>.sqlite3`, keeping
spaces between words. The composer and selected preview are preserved.

The archive is written on the machine running `opencode-tui`. Relative paths
are resolved against the session workdir; `~/` expands to the local home directory.
For a remote workdir, enter an existing local directory. Existing files are never
replaced; choose another filename to export again. `/export /path/to/file.sqlite3`
prefills that path and still opens the filepath prompt.

Exports include the full saved history, beyond the sidebar's loaded prompts,
up to the latest recorded message when export starts. GoLS shows an overview
and one Markdown document per prompt/response turn, preserving math, tables,
inline tool summaries, and saved text output. Complete API message records,
including embedded attachments and additional fields, are retained in the
`session_messages` table. Referenced source files are not fetched into the archive.

```sh
./BINARIES/st -e ./BINARIES/gols '/project/First three words.sqlite3'
```

## Responsiveness and limits

HTTP requests and SSE events run independently of terminal rendering. Reconnect
reloads session state, with periodic history refresh as a fallback. Ambiguous
prompt failures reconcile with server history rather than automatically resending
and risking duplicate work. Long-running sessions have no prompt-duration cutoff.

The client supports session management, live responses/tools, message-file
previews, model selection, and permissions. Sending attachments, multiline
editing, forms, diffs, and PTY views are not
implemented. API integration is isolated in [`api.go`](api.go); see the
[OpenCode V2 client documentation](https://opencode.ai/v2/docs/build/client).

```sh
GOWORK=off go -C demo-go/opencode-tui test ./...
```
