# Paragraph harness

`st-go/aiharness` is a terminal-independent Go module for explicit-context
paragraph generation. Govim's adapter is `demo-go/govim/inline_ai.go`.

- `Resolve` reads project database, context file, server, model, and variant directives.
- `Blocks` / `FindBlock` locate UUID anchors using rune offsets.
- `ProjectText` removes bookkeeping for preview without changing source files.
- `Open`, `Submit`, `Latest`, `States`, `RecordAction`, and `Updates` provide a persistent,
  versioned UUID-to-insertion dictionary and bounded asynchronous execution.
- `SubmitWith` freezes a per-request backend so documents sharing a database can
  use different servers, including while other requests are queued.
- `Generator` is the backend interface; `OpenCode` calls V2's advertised stateless
  text endpoint with an exact prompt and optional model. API discovery supports
  both current and earlier V2 route names without retrying generation.
- `NewOpenCode("", "")` discovers/authenticates to the registered local service.
  Explicit URLs matching that service reuse its server authentication; other
  servers can use `OPENCODE_PASSWORD`, inherited `OPENCODE_TUI_*` authentication,
  or an explicit Authorization header.
  Provider credentials and subscription OAuth remain owned by OpenCode.

Model calls receive only the assembled instruction, explicit context, and frozen
current document. Generated paragraphs are not automatically appended to context;
accepted text participates through the next document snapshot. Each regeneration
creates a new version, preserving prior paragraphs and insertion/discard events.

The editor owns source edits, undo, and display. Workers never edit buffers.
Completion is persisted before its notification. Discard changes the dictionary
state before cancelling local work, preventing late completion from reviving it.
Interrupted requests are never replayed automatically. Multiple documents can
share a database; opening it does not reset another editor's active requests.

```sh
GOWORK=off go -C functionalities/aiharness test -race ./...
```

See [the govim workflow](../../demo-go/govim/INLINE-AI.md) for syntax and setup.
