# readline

`st-go/tui/readline` is an independent, pure-Go single-line editing component.
It owns bounded UTF-8 text, a rune cursor, a protected-prefix boundary,
history/draft navigation, kill/yank, horizontal viewport calculation, and the
in-process pure-Go Pinyin mode shared by every consumer.
It does not read stdin, write ANSI sequences, execute commands, access a
clipboard, or know about X11, framebuffer, browser, or window APIs.

```go
line := readline.New("/", 1, 4096) // protect the search prefix
defer line.Close()
result := line.Handle("\x01")    // Ctrl-A
line.Insert("hello")             // also suitable for single-line paste
display := line.ViewDisplay(60, nil)
// Render display.Candidates in a contrasting style between Before and After.
// Caller handles result.Submit/Cancel/Err.
// On an existing timer/input tick, call line.Poll() and redraw when
// result.DisplayChanged is true; decoder work never blocks Handle.
```

`Handle` accepts printable runes, control keys and names such as `<Left>`,
`<Delete>`, `<C-Right>`; lowercase arrow names are accepted for existing TUI
parsers. `FeedByte` assembles fragmented UTF-8 for byte-oriented callers.
`CSI` maps common decoded CSI payloads to key names. Transport decoding,
completion popups, rendering, and submitted-command behavior stay in the app.
`View` accepts a cell-width callback for app-specific ambiguous-width or ASCII
rendering. `ByteCursor` is available to adapters with UTF-8 byte-based views.

Typing `[[[` enters Pinyin mode. Lowercase spelling and apostrophes produce up
to five numbered choices; `1`–`5` selects, `+`/`-` pages, and Backspace edits
the spelling. `ViewDisplay` returns choices immediately after the insertion
cursor as transient text so callers can color them without adding them to the
line. Escape leaves IME mode without canceling readline and preserves the
opening delimiter and spelling. A successful `]]]` exit removes both delimiter
triples. Programmatic insertion/paste does not trigger the mode. Set
`DisableIME` when literal delimiter handling is required. Candidate lookup is
coalesced on a bounded worker around the process-wide immutable decoder. Call
`Poll` from the application's existing input/timer loop and `Close` if a live
line is discarded.

Consumers: govim commands/searches, zat commands/searches/open-path entry,
GoLS document searches/commands/corpus search, file-browser path/command entry.
