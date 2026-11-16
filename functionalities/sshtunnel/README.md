# SSH loopback transport

`st-go/sshtunnel` manages `ssh -N -L` forwards for govim's paragraph harness and
opencode-tui. It uses only the Go standard library and the system `ssh` command.

`New(server, destination)` validates a remote loopback HTTP(S) URL and an SSH
`[user@]host`/config alias. An empty server defaults to `http://127.0.0.1:4096`.
`Config.Open(ctx)` starts a private loopback listener and waits for readiness.
Use `Tunnel.DialContext` on a dedicated HTTP transport with proxies and redirects
disabled, retaining the remote URL for Host, TLS verification and base paths.

Call `Close` to kill and reap the child, including after startup/request failures.
Context cancellation also kills it. `Done`/`Err` report unexpected SSH exits.
There is no tunnel lifetime timeout, automatic reconnection or request replay.
OpenSSH handles config aliases, key/agent authentication and known hosts;
`BatchMode=yes` keeps terminal input available to the application.
