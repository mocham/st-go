# Govim missing features and priorities

This is a backlog, not a list of implemented capabilities. See the
[README](README.md#commands) for current command coverage and
[recovery guarantees](README.md#recovery-and-saving).

## Highest priority: full search/Ex behavior and persistent undo

These two areas take precedence over the remaining feature backlog. Neither
requires a general Vimscript interpreter or plugin loader.

### Full search/Ex behavior

Current support includes `/`, `?`, repeat/word searches, highlights, ranged
substitution with confirmation, common Ex edits, and global/inverse-global
delete or print. Search uses a basic Vim-pattern translation to Go's RE2 engine;
it is not the complete Vim pattern language.

- [ ] Add live incremental search while typing, with cancellation restoring the
  original position and correct operator/motion behavior.
- [ ] Close Vim regex compatibility gaps, including pattern backreferences,
  lookaround, and match-boundary atoms such as `\zs` and `\ze`. Replacement
  references (`\1` through `\9`) already work; pattern backreferences do not.
- [ ] Complete search offsets, search-pattern Ex addresses, and Vim-compatible
  range, delimiter/escaping, substitution/repeat, and command-chaining behavior.
- [ ] Extend `:global` / `:vglobal` beyond delete/print to supported Ex command
  bodies, preserving correct behavior as matched lines are edited or removed.
- [ ] Implement actual buffer filters (`:[range]!command`, including
  `:%!sort`) and shell-output reads (`:read !command`). Running `:!command`
  already works, but is not a text filter.

Compatibility work must keep resource use bounded and preserve text and recovery
on invalid patterns, cancellation, failed filters, and partial failures. Add
focused tests, including comparisons with Vim where applicable. General
Vimscript expression evaluation remains deferred rather than implied by this
search/Ex milestone.

### Persistent undo

Current undo/redo is session-local. Crash recovery reconstructs history from the
swap journal, but a normal clean exit removes that journal; reopening a file
does not restore its undo history. Recovery is not persistent undo.

- [ ] Persist undo history across successful saves, clean exits, and reopening
  the same file, retaining edit-group boundaries and redo where applicable.
- [ ] Validate saved history against the file's contents/identity; stale or
  externally modified files must never receive unrelated undo history silently.
- [ ] Use private, crash-safe history storage with corruption handling and
  bounded retention. History can contain deleted or sensitive plaintext.
- [ ] Keep persistent history separate from active swap locking and recovery;
  failures must not weaken existing write-ahead journaling or safe-save behavior.
- [ ] Test clean reopen, save/undo/redo sequences, external modifications,
  truncated/corrupt history, and interrupted history writes.

Branching undo-tree navigation is also missing and tracked below; persistence
must not depend on completing that separate feature first.

## Remaining missing features

These are lower priority than both areas above; their order here is not a
committed implementation sequence.

| Feature | Missing behavior / current limitation |
| --- | --- |
| Command-line completion | Tab completion for Ex commands, filenames, and buffer names inside govim. File-browser location completion is separate. |
| Quickfix and location lists | Structured compiler-error/project-search results, `:make`, `:grep`, and `:cnext`-style navigation; especially useful for TeX errors. |
| Folding | Collapse/open sections, functions, or environments with Vim-style fold commands. |
| Tags navigation | ctags-based symbol lookup, Ctrl-] definition jumps, and a tag stack. Existing `gf` and jump history are not a tags engine. |
| Richer Insert completion | Filename, dictionary, and omni completion; existing Ctrl-N/P only finds words in the current buffer. |
| Branching undo | Retain alternate edit branches and navigate an undo tree/timeline, including `g-` / `g+`, `:earlier`, `:later`, and `:undolist`. Current history is linear. |
| Diff/merge mode | `:diffthis`, synchronized difference navigation, and hunk transfer. Ordinary splits already work. |
| Spell checking | Spelling highlights, word navigation, and suggestions for prose/LaTeX. |
| Session/state persistence | Restore layouts, marks, registers, and command/search history across launches. Buffers, marks, and registers already work within a session. |
| Remaining editing parity | Tag text objects, corner-case motions/block operations, full filetype indentation, and grapheme-cluster-aware editing. Current Unicode indexing is by rune. |

## Intentionally deferred

General `.vimrc`/options interpretation, custom Vim-style mappings and
abbreviations, autocommands, arbitrary Vimscript, and plugin loading remain
deferred. Builtin preferences and embedded TeX insert mappings do not provide
those general facilities. Native editing, data safety, and the two highest
priorities above come first.
