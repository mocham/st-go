# Embedded TeX insert mappings

`tex.json` is build-time data consumed by `tui/imap`, not a Vim plugin or runtime
configuration file. Greek variants and quote/accent protections follow Vim-LaTeX
conventions, with `w` mapped to `\omega` and uppercase `P` to `\Pi`.

All IMAP strings, supported extensions, placeholder spelling, and F5 environment/
package bodies are declared here. The editor adapter only chooses a context,
validates the typed name, and applies the returned edit through normal undo and
swap journaling. The first `<++>` is consumed as the initial cursor position;
remaining stops are consumed by Ctrl-J.

Edit the JSON and run `make govim` to add/change text expansions. A new literal
suffix rule can use any printable trigger, not only a backtick/Greek key. See
[`tui/imap/README.md`](../../../functionalities/tui/imap/README.md) for the schema and mechanism.
