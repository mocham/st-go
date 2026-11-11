# OpenCode TUI

A minimal OpenCode V2 terminal client with an st-go rich-response pane. The left
pane lists sessions as a **soft-pink workdir** followed by a **soft-blue session
name**. Click a session to activate it and expand its prompts; the others fold.
Initially the latest 10 prompts are shown, newest first. Click **More** to reveal
10 older prompts at a time. The session catalog and prompt history are paged
from the server, so the list is not limited to the first API page.

Click a prompt to show its response on the right as selectable PDF text with
vector mathematics, or browser-native HTML with `--web`. Multi-step assistant
messages are grouped under their prompt. In particular, Markdown answers can
define and use LaTeX macros such as:

```markdown
\newcommand{\RR}{\mathbb{R}}

The map $f:\RR\to\RR$ is continuous.
```

The client reuses two existing components rather than embedding an agent:

- the official headless `opencode serve` V2 server for sessions, models,
  providers, tools, plugins, and permissions;
- st-go's `tui/preview` and retained display-region protocol for Markdown,
  LaTeX, and PDF presentation.

## Build and run

Connect to a configured, running OpenCode V2 HTTP server. The client only needs
its URL and authentication; the OpenCode binary is not needed on the client
machine. Build and launch the client inside st-go:

```sh
make st opencode-tui
./BINARIES/st -e ./BINARIES/opencode-tui --server http://127.0.0.1:4096 /path/to/project
```

Use the server's actual listening address. Bare addresses such as
`--server 127.0.0.1:4096` use HTTP; use `https://` explicitly for TLS.
`--server` is required unless `OPENCODE_TUI_SERVER` is set.
Exiting detaches the client; the server and any
active session execution continue independently. Ctrl-C explicitly interrupts
active work.

`DIRECTORY` is a path on the server. For a remote server, pass its absolute
project path; the client does not require that path to exist locally. Omitted
or relative paths resolve against the client's current directory. With
`--session`, the existing session's location is used.

Resume a session by ID:

```sh
./BINARIES/st -e ./BINARIES/opencode-tui --server http://127.0.0.1:4096 --session ses_...
```

Set the endpoint and authentication through the environment:

```sh
export OPENCODE_TUI_SERVER='127.0.0.1:4096'
read -rsp 'OpenCode server password: ' OPENCODE_TUI_PASSWORD
echo
export OPENCODE_TUI_PASSWORD
./BINARIES/opencode-tui /path/on/server/to/project
```

Use the password printed by the server at startup or configured by its operator.
`OPENCODE_TUI_PASSWORD` enables HTTP Basic auth; `OPENCODE_TUI_USERNAME`
defaults to `opencode`. For an unauthenticated server, leave it unset.

For other authentication setups, `OPENCODE_TUI_AUTHORIZATION` accepts a complete
header value (`Basic ...` or `Bearer ...`) and takes precedence over the password
variables. Unset it when switching to password authentication. Authentication
and connection errors are reported before entering the TUI. The client does
not inspect OpenCode's service registration or credential files.

For a plain terminal, use `--no-preview` for a text response pane. Without that
flag, a terminal that does not answer st-go's display-region query falls back
to the same session/response layout using text after two seconds.
The full st-go profile is needed to display the generated
PDF; the OpenCode server and client remain backend-neutral.
Run directly inside st-go unless an intervening multiplexer is configured to
pass DCS sequences through.

### Browser-native responses (`--web`)

```sh
make st-web opencode-tui
./BINARIES/st-web -e ./BINARIES/opencode-tui --web --server http://127.0.0.1:4096 /path/to/project
```

`--web` reserves the usual rich-response pane and sends HTML/CSS as base64 JSON
through st-go's DCS region protocol. st-web displays semantic Markdown and inline
SVG mathematics in a positioned HTML overlay, with native selection and scrolling.
Responses update as they stream; F2 toggles the pane and restores its latest
content. Esc returns keyboard focus from the preview to the composer.

This mode bypasses PDF creation and rasterization. Framebuffer/X11 reserve a blank
pane and discard the HTML content. `--no-preview` takes precedence over `--web`.
Large HTML responses use chunked transfers with a 64 MiB JSON document budget;
each DCS message stays within the terminal's 1 MiB control-message limit.

