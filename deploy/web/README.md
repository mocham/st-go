# Shared web terminal deployment

This Compose stack runs one persistent `st-web` process and a sibling nginx.
Nginx terminates TLS and applies bcrypt-backed Basic authentication to every
HTTP route and WebSocket handshake. Only nginx publishes a port; Docker is the
shared environment's sandbox. All authenticated users share its files and PTYs.

## Start

From this directory:

```sh
cp .env.example .env
mkdir -p secrets
htpasswd -cB secrets/htpasswd alice
cp /path/to/fullchain.pem secrets/tls.crt
cp /path/to/privkey.pem secrets/tls.key
docker compose up -d --build
```

`htpasswd` is supplied by Apache's utilities package. Add users with
`htpasswd -B secrets/htpasswd bob` (without `-c`, which recreates the file).
The local `.env` and `secrets/` directory are excluded from Git and image builds.

Set `ST_ORIGIN` in `.env` to the exact public origin, such as
`https://terminal.example.com` or `https://terminal.example.com:8443`.
`ST_HTTPS_PORT` controls the published port. `ST_BASE_PATH` defaults to
`/terminal/`; use a leading and trailing slash, including `/` for the root.
Open `ST_ORIGIN` followed by `ST_BASE_PATH` as a top-level browser page.

The image includes CGO-free st-web, file-browser, GoLS, govim and opencode-tui.
Shells start in `/workspace`; `/workspace` and `/home/terminal` use named volumes.
For browser-native documents, run `file-browser --web /workspace`,
`govim --web file.md`, or `opencode-tui --web` inside a terminal.

## Terminal ownership

- **New terminal** creates and attaches a new shell, including when none remain.
- **Refresh terminals** fetches the current shared list and occupancy. There is
  no background list polling or notification stream.
- Each PTY permits one browser attachment. Occupied terminals have disabled
  controls. Stale availability cannot bypass atomic server-side acquisition.
- **Hide** releases control and remembers visibility in that browser's local
  storage. **Show hidden** reveals those entries; **Show** restores one. Processes
  continue while hidden or detached. **Close** terminates the PTY.
- Selecting another PTY navigates the top-level page and releases the old one.
  Application tab requests create independent PTYs in this same service.
- Heartbeats run every 20 seconds by default and receive a server reply. After
  90 seconds without client contact, control expires without killing the PTY.
  Clean disconnects release it immediately. After a disconnection, refresh and
  select the terminal to acquire fresh control; another owner cannot be displaced.

`ST_ATTACHMENT_TIMEOUT` changes the expiry (Go duration, e.g. `90s`);
shorter timeouts also shorten the browser heartbeat interval. `ST_MAX_PTYS`
defaults to 8 and caps simultaneous PTYs (maximum 64). Rendering/media budgets
apply per PTY; aggregate memory therefore grows with the number and size of
active terminals and previews.

## HTTP API

All paths below are relative to `ST_BASE_PATH`:

| Method | Path | Result |
| --- | --- | --- |
| GET | `healthz` | Healthy even with zero PTYs |
| GET / POST | `api/ptys` | List / create PTY |
| GET / PATCH / DELETE | `api/ptys/{id}` | Inspect / rename / close |
| GET (WebSocket) | `api/ptys/{id}/attach` | Exclusive attachment |
| GET | `api/ptys/{id}/view/` | Top-level terminal page |

POST accepts `command` (argv array), `directory` (absolute), and `title`.
Omitted command/directory use the service's configured shell and starting
directory; `-e` applies only to the initial PTY. PATCH accepts `title`.
Malformed requests return 400; occupied attachment/mutation attempts return 409.
The WebSocket `hello` supplies an attachment token; the controlling browser
sends it as `X-ST-Attachment` for rename/close. Unoccupied PTYs need no token.

Browser mutations and attachments require a matching Origin. Non-browser API
clients can omit Origin and supply `X-ST-Service-Request: 1` for mutations:

```sh
curl --user alice https://terminal.example.com/terminal/api/ptys
curl --user alice -H 'X-ST-Service-Request: 1' \
  -H 'Content-Type: application/json' -d '{"title":"work"}' \
  https://terminal.example.com/terminal/api/ptys
```

Nginx preserves Host, Origin, and the complete request path. st-web validates
the configured public origin without trusting forwarded-header overrides.
The internal health route also accepts local Host values for container probes.

## Operations

```sh
docker compose ps
docker compose logs --tail=100 terminal nginx
docker compose restart terminal
```

Restart policies restart exited containers. Health checks report unhealthy
containers but do not restart them. Browser disconnects preserve processes;
service/container restarts end all PTYs. Named-volume files survive restarts.

See [web rendering and tests](../../platform_dependent/web/README.md).
