# Reusable Go modules

These modules are independent of the st-go graphical host. Import paths stay
unchanged; `go.work` selects their local directories while developing this
repository. Each consumer also declares its own local `replace` directives,
so ordinary Go commands work with **`GOWORK=off`**, not just in the workspace.

| Directory | Module | Responsibility |
| --- | --- | --- |
| `tui/` | `st-go/tui` | Line editing, navigation, search, outlines, input mappings, Pinyin client, document preview, audio and tab-spawn protocol clients |
| `latexpdf/` | `github.com/mocham/GoLS/latexpdf` | Native-Go selectable-text/vector-math PDF generation |
| `semhtml/` | `st-go/semhtml` | Sanitized semantic HTML to PDF |
| `ptyutil/` | `st-go/ptyutil` | Unix PTY creation, controlling terminals, resize |
| `limits/` | `st-go/limits` | Shared bounded-file/resource utilities |
| `pdfmeta/` | `st-go/pdfmeta` | PDF metadata/search protocol data |
| `config/` | `st-go/config` | Terminal configuration and embedded defaults |
| `terminfo/` | `st-go/terminfo` | Embedded terminal-description installation |
| `audio/` | `st-go/audio` | Terminal-owned Go MP3 decoding and native ALSA output |
| [`widgets/`](widgets/README.md) | `st-go/widgets` | Widget-local buffers, bounds, visibility, focus, event routing and retained surfaces; no tabs/layout policy |

The terminal engine and neutral protocol contracts remain under
[`platform_neutral/`](../platform_neutral/README.md), with their own modules.
The old `st-go/term` import remains a separate compatibility module.

```sh
go -C functionalities/tui test ./...
GOWORK=off go -C functionalities/latexpdf test ./...
make test-tui                 # all client/helper modules, no graphical stack
make test                    # also native audio and graphical integration
```

`audio` is a **host service**, not a TUI dependency. Its normal Go build uses
system ALSA through pkg-config; `alsa_static` selects the Make-built static
library. `make test-tui` excludes this native host service. No demo module
imports it, XGB, the graphical frontend, or the root `st-go` module.

Each application under [`demo-go/`](../demo-go/README.md) has its own `go.mod`.
Go's `./...` pattern does not cross module boundaries; use `make test-tui` or
explicit module-directory commands to test multiple modules. Standard `go.sum`
files belong to their respective modules; generated executables stay in the
gitignored `BINARIES/` directory.
