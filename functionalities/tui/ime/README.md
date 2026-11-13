# Streaming Pinyin input

`Stream` adapts the shared readline editor to native terminal and XIM input.
Normal input is left to its owner; only possible `[[[` triggers are buffered.
Composition uses readline's candidate numbering, paging and editing. Escape or
`]]]` commits the local composition without delimiters. `Flush` preserves
unfinished literal text when leaving a field or disabling the input method.

Call all methods in one serialization domain. `Notify` runs on the decoder
worker and only wakes that domain; `Poll` applies its result without blocking.
Native owners retain opaque key/paste events while `Pending()` is true, then
replay them in order after polling. This keeps Return, shortcuts and paste from
overtaking a candidate selection. Closing a context cancels its worker.

The engine/dictionary are in `st-go/googlepinyin`; this adapter has no native
display, daemon, clipboard or PTY dependency.
