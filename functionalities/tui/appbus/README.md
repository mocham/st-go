# Terminal application channels

`appbus.Client` is event-loop owned. Create it with `ST_GO_APPBUS_TOKEN`, call
`Tick` while reading terminal input, `Publish` structured JSON, pass incoming
`app-bus` DCS controls to `Receive`, and call `Close` before handing off the TTY.
These implement subscription, broadcast publication, and unsubscription.

The terminal brokers channels across its native tabs or one web session service.
Subscriptions belong to an application identity and an owning PTY, never a
focused tab. Background readers receive the same messages. A publisher receives
its own publications. All payloads remain application-defined; govim's versioned
contract is in `../writing` and contains no OpenCode HTTP types.

Wire controls (DCS `ESC P` / ST `ESC \\` framing):

```text
app-bus auth TOKEN APPLICATION subscribe CHANNEL
app-bus auth TOKEN APPLICATION publish CHANNEL MESSAGE PART COUNT BASE64
app-bus auth TOKEN APPLICATION poll
app-bus auth TOKEN APPLICATION unsubscribe

app-bus APPLICATION ready
app-bus APPLICATION message SENDER SEQUENCE CHANNEL MESSAGE PART COUNT BASE64
app-bus APPLICATION error REASON
```

Tokens are per terminal and supplied in the PTY environment. Application/message
identities are 128-bit random hex values; the broker binds sender identity to the
authenticated origin. Channel names use 1–96 ASCII letters, digits, `._:-`.
Sequences are terminal-assigned monotonic broadcast positions. Chunks contain
up to 2,048 decoded bytes; complete JSON payloads are limited to 2 MiB. Frames fit
the applications' existing 8 KiB input limit. Reassembly rejects missing or
out-of-order chunks and expires incomplete messages after 30 seconds.

Polling is a transport detail: it prevents unsolicited replies from landing in a
shell during a TTY handoff and avoids cross-terminal parser-lock acquisition.
Subscriptions renew every 150 ms, expire after five seconds without activity,
and drain one frame per poll (immediately polling again after receiving a chunk).
Replies stay below the PTY input buffer capacity to avoid duplex backpressure
deadlocks while publishing a large payload. The broker permits 64 subscriptions and bounds
each mailbox to 2,048 frames / 4 MiB. Overflow discards the mailbox and reports
`overflow-resynchronize`. Delivery is live-only. Application request IDs,
immutable snapshots, status reconciliation, and saved history provide recovery;
never blindly resend a possibly accepted generation request.
