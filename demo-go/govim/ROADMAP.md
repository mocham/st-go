# Planned Govim work

Current capabilities are in the [README](README.md). These items are not yet
implemented.

## Priorities

1. **Search/Ex compatibility:** incremental search, more Vim regex/addressing
   behavior, command chaining, general supported `:global` bodies, and buffer
   filters/shell-output reads.
2. **Persistent undo:** restore edit history after clean exit, validate it against
   file contents, and keep it independent of crash-recovery journals.

## Further gaps

Command completion, quickfix/location lists, folding, tags, richer Insert
completion, branching undo, diff/merge, spell checking, session persistence,
and fuller motion/indentation/Unicode behavior.

General Vimscript, plugin loading, arbitrary options, mappings, abbreviations,
and autocommands remain deferred.
