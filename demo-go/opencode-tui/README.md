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

Responses update while streaming. F2 toggles rich/text display; Esc returns focus
from browser previews to the composer. `--no-preview` uses text only and takes
precedence over `--web`. Unsupported terminals automatically fall back to text.

## Navigation and controls

The sidebar groups prompts by session, showing workdir and title. Click a session
to expand it, a prompt to read its response, or **More** for older prompts.
**+ New session** creates one; **Delete** removes a session and its children.
The header displays model, variant, and context usage.

| Key | Action |
| --- | --- |
| Enter | Send the single-line prompt |
| Up/Down | Prompt history |
| More / F3 | Load 10 older prompts |
| PgUp/PgDn | Scroll session/prompt list |
| Alt-Up/Down | Select newer/older prompt |
| Alt-PgUp/PgDn | Scroll response |
| Mouse wheel | Scroll pane under pointer |
| F2 | Toggle rich/text response |
| Ctrl-C | Interrupt active work; quit while idle |
| Ctrl-Q | Quit |
| `y` / `a` / `n` | Allow once / always / reject permission request |

`/sessions`, `/models`, and `/variants` open searchable pickers. Type to filter,
use arrows to select, Enter to apply, and Esc to cancel. Ctrl-R reloads.
Model/variant selections persist on the server. Bracketed paste is supported;
multiline pasted text is normalized to the single-line composer.

## Responsiveness and limits

HTTP requests and SSE events run independently of terminal rendering. Reconnect
reloads session state, with periodic history refresh as a fallback. Ambiguous
prompt failures reconcile with server history rather than automatically resending
and risking duplicate work. Long-running sessions have no prompt-duration cutoff.

The client supports session management, live responses, model selection, and
permissions. Attachments, multiline editing, forms, diffs, and PTY views are not
implemented. API integration is isolated in [`api.go`](api.go); see the
[OpenCode V2 client documentation](https://opencode.ai/v2/docs/build/client).

```sh
GOWORK=off go -C demo-go/opencode-tui test ./...
```