### Docker connections and EOF

With Docker publishing `-p 4096:4096`, the server inside the container normally
needs `opencode serve --hostname 0.0.0.0 --port 4096`. A listener bound only to
the container's `127.0.0.1` is not reachable through that port mapping. When the
client also runs in a container, its `127.0.0.1` refers to that container; use
the server container's network name/address instead.

From the same host or container as the TUI, check the actual API endpoint:

```sh
curl --noproxy '*' --max-time 5 -i http://127.0.0.1:4096/api/info
```

A `401` response proves HTTP connectivity and means credentials are required.
An EOF before an HTTP response means the connection was closed; check the
listening address, port mapping, and HTTP/HTTPS scheme. Use the exact scheme
and port that work in your browser. Opening the web page alone does not verify
the API, and browser authentication is not inherited by the TUI.

## Keys

| Key | Action |
| --- | --- |
| Enter | Send the single-line prompt |
| Up / Down | Browse prompt history |
| Click a session | Activate it and expand its latest 10 prompts; fold the other sessions |
| Click a prompt | Select its response; wrapped lines are clickable too |
| More / F3 | Reveal 10 older prompts from the active session |
| Mouse wheel | Scroll the pane under the pointer |
| PgUp / PgDn | Scroll the session/prompt list |
| Alt-Up / Alt-Down | Select the newer / older prompt |
| Alt-PgUp / Alt-PgDn | Scroll the selected response |
| F2 | Switch between rich and text response panes |
| Ctrl-C | Interrupt active work; quit while idle |
| Ctrl-Q | Quit |
| `y` / `a` / `n` | Allow once / always / reject a permission request |

Bracketed paste is accepted and normalized into the bounded single-line
composer. DCS-looking pasted text is never interpreted as a terminal command.
The selected response updates while it streams. Selecting an older prompt
keeps it selected as newer responses arrive; sending a new prompt selects it.
The composer stays available below the list. Empty, queued, or failed prompts
show a response placeholder instead of another prompt's answer.
While a clicked session loads, sending is paused; Esc cancels the switch. A
failed switch restores the current session. New sessions and session title/path
changes refresh the catalog automatically.

## Slash commands

Type a command and press Enter:

| Command | Action |
| --- | --- |
| `/sessions` | Search and attach to an existing session, or create a new one in the current session's directory |
| `/models` | Choose an enabled model from the current session's project configuration |
| `/variants` | Choose a variant of the current model, including **Default (no variant)** |

Pickers support typing to filter, Up/Down and PgUp/PgDn to navigate, Enter to
apply, Esc or Ctrl-C to cancel, and Ctrl-R to reload/retry. You can start with
a filter, such as `/models gpt` or `/sessions math`. Session searches run on the
server across non-archived sessions, with **Load more sessions** for older pages.
The session list includes project paths so you can distinguish projects.

Model and variant selections are saved on the server for subsequent turns and
shown in the chat header. `/variants` resolves the server's default model when
the session has no explicit selection. Switching to a different model resets
the variant; selecting the same model preserves it. A pending permission request
takes keyboard focus until answered. Switching sessions leaves their executions
running and loads the selected session's prompts, permissions, and response.

## Scope

This demo intentionally implements a small useful slice: session create/resume/search,
an expandable session sidebar, paged prompt history, live assistant text,
reconnect/resync, tool status
after each turn, model/variant selection, prompt/response navigation, interrupts,
and permission replies. It does not yet provide a multiline editor, attachments,
forms, diffs, or PTY views. Those can be layered on the same V2 API
without changing the rich-rendering boundary.

The OpenCode V2 HTTP surface is currently experimental. This client is tested
against the repository's current V2 contract and keeps its API adapter isolated
in `api.go` so schema changes do not leak into terminal rendering.

## Test

```sh
GOWORK=off go -C demo-go/opencode-tui test ./...
```

Tests cover the API subset, searchable pickers, persisted model/variant changes,
session-switch isolation, sidebar paging/folding, mouse hit testing, SSE framing,
input/DCS isolation, display geometry,
and actual Markdown responses containing user-defined LaTeX macros rendered to
PDF and HTML/SVG, including web-pane streaming, hide/restore, and clearing.
