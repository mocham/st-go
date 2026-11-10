# readline

`st-go/tui/readline` is an independent, pure-Go single-line editing component.
It owns bounded UTF-8 text, a rune cursor, a protected-prefix boundary,
history/draft navigation, kill/yank, and horizontal viewport calculation.
It does not read stdin, write ANSI sequences, execute commands, access a
clipboard, or know about X11, framebuffer, browser, or window APIs.

```go
line := readline.New("/", 1, 4096) // protect the search prefix
result := line.Handle("\x01")    // Ctrl-A
line.Insert("hello")             // also suitable for single-line paste
text, cursorCell := line.View(60, nil)
// Caller renders text and cursorCell, and handles result.Submit/Cancel/Err.
```

`Handle` accepts printable runes, control keys and names such as `<Left>`,
`<Delete>`, `<C-Right>`; lowercase arrow names are accepted for existing TUI
parsers. `FeedByte` assembles fragmented UTF-8 for byte-oriented callers.
`CSI` maps common decoded CSI payloads to key names. Transport decoding,
completion popups, rendering, and submitted-command behavior stay in the app.
`View` accepts a cell-width callback for app-specific ambiguous-width or ASCII
rendering. `ByteCursor` is available to adapters with UTF-8 byte-based views.

Consumers: govim commands/searches, zat commands/searches/open-path entry,
GoLS document searches/commands/corpus search, file-browser path/command entry.
Vimtex has no independent line prompt: input editing belongs to its child Vim.
