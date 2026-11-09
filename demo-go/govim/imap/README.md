# Embedded TeX insert mappings

`tex.json` is build-time data consumed by `tui/imap`, not a Vim plugin or runtime
configuration file. Its Greek variants and quote/accent protections follow the
supplied `TEST-ASSETS/vim91/ftplugin/latex-suite/main.vim`. The lowercase `w`
remains `\omega` for the Greek mapping instead of the suite's later wedge alias;
uppercase `P` supplies `\Pi` too.

All IMAP strings, supported extensions, placeholder spelling, and F5 environment/
package bodies are declared here. The editor adapter only chooses a context,
validates the typed name, and applies the returned edit through normal undo and
swap journaling. The first `<++>` is consumed as the initial cursor position;
remaining stops are consumed by Ctrl-J.

Edit the JSON and run `make govim` to add/change text expansions. A new literal
suffix rule can use any printable trigger, not only a backtick/Greek key. See
[`tui/imap/README.md`](../../../tui/imap/README.md) for the schema and mechanism.
